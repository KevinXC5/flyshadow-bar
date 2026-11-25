package ui

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/KevinXC5/flyshadow-bar/internal/cliinstall"
	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
	"github.com/KevinXC5/flyshadow-bar/internal/updater"
	"github.com/KevinXC5/flyshadow-bar/internal/version"
)

// viewModel 持有主窗口 UI 状态，所有读写均发生在主线程（或 ui.Tester 测试线程）。
type viewModel struct {
	app *App

	snap           engine.Snapshot
	activeTab      int // 0=节点, 1=规则, 2=连接, 3=设置
	searchQuery    string
	onlyStarred    bool
	sortByDelay    bool
	portDraft      string
	portDirty      bool
	openAtLogin    bool
	openAtLoginSet bool

	// 自定义规则页状态
	ruleDomainDraft  string
	ruleLabelDraft   string
	ruleMatchDraft   int
	ruleActionDraft  int
	editingRuleID    int
	ruleSearchQuery  string
	ruleFilterAction int // -1=全部, 0=直连, 1=拒绝, 2=代理

	// 实时连接页状态
	connections      []core.ConnectionInfo
	connSearchQuery  string
	connsPaused      bool
	connSortField    connSortField
	connSortAsc      bool
	connSortMenuOpen bool

	// 订阅密码配置弹窗状态
	subModalOpen     bool
	subPasswordDraft string
	subPasswordShow  bool
	subSaving        bool
	subModalErr      string

	// 生效分流规则查看弹窗状态
	activeRulesModal activeRulesModalState

	checkingUpdate      bool
	updateNativeEnabled bool
	updateAvailable     string
	updateNotes         string
	updateInstalled     bool
	updateDownloading   bool
	updateDownloaded    int64
	updateTotal         int64
	updateCancel        context.CancelFunc
	updateResult        *updater.CheckResult
	updateErr           string
	updateMarkdownOpts  markdown.Options
	cliStatus           cliinstall.Status
	cliStatusLoaded     bool
	cliInstalling       bool
	toastMsg            string
}

func newViewModel(app *App, initial engine.Snapshot) *viewModel {
	vm := &viewModel{
		app:              app,
		snap:             initial,
		portDraft:        strconv.Itoa(initial.ProxyPort),
		ruleMatchDraft:   config.MatchDomainSuffix,
		ruleActionDraft:  2,
		ruleFilterAction: -1,
	}
	if app != nil && app.updates != nil {
		vm.applyNativeUpdateStatus(app.updates.Status())
	}
	return vm
}

func (vm *viewModel) applyNativeUpdateStatus(st UpdateStatus) {
	vm.updateNativeEnabled = st.Enabled
	vm.updateAvailable = st.Available
	vm.updateNotes = st.Notes
	vm.updateInstalled = st.Installed
}

// palette 定义浅色 / 深色下的克制配色层次，主视觉为冷静的靛青蓝点缀。
type palette struct {
	canvas        ui.Color
	card          ui.Color
	cardElevated  ui.Color
	cardHover     ui.Color
	cardSelected  ui.Color
	border        ui.Color
	borderStrong  ui.Color
	text          ui.Color
	textSecondary ui.Color
	textMuted     ui.Color
	accent        ui.Color
	accentSoft    ui.Color
	success       ui.Color
	successSoft   ui.Color
	warning       ui.Color
	warningSoft   ui.Color
	danger        ui.Color
	dangerSoft    ui.Color
	pillBg        ui.Color
	starGold      ui.Color
}

func paletteFor(dark bool) palette {
	if dark {
		return palette{
			canvas:        ui.Hex("#111318"),
			card:          ui.Hex("#181c23"),
			cardElevated:  ui.Hex("#1f242e"),
			cardHover:     ui.Hex("#222834"),
			cardSelected:  ui.RGBA(56, 139, 253, 0.14),
			border:        ui.RGBA(255, 255, 255, 0.08),
			borderStrong:  ui.RGBA(96, 165, 250, 0.42),
			text:          ui.Hex("#f1f5f9"),
			textSecondary: ui.Hex("#cbd5e1"),
			textMuted:     ui.Hex("#8b97a8"),
			accent:        ui.Hex("#4f8cff"),
			accentSoft:    ui.RGBA(79, 140, 255, 0.18),
			success:       ui.Hex("#34d399"),
			successSoft:   ui.RGBA(52, 211, 153, 0.16),
			warning:       ui.Hex("#fbbf24"),
			warningSoft:   ui.RGBA(251, 191, 36, 0.16),
			danger:        ui.Hex("#f87171"),
			dangerSoft:    ui.RGBA(248, 113, 113, 0.16),
			pillBg:        ui.RGBA(255, 255, 255, 0.06),
			starGold:      ui.Hex("#fbbf24"),
		}
	}
	return palette{
		canvas:        ui.Hex("#f5f7fb"),
		card:          ui.Hex("#ffffff"),
		cardElevated:  ui.Hex("#f8fafc"),
		cardHover:     ui.Hex("#f1f5f9"),
		cardSelected:  ui.RGBA(37, 99, 235, 0.08),
		border:        ui.RGBA(15, 23, 42, 0.08),
		borderStrong:  ui.RGBA(37, 99, 235, 0.35),
		text:          ui.Hex("#0f172a"),
		textSecondary: ui.Hex("#334155"),
		textMuted:     ui.Hex("#64748b"),
		accent:        ui.Hex("#2563eb"),
		accentSoft:    ui.RGBA(37, 99, 235, 0.11),
		success:       ui.Hex("#059669"),
		successSoft:   ui.RGBA(5, 150, 105, 0.12),
		warning:       ui.Hex("#d97706"),
		warningSoft:   ui.RGBA(217, 119, 6, 0.12),
		danger:        ui.Hex("#dc2626"),
		dangerSoft:    ui.RGBA(220, 38, 38, 0.11),
		pillBg:        ui.RGBA(15, 23, 42, 0.05),
		starGold:      ui.Hex("#f59e0b"),
	}
}

