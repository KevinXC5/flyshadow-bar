package ui

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

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
	NoConnect           bool
	DisableAutoSysProxy bool
}

// App 串联 Engine、本地控制服务、系统托盘、增量更新与 MyGo Native UI 主窗口。
type App struct {
	eng     *engine.Engine
	ctrl    *control.Server
	tray    *trayController
	win     *mygo.Window
	vm      *viewModel
	updates *UpdateManager
	prefs   config.AppPrefs

	updateProgressReset func()

	quitting  bool
	unsub     func()
	stopCh    chan struct{}
	closeOnce sync.Once
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
		return nil
	}

	// macOS 作为轻量菜单栏应用，默认隐藏 Dock 图标
	if runtime.GOOS == "darwin" {
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	}

	prefs := config.LoadAppPrefs("")
	applyThemeSource(prefs.ThemeMode)

	eng, err := engine.New(engine.Options{
		ConfigPath:          opts.ConfigPath,
		CoreLibPath:         opts.CoreLibPath,
		DisableAutoSysProxy: opts.DisableAutoSysProxy,
	})
	if err != nil {
		return fmt.Errorf("初始化 FlyShadow 引擎失败: %w", err)
	}

	app := &App{
		eng:    eng,
		prefs:  prefs,
		stopCh: make(chan struct{}),
	}
	app.updates = newUpdateManager(func() {
		mygo.RunOnMain(func() {
			app.quitting = true
			app.cleanup()
			mygo.App.Relaunch()
		})
	})

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
		eng.Shutdown()
		return fmt.Errorf("启动本地控制服务失败: %w", err)
	}
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

	hidden := opts.StartHidden || mygo.App.WasOpenedAtLogin()
	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title:         "FlyShadow Bar",
		Width:         500,
		Height:        780,
		MinWidth:      440,
		MinHeight:     580,
		StateKey:      "main-v2",
		Hidden:        hidden,
		TitleBarStyle: mygo.TitleBarHidden,
		Content:       ui.View(a.vm.render),
	})

	// 点击窗口关闭按钮时隐藏至托盘而非退出，仅在真正退出应用时放行关闭
	a.win.OnClose(func(e *mygo.CloseEvent) {
		if !a.quitting {
			e.PreventDefault()
			a.win.Hide()
			a.refreshTrayMenu()
		}
	})
	a.win.OnShow(func() { a.refreshTrayMenu() })
	a.win.OnHide(func() { a.refreshTrayMenu() })

	var err error
	a.tray, err = newTrayController(a)
	if err != nil {
		log.Printf("创建系统托盘失败: %v", err)
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

	// 绑定 MyGo 签名增量更新的后台通知与进度节流回调
	if a.updates != nil {
		a.updates.availableFn = func(st UpdateStatus) {
			a.updateWindow(func() {
				if a.vm != nil {
					a.vm.applyNativeUpdateStatus(st)
				}
			})
			mygo.RunOnMain(func() { a.refreshTrayMenu() })
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
	if err != nil {
		return
	}
	a.updateWindow(func() {
		if a.vm != nil {
			a.vm.connections = list
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

func (a *App) showWindow() {
	if a.win == nil {
		return
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
	a.win.Hide()
	a.refreshTrayMenu()
}

func (a *App) toggleWindow() {
	if a.isWindowVisible() {
		a.hideWindow()
	} else {
		a.showWindow()
	}
}

func (a *App) openSettingsTab() {
	if a.vm != nil {
		a.vm.activeTab = 3
	}
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
	_ = config.SaveAppPrefs("", a.prefs)
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *App) setShowTraySpeed(show bool) {
	a.prefs.ShowTraySpeed = show
	_ = config.SaveAppPrefs("", a.prefs)
	a.refreshTrayMenu()
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
