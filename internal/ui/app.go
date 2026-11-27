package ui

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/KevinXC5/flyshadow-bar/internal/applog"
	"github.com/KevinXC5/flyshadow-bar/internal/cliinstall"
	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/control"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
	"github.com/KevinXC5/flyshadow-bar/internal/version"
)

// RunOptions 配置 GUI 应用启动行为。
type RunOptions struct {
	ConfigPath          string
	CoreLibPath         string
	StartHidden         bool
	ShowOnceOnStart     bool
	NoConnect           bool
	DisableAutoSysProxy bool
}

// App 串联 Engine、本地控制服务、系统托盘、增量更新与 MyGo Native UI 主窗口。
type App struct {
	eng     *engine.Engine
	ctrl    *control.Server
	tray    *trayController
	win       *mygo.Window
	vm        *viewModel
	updates   *UpdateManager
	prefs     config.AppPrefs
	prefsPath string

	updateProgressReset func()

	quitting     bool
	pinWindow    bool // 置顶窗口：开启后失焦不自动收起（--show 启动默认开启，也可由标题栏置顶按钮随时切换）
	lastBlurHide time.Time
	unsub        func()
	stopCh       chan struct{}
	closeOnce    sync.Once
}

// Run 启动单进程 GUI/托盘桌面应用，阻塞直至应用退出。
func Run(opts RunOptions) error {
	mygo.App.SetName("FlyShadow Bar")
	// 正式打包由 mygo build 注入 packageVersion；仅在未注入时回退到 version.Version，
	// 避免覆盖 mygo.Updater 用于匹配增量补丁的当前版本号。
	if v := mygo.App.Version(); v != "" && v != "0.0.0" {
		version.Version = v
	} else {
		mygo.App.SetVersion(version.Version)
	}

	if !mygo.App.RequestSingleInstanceLock() {
		log.Printf("[app] 检测到已有 FlyShadow Bar 实例在运行，唤醒现有实例后退出")
		return nil
	}

	// macOS 作为轻量菜单栏应用，默认隐藏 Dock 图标
	if runtime.GOOS == "darwin" {
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	}

	log.Printf("[app] 启动 FlyShadow Bar: version=%s os=%s/%s startHidden=%v showOnce=%v noConnect=%v noAutoSysProxy=%v",
		version.Version, runtime.GOOS, runtime.GOARCH, opts.StartHidden, opts.ShowOnceOnStart, opts.NoConnect, opts.DisableAutoSysProxy)

	prefsPath := config.PrefsPathForConfig(opts.ConfigPath)
	prefs := config.LoadAppPrefs(prefsPath)
	applyThemeSource(prefs.ThemeMode)

	eng, err := engine.New(engine.Options{
		ConfigPath:          opts.ConfigPath,
		CoreLibPath:         opts.CoreLibPath,
		DisableAutoSysProxy: opts.DisableAutoSysProxy,
	})
	if err != nil {
		log.Printf("[app] 初始化引擎失败: %v", err)
		return fmt.Errorf("初始化 FlyShadow 引擎失败: %w", err)
	}

	app := &App{
		eng:       eng,
		prefs:     prefs,
		prefsPath: prefsPath,
		stopCh:    make(chan struct{}),
	}
	app.updates = newUpdateManager(func() {
		mygo.RunOnMain(func() {
			log.Printf("[updater] 正在重启以应用新版本")
			app.quitting = true
			// 先挂起脱离当前进程组的 macOS 重启看门狗，再交由 MyGo 正常走完退出、换包与拉起流程
			// （cleanup 由 OnWillQuit / OnQuit 统一执行，避免提前清理干扰退出事件循环）
			armRelaunchWatchdog("--relaunch")
			mygo.App.Relaunch("--relaunch")
		})
	}, eng.LocalProxyURL)

	// 启动本地控制服务（供 CLI 与 SSH 终端操作运行中的实例）
	ctrlSrv, err := control.StartServer(eng, control.ServerOptions{
		OnShowUI: func() {
			mygo.RunOnMain(func() { app.showWindow() })
		},
		OnQuit: func() {
			mygo.App.Quit()
		},
	})
	if err != nil {
		log.Printf("[app] 启动本地控制服务失败: %v", err)
		eng.Shutdown()
		return fmt.Errorf("启动本地控制服务失败: %w", err)
	}
	log.Printf("[app] 本地控制服务已就绪: addr=%s", ctrlSrv.Addr())
	app.ctrl = ctrlSrv

	// 再次启动同一二进制时唤醒主窗口
	mygo.App.OnSecondInstance(func(_ []string, _ string) {
		app.showWindow()
	})
	// 防止关闭窗口导致托盘常驻应用退出
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		app.quitting = true
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		app.cleanup()
	})
	mygo.App.OnQuit(func() {
		app.cleanup()
	})

	mygo.App.WhenReady(func() {
		app.onReady(opts)
	})

	if err := mygo.App.Run(); err != nil {
		app.cleanup()
		return err
	}
	app.cleanup()
	return nil
}