func (vm *viewModel) render(c *ui.Context) {
	if vm.app != nil {
		if c.Shortcut(ui.Cmd, ui.KeyW) {
			vm.app.hideWindow()
		} else if c.Shortcut(0, ui.KeyEscape) {
			if vm.subModalOpen {
				if !vm.subSaving {
					vm.closeSubscribeModal()
				}
			} else if vm.activeRulesModal.open {
				vm.closeActiveRulesModal()
			} else if vm.connSortMenuOpen {
				vm.connSortMenuOpen = false
			} else {
				vm.app.hideWindow()
			}
		}
		c.OnShortcut(ui.Cmd, ui.KeyQ, mygo.App.Quit)
	}

	if vm.toastMsg != "" {
		c.Toast(vm.toastMsg)
		vm.toastMsg = ""
	}

	base := c.Theme()
	p := paletteFor(base.Dark)
	themed := *base.WithAccent(p.accent)
	themed.Background = p.canvas
	themed.Radius = 9
	themed.Spacing = 3.2
	// 全局采用精致细滚动条，配合列表右侧外置槽道，绝不遮挡高亮卡片边框
	thumbBase := ui.RGBA(15, 23, 42, 0.22)
	thumbHover := ui.RGBA(15, 23, 42, 0.42)
	if base.Dark {
		thumbBase = ui.RGBA(255, 255, 255, 0.18)
		thumbHover = ui.RGBA(255, 255, 255, 0.36)
	}
	themed.ScrollbarStyle = ui.ScrollbarStyle{
		TrackWidth: 8,
		ThumbWidth: 3.5,
		MinLength:  24,
		Thumb:      thumbBase,
		Hover:      thumbHover,
		Active:     thumbHover,
	}
	c.SetTheme(&themed)

	ui.Column(c).Fill().Background(p.canvas).Children(func() {
		vm.renderTitleBar(c, p)
		ui.Column(c).Grow(1).Padding(4, 14, 12, 14).Gap(9).Children(func() {
			vm.renderUpdateTopBanner(c, p)
			vm.renderCompactOverview(c, p)
			switch vm.activeTab {
			case 0:
				vm.renderNodesView(c, p)
			case 1:
				vm.renderRulesView(c, p)
			case 2:
				vm.renderConnectionsView(c, p)
			default:
				vm.renderSettingsView(c, p)
			}
		})
	})

	vm.renderSubscribeModal(c, p)
	vm.renderActiveRulesModal(c, p)
}

func (vm *viewModel) renderTitleBar(c *ui.Context, p palette) {
	bar := c.TitleBar()
	h := max(bar.Height, 40)
	// 与下方主内容区左右 14px 内边距保持严格像素对齐
	leftPad := max(bar.Left+12, 14)
	rightPad := max(bar.Right+12, 14)

	ui.Row(c).Height(h).Padding(0, rightPad, 0, leftPad).AlignItems(ui.Center).Gap(8).
		DragWindow().Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(7).Children(func() {
			if logo := appLogoBitmap(); logo != nil {
				ui.Image(c, logo).Size(20, 20).Fit(ui.Contain)
			}
			ui.Text(c, "FlyShadow Bar").FontSize(13).Bold().TextColor(p.text)
		})
		ui.Spacer(c)
		prevTab := vm.activeTab
		if ui.Segmented(c.Key("nav-tabs"), &vm.activeTab, "节点", "规则", "连接", "设置").Changed() {
			if vm.activeTab != 2 {
				vm.connSortMenuOpen = false
			}
			if vm.activeTab == 2 && prevTab != 2 && vm.app != nil {
				go vm.app.refreshConnections()
			}
		}
	})
}

// renderCompactOverview 紧凑高密度顶部概览区：当前节点 + 实时速率折线图 + 订阅流量卡 + 模式与快捷开关。
func (vm *viewModel) renderCompactOverview(c *ui.Context, p palette) {
	snap := vm.snap
	nodeName := "未选择节点"
	if snap.CurrentNode != nil {
		nodeName = snap.CurrentNode.DisplayName
	}

	ui.Column(c).Padding(11, 12).Gap(9).Radius(13).
		Background(p.card).Border(1, p.border).
		Shadow(0, 1, 6, 0, ui.RGBA(0, 0, 0, 0.04)).Children(func() {

		// 第一行：状态胶囊 + 当前节点名 + 协议标签 + 电源主开关
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Row(c).Grow(1).AlignItems(ui.Center).Gap(6).Children(func() {
				renderPill(c, StateLabel(snap.State), stateDotColor(snap.State, p), stateBgColor(snap.State, p))
				ui.Text(c, nodeName).Grow(1).FontSize(14).Bold().TextColor(p.text).Ellipsis("…")
				if snap.EffectiveTransport.EnableQuic {
					renderMiniTag(c, "QUIC", p.accent, p.accentSoft)
				}
				if snap.EffectiveTransport.EnableNativeUDP {
					renderMiniTag(c, "UDP", p.success, p.successSoft)
				}
			})

			if snap.State == engine.StateConnected || snap.State == engine.StateConnecting {
				pwrBtn := ui.Box(c.Key("btn-disconnect")).Size(32, 32).Radius(9.5).Center().
					Background(p.dangerSoft).Border(1, p.danger).Cursor(ui.CursorPointer).
					Label("断开").Tooltip("断开")
				if pwrBtn.Hovered() {
					pwrBtn.Background(p.danger)
					pwrBtn.Children(func() {
						ui.Icon(c, svgPower).Size(15, 15).TextColor(ui.Hex("#ffffff"))
					})
				} else {
					pwrBtn.Children(func() {
						ui.Icon(c, svgPower).Size(15, 15).TextColor(p.danger)
					})
				}
				if pwrBtn.Clicked() {
					vm.runAsync(func() error { return vm.app.eng.Disconnect() }, "已断开")
				}
			} else {
				pwrBtn := ui.Box(c.Key("btn-connect")).Size(32, 32).Radius(9.5).Center().
					Background(p.accent).Cursor(ui.CursorPointer).
					Label("连接").Tooltip("连接")
				if pwrBtn.Hovered() {
					pwrBtn.Opacity(0.9)
				}
				pwrBtn.Children(func() {
					ui.Icon(c, svgPower).Size(15, 15).TextColor(ui.Hex("#ffffff"))
				})
				if pwrBtn.Clicked() {
					vm.runAsync(func() error { return vm.app.eng.Connect() }, "")
				}
			}
		})

		if snap.State == engine.StateError && snap.LastError != "" {
			ui.Row(c).Padding(5, 8).Radius(7).Background(p.dangerSoft).Children(func() {
				ui.Text(c, snap.LastError).FontSize(11).TextColor(p.danger).Wrap()
			})
		}

		// 第二行：实时速率折线图（左宽）与订阅流量卡（右窄）并排等高展示
		ui.Row(c).Gap(8).AlignItems(ui.Stretch).Children(func() {
			vm.renderSpeedChart(c, p, snap)
			vm.renderSubscriptionCard(c, p, snap)
		})

		// 第三行：代理模式三等分切换栏
		vm.renderModeBar(c, p, snap.ModeCode)

		// 第四行：三个紧凑快捷开关（系统代理 / UDP 加速 / 原生 UDP）
		ui.Row(c).Gap(6).AlignItems(ui.Stretch).Children(func() {
			sysOn := snap.SystemProxy
			vm.renderQuickToggle(c, p, "qt-sysproxy", "系统代理", &sysOn, func(v bool) {
				vm.runAsync(func() error { return vm.app.eng.SetSystemProxy(v) }, toggleToast("系统代理", v))
			})
			udpOn := snap.UDPAccelerationEnabled
			vm.renderQuickToggle(c, p, "qt-udp", "UDP 加速", &udpOn, func(v bool) {
				vm.runAsync(func() error { return vm.app.eng.SetUDPAcceleration(v) }, toggleToast("UDP 加速 (QUIC)", v))
			})
			nativeOn := snap.NativeUDP
			vm.renderQuickToggle(c, p, "qt-native", "原生 UDP", &nativeOn, func(v bool) {
				vm.runAsync(func() error { return vm.app.eng.SetNativeUDP(v) }, toggleToast("原生 UDP", v))
			})
		})
	})
}

// renderSpeedChart 绘制实时上下行速率折线面积图，顶部紧凑显示实时速率与延迟。
func (vm *viewModel) renderSpeedChart(c *ui.Context, p palette, snap engine.Snapshot) {
	pingVal := "--"
	if snap.State == engine.StateConnected {
		if snap.TunnelPingMs > 0 {
			pingVal = fmt.Sprintf("%d ms", snap.TunnelPingMs)
		} else if snap.CurrentNode != nil && snap.CurrentNode.DelayMs > 0 {
			pingVal = fmt.Sprintf("%d ms", snap.CurrentNode.DelayMs)
		}
	}

	samples := snap.SpeedHistory
	if len(samples) < 2 {
		samples = make([]engine.SpeedSample, 24)
		samples[len(samples)-1] = engine.SpeedSample{
			UploadBps:   snap.UploadSpeedBps,
			DownloadBps: snap.DownloadSpeedBps,
		}
	}

	// 计算纵轴自适应上限（保底 64 KB/s，避免极低背景流量把曲线拉满抖动）
	peak := float64(64 * 1024)
	for _, s := range samples {
		if float64(s.DownloadBps) > peak {
			peak = float64(s.DownloadBps)
		}
		if float64(s.UploadBps) > peak {
			peak = float64(s.UploadBps)
		}
	}
	peak *= 1.18

	n := len(samples)
	xs := make([]float32, n)
	ysDown := make([]float32, n)
	ysUp := make([]float32, n)
	for i, s := range samples {
		xs[i] = float32(i) / float32(n-1)
		ysDown[i] = float32(min(1.0, max(0.0, float64(s.DownloadBps)/peak)))
		ysUp[i] = float32(min(1.0, max(0.0, float64(s.UploadBps)/peak)))
	}

	ui.Column(c).Grow(1).MinWidth(0).Padding(8, 10, 7, 10).Gap(5).Radius(9.5).
		Background(p.cardElevated).Border(1, p.border).Children(func() {
		// 顶部实时读数行
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
				ui.Box(c).Size(6, 6).Radius(99).Background(p.accent)
				ui.Text(c, "↓ "+FormatSpeed(snap.DownloadSpeedBps)).FontSize(11).Bold().TextColor(p.textSecondary).NoWrap()
			})
			ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
				ui.Box(c).Size(6, 6).Radius(99).Background(p.success)
				ui.Text(c, "↑ "+FormatSpeed(snap.UploadSpeedBps)).FontSize(11).TextColor(p.textSecondary).NoWrap()
			})
			ui.Spacer(c)
			ui.Text(c, pingVal).FontSize(10.5).TextColor(p.textMuted).NoWrap().Tooltip("隧道延迟")
		})

		// 折线面积图画布（随右侧卡片等高舒展）
		ui.Box(c).Grow(1).MinHeight(46).Draw(func(pt *ui.Painter, r ui.Rect) {
			if r.W <= 2 || r.H <= 2 {
				return
			}
			// 留出上下 2px 呼吸边距，防止顶部峰值描边被裁切
			plot := ui.Rect{X: r.X, Y: r.Y + 2, W: r.W, H: r.H - 4}
			gridColor := p.border
			pt.Line(plot.X, plot.Y+plot.H*0.5, plot.X+plot.W, plot.Y+plot.H*0.5, 0.75, gridColor)
			pt.Line(plot.X, plot.Y+plot.H, plot.X+plot.W, plot.Y+plot.H, 0.75, gridColor)

			// 下行面积 + 主折线（靛青蓝）
			if areaDown := chart.Area(plot, xs, nil, ysDown, chart.CurveMonotone); areaDown != nil {
				pt.FillPathGradient(areaDown, ui.LinearGradient{
					From:  p.accent.Alpha(0.24),
					To:    p.accent.Alpha(0.01),
					Angle: 180,
				})
			}
			if lineDown := chart.Line(plot, xs, ysDown, chart.CurveMonotone); lineDown != nil {
				pt.StrokePath(lineDown, 1.6, p.accent)
			}

			// 上行面积 + 细折线（翡翠绿）
			if areaUp := chart.Area(plot, xs, nil, ysUp, chart.CurveMonotone); areaUp != nil {
				pt.FillPathGradient(areaUp, ui.LinearGradient{
					From:  p.success.Alpha(0.14),
					To:    p.success.Alpha(0.0),
					Angle: 180,
				})
			}
			if lineUp := chart.Line(plot, xs, ysUp, chart.CurveMonotone); lineUp != nil {
				pt.StrokePath(lineUp, 1.25, p.success)
			}
		})
	})
}