func (a *App) onReady(opts RunOptions) {
	initialSnap := a.eng.Snapshot()
	a.vm = newViewModel(a, initialSnap)

	var err error
	a.tray, err = newTrayController(a)
	if err != nil {
		log.Printf("创建系统托盘失败: %v", err)
	}

	// 启动默认静默驻留菜单栏 / 系统托盘，不提前创建主窗口；
	// 仅在显式传 --show（钉住窗口）或 --relaunch（更新重启后弹出一次，失焦仍自动收起）时立即弹窗。
	hidden := opts.StartHidden || mygo.App.WasOpenedAtLogin()
	if !hidden {
		a.setWindowPinned(true)
		a.ensureWindow()
		a.showWindow()
	} else if opts.ShowOnceOnStart {
		a.ensureWindow()
		if a.vm != nil && version.Version != "" && version.Version != "dev" {
			a.vm.queueToastOnShow("已更新至 " + version.Version)
		}
		a.showWindow()
	}

	// 订阅引擎状态快照，实时驱动托盘和主窗口刷新
	a.unsub = a.eng.Subscribe(func(snap engine.Snapshot) {
		mygo.RunOnMain(func() {
			if a.vm != nil {
				a.vm.snap = snap
			}
			if a.tray != nil {
				a.tray.Update(snap)
			}
			if a.win != nil && a.win.IsVisible() {
				a.win.Invalidate()
			}
		})
	})

	// 后台启动本地代理与自动建连，避免阻塞 UI 首次渲染
	go func() {
		if err := a.eng.Start(!opts.NoConnect); err != nil {
			log.Printf("启动引擎失败: %v", err)
		}
	}()

	// 仅在窗口打开且位于「连接」页时按需轮询活跃连接，平时零额外开销
	go a.connectionsPollLoop()

	// 安装完成后自动将命令行工具 fsbar 链接到对应的 PATH 目录（开发调试或隔离验收模式仅做只读检测）
	go a.ensureCLIOnStartup(opts.DisableAutoSysProxy)

	// 绑定 MyGo 签名增量更新的后台自动巡检、静默下载与就绪回调
	if a.updates != nil {
		a.updates.availableFn = func(st UpdateStatus) {
			mygo.RunOnMain(func() {
				if a.updateProgressReset != nil {
					a.updateProgressReset()
				}
				if a.vm != nil {
					a.vm.applyNativeUpdateStatus(st)
					a.vm.updateDownloading = true
					a.vm.updateDownloaded = 0
					a.vm.updateTotal = 0
					a.vm.updateErr = ""
				}
				if a.win != nil && a.win.IsVisible() {
					a.win.Invalidate()
				}
				a.refreshTrayMenu()
			})
		}
		a.updates.readyFn = func(st UpdateStatus) {
			mygo.RunOnMain(func() {
				if a.vm != nil {
					a.vm.updateDownloading = false
					a.vm.applyNativeUpdateStatus(st)
				}
				if a.win != nil && a.win.IsVisible() {
					a.win.Invalidate()
				}
				a.refreshTrayMenu()
			})
		}
		a.updates.installFailFn = func() {
			mygo.RunOnMain(func() {
				if a.vm != nil {
					a.vm.updateDownloading = false
				}
				if a.win != nil && a.win.IsVisible() {
					a.win.Invalidate()
				}
			})
		}
		fn, reset := newThrottledUpdateProgress(func(downloaded, total int64) {
			a.updateWindow(func() {
				if a.vm != nil {
					a.vm.updateDownloaded = downloaded
					a.vm.updateTotal = total
				}
			})
		})
		a.updates.progressFn = fn
		a.updateProgressReset = reset
		a.updates.StartBackground(a.stopCh)
	}
}

// ensureWindow 按需懒创建主窗口，避免默认静默驻留托盘时产生多余窗口对象与显存开销。
func (a *App) ensureWindow() {
	if a.win != nil {
		return
	}
	if a.vm == nil {
		a.vm = newViewModel(a, a.eng.Snapshot())
	} else {
		a.vm.snap = a.eng.Snapshot()
	}
	winH := calcPreferredPopoverHeight()
	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title:             "FlyShadow Bar",
		Width:             500,
		Height:            winH,
		MinWidth:          440,
		MinHeight:         600,
		StateKey:          "popover-v5",
		Hidden:            true,
		SkipTaskbar:       true,
		AlwaysOnTop:       true,
		TitleBarStyle:     mygo.TitleBarHidden,
		HideWindowButtons: true,
		Content:           ui.View(a.vm.render),
	})

	// 点击窗口关闭按钮时隐藏至托盘而非退出，仅在真正退出应用时放行关闭
	a.win.OnClose(func(e *mygo.CloseEvent) {
		if !a.quitting {
			e.PreventDefault()
			a.win.Hide()
			a.refreshTrayMenu()
		}
	})
	// 托盘弹窗体验：点击面板外部自动收起（已置顶、更新下载中或模态弹窗打开时保留不收起）
	a.win.OnBlur(func() {
		if !shouldHideOnBlur(a.quitting, a.pinWindow, a.win != nil && a.win.IsVisible(), a.vm) {
			return
		}
		if a.vm != nil {
			a.vm.connSortMenuOpen = false
		}
		a.lastBlurHide = time.Now()
		a.win.Hide()
		a.refreshTrayMenu()
	})
	a.win.OnShow(func() { a.refreshTrayMenu() })
	a.win.OnHide(func() {
		if a.vm != nil {
			a.vm.clearToastsOnHide()
		}
		a.refreshTrayMenu()
	})
}

func (a *App) connectionsPollLoop() {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopCh:
			return
		case <-ticker.C:
			var needPoll bool
			mygo.RunOnMain(func() {
				needPoll = a.win != nil && a.win.IsVisible() && a.vm != nil && a.vm.activeTab == 2 && !a.vm.connsPaused
			})
			if needPoll {
				a.refreshConnections()
			}
		}
	}
}

func (a *App) refreshConnections() {
	if a.eng == nil {
		return
	}
	list, err := a.eng.Connections()
	procStats := a.eng.ProcessTrafficStats()
	a.updateWindow(func() {
		if a.vm != nil {
			if err == nil {
				a.vm.connections = list
			}
			a.vm.procTraffic = procStats
		}
	})
}

func (a *App) resetProcessTraffic() {
	if a.eng == nil {
		return
	}
	a.eng.ResetProcessTraffic()
	procStats := a.eng.ProcessTrafficStats()
	a.updateWindow(func() {
		if a.vm != nil {
			a.vm.procTraffic = procStats
			a.vm.showToast("已重置进程流量统计")
		}
	})
}

func (a *App) cleanup() {
	a.closeOnce.Do(func() {
		if a.stopCh != nil {
			close(a.stopCh)
		}
		if a.unsub != nil {
			a.unsub()
			a.unsub = nil
		}
		if a.tray != nil {
			a.tray.Destroy()
			a.tray = nil
		}
		if a.ctrl != nil {
			a.ctrl.Close()
			a.ctrl = nil
		}
		if a.eng != nil {
			a.eng.Shutdown()
		}
	})
}

func (a *App) isWindowVisible() bool {
	return a.win != nil && a.win.IsVisible()
}

func (a *App) isWindowPinned() bool {
	return a.pinWindow
}

// setWindowPinned 更新窗口置顶状态，开启后窗口在失焦时保持可见且始终悬浮于其他窗口之上。
func (a *App) setWindowPinned(pinned bool) {
	a.pinWindow = pinned
	if a.vm != nil {
		a.vm.windowPinned = pinned
	}
	if a.win != nil {
		a.win.SetAlwaysOnTop(true)
		a.win.Invalidate()
	}
}

func (a *App) toggleWindowPinned() bool {
	next := !a.pinWindow
	a.setWindowPinned(next)
	return next
}

func (a *App) showWindow() {
	a.ensureWindow()
	if a.win == nil {
		return
	}
	// 弹出主面板时立即向系统核对代理真实状态，防止外部清空后开关仍停留在旧状态；
	// 若距离上次订阅同步已超过 5 分钟，同时在后台静默刷新套餐流量、到期时间、节点与云端规则。
	if a.eng != nil {
		go a.eng.RefreshSystemProxy()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			_, _ = a.eng.SyncSubscriptionIfStale(ctx, engine.DefaultAutoSyncInterval)
		}()
	}
	a.win.Restore()
	a.win.Show()
	a.win.Focus()
	mygo.App.Focus()
	a.win.Invalidate()
	a.refreshTrayMenu()
}