// renderSubscriptionCard 右侧紧凑订阅流量卡，与左侧实时速率图并排等高。
func (vm *viewModel) renderSubscriptionCard(c *ui.Context, p palette, snap engine.Snapshot) {
	ui.Column(c).Width(176).Padding(8, 10, 7, 10).Gap(5).Radius(9.5).
		Background(p.cardElevated).Border(1, p.border).Children(func() {
		// 第一行：VIP 徽标 / 标题 + 订阅密码设置与同步图标按钮
		ui.Row(c).AlignItems(ui.Center).Gap(3).Children(func() {
			if snap.Traffic != nil && snap.Traffic.Level > 0 {
				renderMiniTag(c, fmt.Sprintf("VIP %d", snap.Traffic.Level), p.accent, p.accentSoft)
			} else {
				ui.Text(c, "订阅").FontSize(10.5).Bold().TextColor(p.textMuted).NoWrap()
			}
			ui.Spacer(c)

			// 钥匙图标：弹窗填写或更换订阅密码
			keyTip := "配置订阅密码"
			if snap.HasCloudAuth {
				keyTip = "更换订阅密码"
			}
			keyBtn := ui.Box(c.Key("btn-sub-config")).Size(20, 20).Radius(5).Center().
				Cursor(ui.CursorPointer).Label("配置订阅密码").Tooltip(keyTip)
			if keyBtn.Hovered() {
				keyBtn.Background(p.pillBg)
			}
			keyBtn.Children(func() {
				ui.Icon(c, svgKey).Size(12, 12).TextColor(p.textSecondary)
			})
			if keyBtn.Clicked() {
				vm.openSubscribeModal()
			}

			if snap.HasCloudAuth {
				syncBtn := ui.Box(c.Key("btn-sync-sub")).Size(20, 20).Radius(5).Center().
					Label("同步订阅").Tooltip("同步订阅与规则")
				if snap.Syncing {
					syncBtn.Background(p.accentSoft)
				} else {
					syncBtn.Cursor(ui.CursorPointer)
					if syncBtn.Hovered() {
						syncBtn.Background(p.pillBg)
					}
				}
				syncBtn.Children(func() {
					if snap.Syncing {
						ui.Spinner(c).Size(10.5, 10.5)
					} else {
						ui.Icon(c, svgRefresh).Size(12, 12).TextColor(p.textSecondary)
					}
				})
				if !snap.Syncing && syncBtn.Clicked() {
					vm.runAsync(func() error {
						ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
						defer cancel()
						_, err := vm.app.eng.SyncSubscription(ctx)
						return err
					}, "订阅已同步")
				}
			}
		})

		if snap.Traffic != nil && snap.Traffic.TrafficSize > 0 {
			used := snap.Traffic.TrafficUpload + snap.Traffic.TrafficDownload
			ratio := min(1.0, max(0.0, float64(used)/float64(snap.Traffic.TrafficSize)))
			usageColor := p.text
			barColor := p.accent
			if ratio >= 0.90 {
				usageColor = p.danger
				barColor = p.danger
			} else if ratio >= 0.75 {
				usageColor = p.warning
				barColor = p.warning
			}

			// 第二行：已用 / 总流量
			ui.Row(c).AlignItems(ui.Baseline).Gap(3).Children(func() {
				ui.Text(c, FormatBytes(used)).FontSize(12).Bold().TextColor(usageColor).NoWrap()
				ui.Text(c, "/ "+FormatBytes(snap.Traffic.TrafficSize)).FontSize(10).TextColor(p.textMuted).Ellipsis("…")
			})

			// 第三行：圆角进度条
			ui.Box(c).Height(4).Radius(99).Background(p.pillBg).Draw(func(pt *ui.Painter, r ui.Rect) {
				if r.W <= 0 || ratio <= 0 {
					return
				}
				fillW := max(float32(4), r.W*float32(ratio))
				if fillW > r.W {
					fillW = r.W
				}
				pt.Fill(ui.Rect{X: r.X, Y: r.Y, W: fillW, H: r.H}, barColor, 99)
			})

			// 第四行：到期时间
			if snap.Traffic.ExpireTime > 0 {
				ui.Text(c, "到期 "+FormatExpireDate(int64(snap.Traffic.ExpireTime))).
					FontSize(10).TextColor(p.textMuted).NoWrap()
			}
		} else if snap.HasCloudAuth {
			ui.Spacer(c)
			ui.Text(c, "点击右上角同步").FontSize(10.5).TextColor(p.textMuted).NoWrap()
		} else {
			ui.Spacer(c)
			setupBtn := ui.Row(c.Key("btn-sub-setup-inline")).Padding(4, 8).Radius(6).
				Background(p.accentSoft).AlignItems(ui.Center).Center().Cursor(ui.CursorPointer).
				Label("填写订阅密码").Children(func() {
				ui.Text(c, "填写订阅密码").FontSize(10.5).Bold().TextColor(p.accent).NoWrap()
			})
			if setupBtn.Clicked() {
				vm.openSubscribeModal()
			}
		}
	})
}

func (vm *viewModel) openSubscribeModal() {
	vm.subModalOpen = true
	if vm.app != nil && vm.app.eng != nil {
		vm.subPasswordDraft = vm.app.eng.SubscribePassword()
	} else {
		vm.subPasswordDraft = ""
	}
	vm.subPasswordShow = false
	vm.subModalErr = ""
}

func (vm *viewModel) closeSubscribeModal() {
	vm.subModalOpen = false
	vm.subPasswordDraft = ""
	vm.subPasswordShow = false
	vm.subModalErr = ""
}

// renderSubscribeModal 绘制配置/更换订阅密码的模态弹窗。
func (vm *viewModel) renderSubscribeModal(c *ui.Context, p palette) {
	if !vm.subModalOpen {
		return
	}
	winW, winH := c.Size()
	panelW := min(float32(368), max(float32(300), winW-36))
	shadowAlpha := float32(0.22)
	backdropAlpha := float32(0.40)
	if c.Theme().Dark {
		shadowAlpha = 0.45
		backdropAlpha = 0.54
	}
	modal := ui.DialogBase(c.Key("sub-password-modal"), &vm.subModalOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, backdropAlpha))
		panel.Width(panelW).MaxHeight(max(float32(260), winH-48)).Padding(16).Gap(12).Radius(13).
			Background(p.card).Border(1, p.borderStrong).
			Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, shadowAlpha))

		// 标题栏
		ui.Row(c).AlignItems(ui.Center).Gap(7).Children(func() {
			ui.Box(c).Size(24, 24).Radius(7).Background(p.accentSoft).Center().Children(func() {
				ui.Icon(c, svgKey).Size(13, 13).TextColor(p.accent)
			})
			title := "配置订阅密码"
			if vm.snap.HasCloudAuth {
				title = "更换订阅密码"
			}
			ui.Text(c, title).FontSize(14).Bold().TextColor(p.text)
			ui.Spacer(c)
			closeBtn := ui.Box(c.Key("btn-sub-modal-close")).Size(24, 24).Radius(6).Center().
				Cursor(ui.CursorPointer).Label("关闭").Tooltip("关闭")
			if closeBtn.Hovered() {
				closeBtn.Background(p.pillBg)
			}
			closeBtn.Children(func() {
				ui.Icon(c, svgClose).Size(13, 13).TextColor(p.textMuted)
			})
			if !vm.subSaving && closeBtn.Clicked() {
				vm.closeSubscribeModal()
			}
		})

		ui.Text(c, "输入订阅密码后将自动校验，并同步节点、套餐流量与云端规则。").
			FontSize(11.5).TextColor(p.textMuted).Wrap()

		// 密码输入行（支持掩码切换与回车直接提交）
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			inp := ui.TextInput(c.Key("sub-pw-input"), &vm.subPasswordDraft).
				Grow(1).Placeholder("请输入订阅密码").AutoFocus()
			if !vm.subPasswordShow {
				inp.Password()
			}
			if inp.Submitted() && !vm.subSaving {
				vm.submitSubscribePassword()
			}

			eyeIcon := svgEye
			eyeTip := "显示密码"
			if vm.subPasswordShow {
				eyeIcon = svgEyeOff
				eyeTip = "隐藏密码"
			}
			eyeBtn := ui.Box(c.Key("btn-sub-pw-eye")).Size(30, 30).Radius(7).Center().
				Background(p.cardElevated).Border(1, p.border).Cursor(ui.CursorPointer).
				Label(eyeTip).Tooltip(eyeTip)
			if eyeBtn.Hovered() {
				eyeBtn.Background(p.cardHover)
			}
			eyeBtn.Children(func() {
				ui.Icon(c, eyeIcon).Size(14, 14).TextColor(p.textSecondary)
			})
			if eyeBtn.Clicked() {
				vm.subPasswordShow = !vm.subPasswordShow
			}
		})

		if vm.subModalErr != "" {
			ui.Row(c).Padding(6, 9).Radius(7).Background(p.dangerSoft).Children(func() {
				ui.Text(c, vm.subModalErr).FontSize(11).TextColor(p.danger).Wrap()
			})
		}

		// 底部操作按钮
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Spacer(c)
			if ui.Button(c.Key("btn-sub-cancel"), "取消").Padding(6, 14).Radius(7).
				Disabled(vm.subSaving).Clicked() {
				vm.closeSubscribeModal()
			}
			saveLabel := "保存并同步"
			if vm.subSaving {
				saveLabel = "校验中…"
			}
			if ui.PrimaryButton(c.Key("btn-sub-save"), saveLabel).Padding(6, 14).Radius(7).
				Disabled(vm.subSaving || strings.TrimSpace(vm.subPasswordDraft) == "").Clicked() {
				vm.submitSubscribePassword()
			}
		})
	})
	if modal.Dismissed() {
		if vm.subSaving {
			// 正在校验同步期间防止误触背景关闭弹窗
			vm.subModalOpen = true
		} else {
			vm.closeSubscribeModal()
		}
	}
}