func (a *App) hideWindow() {
	if a.win == nil {
		return
	}
	if a.vm != nil {
		a.vm.connSortMenuOpen = false
		a.vm.closeActiveRulesModal()
	}
	a.win.Hide()
	a.refreshTrayMenu()
}

// shouldHideOnBlur 判断主窗口失焦时是否应自动收起（所有主标签页均一致自动收起，仅置顶窗口、更新下载中或模态弹窗打开时保持可见）。
func shouldHideOnBlur(quitting, pinWindow, visible bool, vm *viewModel) bool {
	if quitting || pinWindow || !visible {
		return false
	}
	if vm != nil && (vm.updateDownloading || vm.subModalOpen || vm.subSaving || vm.activeRulesModal.open) {
		return false
	}
	return true
}

func (a *App) toggleWindow() {
	if a.isWindowVisible() {
		a.hideWindow()
	} else {
		a.showWindow()
	}
}

// toggleWindowFromTray 在点击菜单栏 / 系统托盘图标时切换主窗口显隐，
// 弹出前自动将窗口定位到图标附近（macOS 菜单栏正下方 / Windows 任务栏托盘正上方）。
func (a *App) toggleWindowFromTray() {
	if a.isWindowVisible() {
		a.hideWindow()
		return
	}
	// 点击菜单栏图标会先触发 OnBlur 收起面板，250ms 内忽略紧随其后的 OnClick 避免闪现重开
	if time.Since(a.lastBlurHide) < 250*time.Millisecond {
		return
	}
	a.ensureWindow()
	a.positionWindowNearTray()
	a.showWindow()
}

func (a *App) positionWindowNearTray() {
	if a.win == nil || a.tray == nil || a.tray.tray == nil {
		return
	}
	winBounds := a.win.Bounds()
	winW, winH := winBounds.Width, winBounds.Height
	if winW <= 0 {
		winW = 500
	}
	if winH <= 0 {
		winH = 780
	}

	trayRect := a.tray.tray.Bounds()
	var display mygo.Display
	if trayRect.Width > 0 || trayRect.Height > 0 {
		center := mygo.Point{
			X: trayRect.X + trayRect.Width/2,
			Y: trayRect.Y + trayRect.Height/2,
		}
		display = mygo.Screen.DisplayNearestPoint(center)
	} else {
		display = mygo.Screen.PrimaryDisplay()
	}

	workArea := display.WorkArea
	if workArea.Width <= 0 || workArea.Height <= 0 {
		workArea = display.Bounds
	}
	if x, y, ok := calcTrayPopoverPos(trayRect, workArea, winW, winH, runtime.GOOS); ok {
		a.win.SetPosition(x, y)
	}
}

// calcPreferredPopoverHeight 根据主屏工作区高度自适应弹窗高度：
// 大屏最高取 900px 让单屏展示更多节点，小屏则留足安全余量避免顶住 Dock / 任务栏。
func calcPreferredPopoverHeight() int {
	disp := mygo.Screen.PrimaryDisplay()
	workH := disp.WorkArea.Height
	if workH <= 0 {
		workH = disp.Bounds.Height
	}
	if workH <= 0 {
		return 880
	}
	target := int(float64(workH) * 0.86)
	if target > 900 {
		target = 900
	}
	if target < 680 {
		target = max(600, workH-36)
	}
	return target
}

// calcTrayPopoverPos 计算窗口紧贴菜单栏 / 系统托盘图标的弹出坐标，并夹紧在屏幕工作区内。
func calcTrayPopoverPos(trayRect, workArea mygo.Rectangle, winW, winH int, goos string) (x, y int, ok bool) {
	if workArea.Width <= 0 || workArea.Height <= 0 || winW <= 0 || winH <= 0 {
		return 0, 0, false
	}
	const margin = 8

	hasTray := trayRect.Width > 0 && trayRect.Height > 0
	if !hasTray {
		// Windows 托盘图标被折叠在溢出区或取不到坐标时，贴工作区右下角（macOS 贴右上角）显示
		x = workArea.X + workArea.Width - winW - margin
		if goos == "darwin" {
			y = workArea.Y + margin
		} else {
			y = workArea.Y + workArea.Height - winH - margin
		}
	} else {
		x = trayRect.X + (trayRect.Width-winW)/2
		if goos == "darwin" {
			// macOS：紧贴菜单栏图标下方
			y = max(trayRect.Y+trayRect.Height+6, workArea.Y+4)
		} else {
			// Windows：判断托盘位于工作区底部还是顶部
			if trayRect.Y >= workArea.Y+workArea.Height/2 {
				y = min(trayRect.Y-winH-margin, workArea.Y+workArea.Height-winH-margin)
			} else {
				y = max(trayRect.Y+trayRect.Height+margin, workArea.Y+margin)
			}
		}
	}

	minX := workArea.X + margin
	maxX := workArea.X + workArea.Width - winW - margin
	if maxX < minX {
		x = workArea.X
	} else {
		x = max(minX, min(x, maxX))
	}

	minY := workArea.Y + 4
	maxY := workArea.Y + workArea.Height - winH - margin
	if maxY < minY {
		y = workArea.Y
	} else {
		y = max(minY, min(y, maxY))
	}
	return x, y, true
}