func (vm *viewModel) submitSubscribePassword() {
	pw := strings.TrimSpace(vm.subPasswordDraft)
	if pw == "" {
		vm.subModalErr = "请输入订阅密码"
		return
	}
	if vm.app == nil || vm.app.eng == nil {
		return
	}
	vm.subSaving = true
	vm.subModalErr = ""

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		_, err := vm.app.eng.SetSubscribePassword(ctx, pw)
		vm.app.updateWindow(func() {
			vm.subSaving = false
			if err != nil {
				vm.subModalErr = err.Error()
				return
			}
			vm.subPasswordDraft = ""
			vm.subPasswordShow = false
			vm.subModalErr = ""
			vm.subModalOpen = false
			vm.toastMsg = "订阅已配置并同步"
		})
	}()
}

func (vm *viewModel) renderModeBar(c *ui.Context, p palette, active core.ProxyMode) {
	options := []struct {
		mode  core.ProxyMode
		label string
	}{
		{core.ProxyModeRule, "规则"},
		{core.ProxyModeGlobal, "全局"},
		{core.ProxyModeDirect, "直连"},
	}

	ui.Row(c.Key("mode-bar")).Padding(2.5).Gap(2.5).Radius(9).
		Background(p.cardElevated).Border(1, p.border).AlignItems(ui.Stretch).Children(func() {
		for _, opt := range options {
			selected := opt.mode == active
			item := ui.Row(c.Key(opt.mode.String())).Grow(1).Padding(4.5, 6).Radius(7).
				Center().Cursor(ui.CursorPointer)
			if selected {
				item.Background(p.card).Border(1, p.borderStrong).
					Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.06))
			} else if item.Hovered() {
				item.Background(p.cardHover)
			}
			item.Children(func() {
				txt := ui.Text(c, opt.label).FontSize(11.5).NoWrap()
				if selected {
					txt.Bold().TextColor(p.accent)
				} else {
					txt.TextColor(p.textSecondary)
				}
			})
			if item.Clicked() && !selected {
				nextMode := opt.mode
				vm.runAsync(func() error { return vm.app.eng.SetMode(nextMode) }, "已切换至"+nextMode.Label())
			}
		}
	})
}

func (vm *viewModel) renderQuickToggle(c *ui.Context, p palette, key, label string, val *bool, onToggle func(bool)) {
	bg := p.cardElevated
	bdr := p.border
	if *val {
		bg = p.accentSoft
		bdr = p.borderStrong
	}
	ui.Row(c.Key(key)).Grow(1).Padding(5, 8).Radius(8).Background(bg).Border(1, bdr).
		AlignItems(ui.Center).Gap(5).Children(func() {
		ui.Text(c, label).Grow(1).FontSize(11).TextColor(p.text).NoWrap()
		if ui.Switch(c.Key(key+"-sw"), val).Changed() {
			onToggle(*val)
		}
	})
}

func (vm *viewModel) renderNodesView(c *ui.Context, p palette) {
	snap := vm.snap

	// 搜索与图标工具栏
	ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
		ui.SearchField(c.Key("node-search"), &vm.searchQuery).Grow(1).Placeholder("搜索节点…")

		starTip := "只看收藏"
		starIcon := svgStarOutline
		if vm.onlyStarred {
			starTip = "显示全部"
			starIcon = svgStarFilled
		}
		if renderToolbarIconBtn(c, p, "filter-star", "收藏", starTip, starIcon, vm.onlyStarred, p.starGold, p.warningSoft, p.warning, false) {
			vm.onlyStarred = !vm.onlyStarred
		}

		sortTip := "按延迟排序"
		if vm.sortByDelay {
			sortTip = "默认排序"
		}
		if renderToolbarIconBtn(c, p, "sort-nodes", "按延迟排序", sortTip, svgSortAsc, vm.sortByDelay, p.accent, p.accentSoft, p.borderStrong, false) {
			vm.sortByDelay = !vm.sortByDelay
		}

		pingBtn := ui.Box(c.Key("btn-ping")).Size(30, 30).Radius(8).Center().
			Background(p.card).Border(1, p.border).Label("测速").Tooltip("测速")
		if snap.Pinging {
			pingBtn.Background(p.accentSoft).Border(1, p.borderStrong).Opacity(0.85)
		} else {
			pingBtn.Cursor(ui.CursorPointer)
			if pingBtn.Hovered() {
				pingBtn.Background(p.cardHover)
			}
		}
		pingBtn.Children(func() {
			if snap.Pinging {
				ui.Spinner(c).Size(13, 13)
			} else {
				ui.Icon(c, svgGauge).Size(14.5, 14.5).TextColor(p.textSecondary)
			}
		})
		if !snap.Pinging && pingBtn.Clicked() {
			vm.runAsync(func() error {
				_, err := vm.app.eng.PingAll(context.Background())
				return err
			}, "测速完成")
		}
	})

	filtered := vm.filteredNodes()

	// 节点统计摘要行（未筛选时只显示总数，避免重复）
	ui.Row(c).AlignItems(ui.Center).Padding(0, 2).Children(func() {
		summary := fmt.Sprintf("共 %d 个节点", len(snap.Nodes))
		if len(filtered) != len(snap.Nodes) {
			summary = fmt.Sprintf("显示 %d / %d 个节点", len(filtered), len(snap.Nodes))
		}
		ui.Text(c, summary).FontSize(10.5).TextColor(p.textMuted)
		ui.Spacer(c)
		if snap.Pinging {
			ui.Text(c, fmt.Sprintf("%d/%d", snap.PingCompleted, snap.PingTotal)).FontSize(10.5).TextColor(p.accent)
		}
	})

	// 紧凑高容量节点列表（右侧外扩 10px 独立滚动槽道，确保卡片右对齐且细滚动条绝不重叠）
	ui.Scroll(c.Key("node-scroll")).Grow(1).Margin(0, -10, 0, 0).Children(func() {
		ui.Column(c).Gap(4).Padding(0, 10, 6, 0).Children(func() {
			if len(filtered) == 0 {
				emptyText := "无匹配节点"
				if len(snap.Nodes) == 0 {
					emptyText = "暂无节点"
				}
				ui.Column(c).Padding(32, 16).Center().Children(func() {
					ui.Text(c, emptyText).FontSize(12.5).TextColor(p.textMuted)
				})
				return
			}
			for _, node := range filtered {
				vm.renderNodeRow(c, p, node)
			}
		})
	})
}

func (vm *viewModel) renderNodeRow(c *ui.Context, p palette, node engine.NodeSnapshot) {
	bg := p.card
	bdr := p.border
	if node.Selected {
		bg = p.cardSelected
		bdr = p.borderStrong
	}

	row := ui.Row(c.Key(node.ID)).Padding(6.5, 10).Radius(9.5).
		Background(bg).Border(1, bdr).AlignItems(ui.Center).Gap(8).Cursor(ui.CursorPointer)
	if !node.Selected && row.Hovered() {
		row.Background(p.cardHover)
	}

	row.Children(func() {
		starIcon := svgStarOutline
		starColor := p.textMuted
		if node.Starred {
			starIcon = svgStarFilled
			starColor = p.starGold
		}
		starBox := ui.Box(c.Key("star")).Size(22, 22).Center().Radius(5).Cursor(ui.CursorPointer)
		if starBox.Hovered() {
			starBox.Background(p.pillBg)
		}
		starBox.Children(func() {
			ui.Icon(c, starIcon).Size(13, 13).TextColor(starColor)
		})
		if starBox.Clicked() {
			id := node.ID
			vm.runAsync(func() error {
				_, err := vm.app.eng.ToggleStar(id)
				return err
			}, "")
		}

		// 中间节点名称与协议标签（紧凑排布）
		infoBox := ui.Column(c.Key("select")).Grow(1).Gap(2).Cursor(ui.CursorPointer)
		infoBox.Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				title := ui.Text(c, node.DisplayName).FontSize(12.5).TextColor(p.text).Ellipsis("…")
				if node.Selected {
					title.Bold().TextColor(p.accent)
				}
				if node.EnableQuic {
					renderMiniTag(c, "QUIC", p.accent, p.accentSoft)
				}
				if node.NativeUDP {
					renderMiniTag(c, "UDP", p.success, p.successSoft)
				}
				if node.TrafficRate > 0 && node.TrafficRate != 1 {
					renderMiniTag(c, fmt.Sprintf("%.1fx", node.TrafficRate), p.textSecondary, p.pillBg)
				}
			})
			if node.Description != "" {
				ui.Text(c, node.Description).FontSize(10).TextColor(p.textMuted).Ellipsis("…")
			}
		})
		if infoBox.Clicked() {
			id := node.ID
			name := node.DisplayName
			vm.runAsync(func() error { return vm.app.eng.SelectNode(id) }, "已切换至 "+name)
		}

		delayText := FormatDelay(node.DelayMs, node.Testing)
		delayColor, delayBg := delayColors(node.DelayMs, node.Testing, p)
		delayPill := ui.Row(c.Key("delay")).MinWidth(58).Padding(3.5, 7).Radius(6.5).
			Background(delayBg).Center().Gap(4).Cursor(ui.CursorPointer).
			Tooltip("测速")
		delayPill.Children(func() {
			if node.Testing {
				ui.Spinner(c).Size(10, 10)
			}
			ui.Text(c, delayText).FontSize(11).Bold().TextColor(delayColor).NoWrap()
		})
		if delayPill.Clicked() {
			id := node.ID
			vm.runAsync(func() error {
				_, err := vm.app.eng.PingNode(id)
				return err
			}, "")
		}
	})
}