func (a *App) openSettingsTab() {
	a.ensureWindow()
	if a.vm != nil {
		a.vm.activeTab = 3
	}
	a.positionWindowNearTray()
	a.showWindow()
}

func (a *App) refreshTrayMenu() {
	if a.tray != nil && a.eng != nil {
		a.tray.Update(a.eng.Snapshot())
	}
}

func (a *App) updateWindow(fn func()) {
	if a.win == nil {
		return
	}
	a.win.Update(fn)
}

func (a *App) setThemeMode(mode string) {
	a.prefs.ThemeMode = mode
	applyThemeSource(mode)
	if updated, err := config.UpdateAppPrefs(a.prefsPath, func(p *config.AppPrefs) {
		p.ThemeMode = mode
	}); err == nil {
		a.prefs = updated
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *App) setShowTraySpeed(show bool) {
	a.prefs.ShowTraySpeed = show
	if updated, err := config.UpdateAppPrefs(a.prefsPath, func(p *config.AppPrefs) {
		p.ShowTraySpeed = show
	}); err == nil {
		a.prefs = updated
	}
	a.refreshTrayMenu()
}

// ensureCLIOnStartup 在应用启动后于后台检查并自动将 fsbar 链接至系统 PATH 目录。
func (a *App) ensureCLIOnStartup(skipAutoWrite bool) {
	var st cliinstall.Status
	if skipAutoWrite {
		st = cliinstall.Detect()
	} else {
		var err error
		st, err = cliinstall.EnsureAuto()
		if err != nil {
			log.Printf("自动链接命令行工具 fsbar 失败: %v", err)
			st = cliinstall.Detect()
		} else if st.Changed && st.LinkPath != "" {
			log.Printf("已自动链接命令行工具 fsbar -> %s", st.LinkPath)
		}
	}
	mygo.RunOnMain(func() {
		if a.vm != nil {
			a.vm.cliStatus = st
			a.vm.cliStatusLoaded = true
		}
		if a.win != nil && a.win.IsVisible() {
			a.win.Invalidate()
		}
	})
}

// installCLILink 由设置页主动触发，立即创建或修复 fsbar 命令行 PATH 链接。
func (a *App) installCLILink() {
	go func() {
		st, err := cliinstall.Ensure(cliinstall.Options{})
		a.updateWindow(func() {
			if a.vm == nil {
				return
			}
			a.vm.cliInstalling = false
			if err != nil {
				a.vm.showToast("链接 fsbar 失败: " + err.Error())
				a.vm.cliStatus = cliinstall.Detect()
				a.vm.cliStatusLoaded = true
				return
			}
			a.vm.cliStatus = st
			a.vm.cliStatusLoaded = true
			if st.LinkPath != "" {
				a.vm.showToast("已链接 fsbar 至 " + st.LinkPath)
			} else {
				a.vm.showToast("命令行工具 fsbar 已就绪")
			}
		})
	}()
}

func (a *App) logFilePath() string {
	if p := applog.CurrentPath(); p != "" {
		return p
	}
	p, _ := config.DefaultLogPath()
	return p
}

func (a *App) openLogDirectory() {
	p := a.logFilePath()
	if p == "" {
		return
	}
	go func() {
		if err := revealPathInFileManager(p); err != nil {
			log.Printf("[app] 打开日志目录失败 (%s): %v", p, err)
			a.updateWindow(func() {
				if a.vm != nil {
					a.vm.showToast("打开日志目录失败: " + err.Error())
				}
			})
		}
	}()
}

func applyThemeSource(mode string) {
	switch mode {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}