func (vm *viewModel) renderSettingsView(c *ui.Context, p palette) {
	snap := vm.snap
	if !vm.portDirty {
		vm.portDraft = strconv.Itoa(snap.ProxyPort)
	}
	if !vm.openAtLoginSet && vm.app != nil {
		vm.openAtLogin = mygo.App.OpenAtLogin()
		vm.openAtLoginSet = true
	}

	ui.Scroll(c.Key("settings-scroll")).Grow(1).Margin(0, -10, 0, 0).Children(func() {
		ui.Column(c).Gap(10).Padding(0, 10, 10, 0).Children(func() {

			// 1. 本地代理设置卡片
			renderSectionCard(c, p, "本地代理", func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, "代理端口").Grow(1).FontSize(12.5).TextColor(p.text)
					inp := ui.TextInput(c.Key("port-input"), &vm.portDraft).Width(80)
					if inp.Changed() {
						vm.portDirty = true
					}
					saveBtn := ui.PrimaryButton(c.Key("save-port"), "应用").Padding(5, 11).Radius(7)
					if saveBtn.Clicked() || inp.Submitted() {
						port, err := strconv.Atoi(strings.TrimSpace(vm.portDraft))
						if err != nil || port <= 0 || port > 65535 {
							vm.toastMsg = "端口号需在 1-65535 之间"
						} else {
							vm.portDirty = false
							allowLAN := snap.AllowLAN
							vm.runAsync(func() error {
								return vm.app.eng.UpdateLocalProxySettings(port, allowLAN)
							}, fmt.Sprintf("端口已改为 %d", port))
						}
					}
					if renderToolbarIconBtn(c, p, "copy-proxy", "复制代理命令", "复制终端代理环境变量", svgCopy, false, p.accent, p.accentSoft, p.borderStrong, false) {
						cmd := fmt.Sprintf("export http_proxy=http://127.0.0.1:%d https_proxy=http://127.0.0.1:%d all_proxy=socks5://127.0.0.1:%d",
							snap.ProxyPort, snap.ProxyPort, snap.ProxyPort)
						if vm.app != nil {
							mygo.Clipboard.WriteText(cmd)
						}
						vm.toastMsg = "已复制代理命令"
					}
				})

				ui.Divider(c)
				allowLAN := snap.AllowLAN
				renderSettingSwitch(c, p, "sw-lan", "允许局域网连接", "监听 0.0.0.0，供同网络设备使用", &allowLAN, func(v bool) {
					port := snap.ProxyPort
					vm.runAsync(func() error {
						return vm.app.eng.UpdateLocalProxySettings(port, v)
					}, toggleToast("局域网连接", v))
				})

				ui.Divider(c)
				autoSys := snap.AutoSetSysProxy
				renderSettingSwitch(c, p, "sw-auto-sys", "启动时自动开启系统代理", "退出时自动关闭", &autoSys, func(v bool) {
					vm.runAsync(func() error {
						return vm.app.eng.SetAutoSysProxyOnStartUp(v)
					}, toggleToast("自动系统代理", v))
				})

				if snap.ProxyAuthEnabled {
					ui.Text(c, "已启用本地代理鉴权").FontSize(11).TextColor(p.textMuted)
				}
			})

			// 2. 网络与分流设置卡片
			renderSectionCard(c, p, "网络与分流", func() {
				ipv6 := snap.IPv6Enable
				renderSettingSwitch(c, p, "sw-ipv6", "启用 IPv6", "", &ipv6, func(v bool) {
					vm.runAsync(func() error {
						return vm.app.eng.UpdateNetworkSettings(v, snap.UseBuildInDNS, snap.DirectConnPriority)
					}, toggleToast("IPv6", v))
				})

				ui.Divider(c)
				dns := snap.UseBuildInDNS
				renderSettingSwitch(c, p, "sw-dns", "内置 DNS", "", &dns, func(v bool) {
					vm.runAsync(func() error {
						return vm.app.eng.UpdateNetworkSettings(snap.IPv6Enable, v, snap.DirectConnPriority)
					}, toggleToast("内置 DNS", v))
				})

				ui.Divider(c)
				directPrio := snap.DirectConnPriority
				renderSettingSwitch(c, p, "sw-dprio", "直连优先", "", &directPrio, func(v bool) {
					vm.runAsync(func() error {
						return vm.app.eng.UpdateNetworkSettings(snap.IPv6Enable, snap.UseBuildInDNS, v)
					}, toggleToast("直连优先", v))
				})

				ui.Divider(c)
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					geoState := "未加载 GeoIP"
					if snap.GeoIPLoaded {
						geoState = "已加载 GeoIP"
					}
					ui.Text(c, fmt.Sprintf("%d 条生效规则 · %s", snap.RuleCount, geoState)).
						Grow(1).FontSize(11.5).TextColor(p.textMuted).Ellipsis("…")
					if ui.Button(c.Key("btn-view-active-rules"), "查看规则").Padding(5, 10).Radius(7).
						Tooltip("查看全部生效分流规则").Clicked() {
						vm.openActiveRulesModal()
					}
					if renderToolbarIconBtn(c, p, "btn-reload-cfg", "重载配置", "重载配置", svgRefresh, false, p.accent, p.accentSoft, p.borderStrong, false) {
						vm.runAsync(func() error { return vm.app.eng.Reload() }, "配置已重载")
					}
				})
			})

			// 3. 界面与系统偏好卡片
			renderSectionCard(c, p, "界面与偏好", func() {
				themeIdx := themeModeToIndex(vm.currentPrefs().ThemeMode)
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "外观").Grow(1).FontSize(12.5).TextColor(p.text)
					if ui.Segmented(c.Key("theme-seg"), &themeIdx, "跟随系统", "浅色", "深色").Changed() {
						mode := indexToThemeMode(themeIdx)
						if vm.app != nil {
							vm.app.setThemeMode(mode)
						}
					}
				})

				if runtime.GOOS == "darwin" {
					ui.Divider(c)
					showSpeed := vm.currentPrefs().ShowTraySpeed
					renderSettingSwitch(c, p, "sw-tray-speed", "菜单栏显示网速", "每 3 秒更新上下行速率", &showSpeed, func(v bool) {
						if vm.app != nil {
							vm.app.setShowTraySpeed(v)
						}
					})
				}

				ui.Divider(c)
				login := vm.openAtLogin
				renderSettingSwitch(c, p, "sw-autostart", "开机自启", "登录后静默运行", &login, func(v bool) {
					vm.openAtLogin = v
					if vm.app != nil {
						if err := mygo.App.SetOpenAtLogin(v); err != nil {
							vm.openAtLogin = mygo.App.OpenAtLogin()
							vm.toastMsg = "设置失败: " + err.Error()
						} else {
							vm.toastMsg = toggleToast("开机自启", v)
						}
					}
				})
			})

			// 4. 关于与更新卡片
			renderSectionCard(c, p, "关于与更新", func() {
				renderKVRow(c, p, "客户端版本", version.Full())
				renderKVRow(c, p, "核心版本", nonEmpty(snap.CoreVersion, "未知"))
				renderKVRow(c, p, "核心路径", nonEmpty(snap.CoreLibPath, "未检测到"))
				renderKVRow(c, p, "配置文件", nonEmpty(snap.ConfigPath, "未知"))

				ui.Divider(c)
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					cliDesc := "未链接"
					cliColor := p.textMuted
					if vm.cliStatusLoaded && vm.cliStatus.Installed {
						cliDesc = nonEmpty(vm.cliStatus.Detail, vm.cliStatus.LinkPath)
						if vm.cliStatus.InPATH {
							cliColor = p.textSecondary
						} else {
							cliColor = p.warning
						}
					}
					ui.Column(c).Grow(1).Gap(2).Children(func() {
						ui.Text(c, "命令行 (fsbar)").FontSize(11).TextColor(p.textMuted)
						ui.Text(c, cliDesc).FontSize(11.5).TextColor(cliColor).Wrap()
					})
					btnLabel := "链接到 PATH"
					if vm.cliStatusLoaded && vm.cliStatus.Installed {
						btnLabel = "重新链接"
					}
					if ui.Button(c.Key("btn-install-cli"), btnLabel).Disabled(vm.cliInstalling).Padding(5, 11).Radius(7).Clicked() {
						if vm.app != nil {
							vm.cliInstalling = true
							vm.app.installCLILink()
						}
					}
				})

				ui.Divider(c)
				vm.renderUpdateSection(c, p)
			})
		})
	})
}

func (vm *viewModel) checkUpdate() {
	if vm.app == nil || vm.checkingUpdate || vm.updateDownloading {
		return
	}
	vm.checkingUpdate = true
	vm.updateErr = ""

	// 正式打包且安装目录可写时，优先使用 MyGo 签名增量更新清单；
	// 开发态或只读目录平滑回退到 GitHub Releases API 检查。
	if vm.app.updates != nil && vm.app.updates.enabled() {
		vm.updateNativeEnabled = true
		updates := vm.app.updates
		go func() {
			st, err := updates.Check(context.Background())
			vm.app.updateWindow(func() {
				vm.checkingUpdate = false
				if err != nil {
					vm.updateErr = err.Error()
					return
				}
				vm.applyNativeUpdateStatus(st)
				if st.Available == "" && !st.Installed {
					vm.updateResult = &updater.CheckResult{
						CurrentVersion: st.Version,
						HasUpdate:      false,
					}
				}
			})
			mygo.RunOnMain(func() { vm.app.refreshTrayMenu() })
		}()
		return
	}

	repo := vm.currentPrefs().UpdateRepo
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := updater.Check(ctx, repo, version.Version)
		vm.app.updateWindow(func() {
			vm.checkingUpdate = false
			if err != nil {
				vm.updateErr = "检查更新失败: " + err.Error()
				return
			}
			vm.updateResult = res
		})
	}()
}

func (vm *viewModel) installNativeUpdate() {
	if vm.app == nil || vm.app.updates == nil || vm.updateDownloading || vm.updateInstalled {
		return
	}
	if vm.app.updateProgressReset != nil {
		vm.app.updateProgressReset()
	}
	vm.updateDownloading = true
	vm.updateDownloaded = 0
	vm.updateTotal = 0
	vm.updateErr = ""

	ctx, cancel := context.WithCancel(context.Background())
	vm.updateCancel = cancel
	updates := vm.app.updates
	go func() {
		defer cancel()
		st, err := updates.Install(ctx)
		vm.app.updateWindow(func() {
			vm.updateCancel = nil
			vm.updateDownloading = false
			if err != nil {
				vm.updateErr = err.Error()
				return
			}
			vm.applyNativeUpdateStatus(st)
			vm.toastMsg = "更新已就绪，重启后生效"
		})
		mygo.RunOnMain(func() { vm.app.refreshTrayMenu() })
	}()
}

func (vm *viewModel) cancelUpdateDownload() {
	if vm.updateCancel != nil {
		vm.updateCancel()
	}
	if vm.app != nil && vm.app.updates != nil {
		vm.app.updates.CancelInstall()
	}
}

func (vm *viewModel) restartForUpdate() {
	if vm.app == nil || vm.app.updates == nil {
		return
	}
	if err := vm.app.updates.Restart(); err != nil {
		vm.updateErr = err.Error()
	}
}

func (vm *viewModel) filteredNodes() []engine.NodeSnapshot {
	q := strings.ToLower(strings.TrimSpace(vm.searchQuery))
	out := make([]engine.NodeSnapshot, 0, len(vm.snap.Nodes))
	for _, n := range vm.snap.Nodes {
		if vm.onlyStarred && !n.Starred {
			continue
		}
		if q != "" {
			if !strings.Contains(strings.ToLower(n.DisplayName), q) &&
				!strings.Contains(strings.ToLower(n.Name), q) &&
				!strings.Contains(strings.ToLower(n.Description), q) &&
				!strings.Contains(strconv.Itoa(n.ID), q) {
				continue
			}
		}
		out = append(out, n)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if vm.sortByDelay {
			di, dj := out[i].DelayMs, out[j].DelayMs
			if di <= 0 && dj <= 0 {
				return out[i].Starred && !out[j].Starred
			}
			if di <= 0 {
				return false
			}
			if dj <= 0 {
				return true
			}
			return di < dj
		}
		if out[i].Starred != out[j].Starred {
			return out[i].Starred
		}
		return false
	})
	return out
}

func (vm *viewModel) currentPrefs() config.AppPrefs {
	if vm.app != nil {
		return vm.app.prefs
	}
	return config.AppPrefs{ThemeMode: "system"}
}

func (vm *viewModel) runAsync(fn func() error, okToast string) {
	if vm.app == nil || vm.app.eng == nil {
		return
	}
	go func() {
		err := fn()
		vm.app.updateWindow(func() {
			if err != nil {
				vm.toastMsg = err.Error()
			} else if okToast != "" {
				vm.toastMsg = okToast
			}
		})
	}()
}

func renderToolbarIconBtn(c *ui.Context, p palette, key, label, tip string, svg *ui.SVG, active bool, activeFg, activeBg, activeBorder ui.Color, disabled bool) bool {
	btn := ui.Box(c.Key(key)).Size(30, 30).Radius(8).Center().Label(label).Tooltip(tip)
	fg := p.textSecondary
	if active {
		btn.Background(activeBg).Border(1, activeBorder)
		fg = activeFg
	} else {
		btn.Background(p.card).Border(1, p.border)
	}
	if disabled {
		btn.Opacity(0.5)
	} else {
		btn.Cursor(ui.CursorPointer)
		if !active && btn.Hovered() {
			btn.Background(p.cardHover)
			fg = p.text
		}
	}
	btn.Children(func() {
		ui.Icon(c, svg).Size(14.5, 14.5).TextColor(fg)
	})
	return !disabled && btn.Clicked()
}

func renderPill(c *ui.Context, text string, fg, bg ui.Color) {
	ui.Row(c).Padding(2, 6.5).Radius(99).Background(bg).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(10).Bold().TextColor(fg).NoWrap()
	})
}

func renderMiniTag(c *ui.Context, text string, fg, bg ui.Color) {
	ui.Row(c).Padding(1, 5).Radius(4.5).Background(bg).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(9.5).Bold().TextColor(fg).NoWrap()
	})
}

func renderSectionCard(c *ui.Context, p palette, title string, body func()) {
	ui.Column(c).Padding(12).Gap(9).Radius(12).Background(p.card).Border(1, p.border).Children(func() {
		ui.Text(c, title).FontSize(11.5).Bold().TextColor(p.textMuted)
		body()
	})
}

func renderSettingSwitch(c *ui.Context, p palette, key, title, desc string, val *bool, onChange func(bool)) {
	ui.Row(c.Key(key)).AlignItems(ui.Center).Gap(10).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(12.5).TextColor(p.text)
			if desc != "" {
				ui.Text(c, desc).FontSize(11).TextColor(p.textMuted)
			}
		})
		if ui.Switch(c.Key(key+"-sw"), val).Changed() {
			onChange(*val)
		}
	})
}

func renderKVRow(c *ui.Context, p palette, label, val string) {
	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(p.textMuted)
		ui.Text(c, val).FontSize(11.5).TextColor(p.textSecondary).Wrap()
	})
}

func stateDotColor(state engine.TunnelState, p palette) ui.Color {
	switch state {
	case engine.StateConnected:
		return p.success
	case engine.StateConnecting:
		return p.warning
	case engine.StateError:
		return p.danger
	default:
		return p.textMuted
	}
}

func stateBgColor(state engine.TunnelState, p palette) ui.Color {
	switch state {
	case engine.StateConnected:
		return p.successSoft
	case engine.StateConnecting:
		return p.warningSoft
	case engine.StateError:
		return p.dangerSoft
	default:
		return p.pillBg
	}
}

func delayColors(delayMs int64, testing bool, p palette) (ui.Color, ui.Color) {
	if testing {
		return p.accent, p.accentSoft
	}
	switch {
	case delayMs < 0:
		return p.danger, p.dangerSoft
	case delayMs == 0:
		return p.textMuted, p.pillBg
	case delayMs < 120:
		return p.success, p.successSoft
	case delayMs < 280:
		return p.warning, p.warningSoft
	default:
		return p.danger, p.dangerSoft
	}
}

func themeModeToIndex(mode string) int {
	switch mode {
	case "light":
		return 1
	case "dark":
		return 2
	default:
		return 0
	}
}

func indexToThemeMode(idx int) string {
	switch idx {
	case 1:
		return "light"
	case 2:
		return "dark"
	default:
		return "system"
	}
}

func toggleToast(name string, on bool) string {
	if on {
		return "已开启" + name
	}
	return "已关闭" + name
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
