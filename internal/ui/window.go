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
	connections     []core.ConnectionInfo
	connSearchQuery string
	connsPaused     bool

	checkingUpdate bool
	updateResult   *updater.CheckResult
	updateErr      string
	toastMsg       string
}

func newViewModel(app *App, initial engine.Snapshot) *viewModel {
	return &viewModel{
		app:              app,
		snap:             initial,
		portDraft:        strconv.Itoa(initial.ProxyPort),
		ruleMatchDraft:   config.MatchDomainSuffix,
		ruleActionDraft:  2,
		ruleFilterAction: -1,
	}
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
	c.SetTheme(&themed)

	ui.Column(c).Fill().Background(p.canvas).Children(func() {
		vm.renderTitleBar(c, p)
		ui.Column(c).Grow(1).Padding(4, 14, 12, 14).Gap(9).Children(func() {
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
}

func (vm *viewModel) renderTitleBar(c *ui.Context, p palette) {
	bar := c.TitleBar()
	h := max(bar.Height, 40)
	leftPad := bar.Left + 12
	rightPad := bar.Right + 12

	ui.Row(c).Height(h).Padding(0, rightPad, 0, leftPad).AlignItems(ui.Center).Gap(8).
		DragWindow().Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Box(c).Size(7.5, 7.5).Radius(99).Background(stateDotColor(vm.snap.State, p))
			ui.Text(c, "FlyShadow Bar").FontSize(13).Bold().TextColor(p.text)
		})
		ui.Spacer(c)
		prevTab := vm.activeTab
		if ui.Segmented(c.Key("nav-tabs"), &vm.activeTab, "节点", "规则", "连接", "设置").Changed() {
			if vm.activeTab == 2 && prevTab != 2 && vm.app != nil {
				go vm.app.refreshConnections()
			}
		}
	})
}

// renderCompactOverview 紧凑高密度顶部状态区，为下方列表留出更大单屏视野。
func (vm *viewModel) renderCompactOverview(c *ui.Context, p palette) {
	snap := vm.snap
	nodeName := "未选择节点"
	if snap.CurrentNode != nil {
		nodeName = snap.CurrentNode.DisplayName
	}

	ui.Column(c).Padding(10, 12).Gap(8).Radius(13).
		Background(p.card).Border(1, p.border).
		Shadow(0, 1, 6, 0, ui.RGBA(0, 0, 0, 0.04)).Children(func() {

		// 第一行：节点名 + 状态/协议胶囊 + 紧凑实时指标 + 端口复制 + 连接/断开按钮
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Column(c).Grow(1).Gap(4).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					renderPill(c, StateLabel(snap.State), stateDotColor(snap.State, p), stateBgColor(snap.State, p))
					ui.Text(c, nodeName).Grow(1).FontSize(14).Bold().TextColor(p.text).Ellipsis("…")
					if snap.EffectiveTransport.EnableQuic {
						renderMiniTag(c, "QUIC", p.accent, p.accentSoft)
					}
					if snap.EffectiveTransport.EnableNativeUDP {
						renderMiniTag(c, "UDP", p.success, p.successSoft)
					}
				})

				// 紧凑一行实时指标条
				ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
					ui.Text(c, "↑ "+FormatSpeed(snap.UploadSpeedBps)).FontSize(11).TextColor(p.textSecondary).NoWrap()
					ui.Text(c, "↓ "+FormatSpeed(snap.DownloadSpeedBps)).FontSize(11).TextColor(p.textSecondary).NoWrap()

					pingVal := "--"
					if snap.State == engine.StateConnected {
						if snap.TunnelPingMs > 0 {
							pingVal = fmt.Sprintf("%d ms", snap.TunnelPingMs)
						} else if snap.CurrentNode != nil && snap.CurrentNode.DelayMs > 0 {
							pingVal = fmt.Sprintf("%d ms", snap.CurrentNode.DelayMs)
						}
					}
					ui.Text(c, "延迟 "+pingVal).FontSize(11).TextColor(p.textMuted).NoWrap()

					proxyChip := ui.Row(c.Key("copy-proxy")).Padding(1.5, 6).Radius(5).
						Background(p.cardElevated).Border(1, p.border).AlignItems(ui.Center).Gap(4).
						Cursor(ui.CursorPointer).Tooltip("点击复制终端代理导出命令")
					if proxyChip.Hovered() {
						proxyChip.Background(p.cardHover)
					}
					proxyChip.Children(func() {
						ui.Text(c, fmt.Sprintf(":%d", snap.ProxyPort)).FontSize(10.5).Bold().TextColor(p.textSecondary).NoWrap()
						ui.Icon(c, svgCopy).Size(10, 10).TextColor(p.textMuted)
					})
					if proxyChip.Clicked() {
						cmd := fmt.Sprintf("export http_proxy=http://127.0.0.1:%d https_proxy=http://127.0.0.1:%d all_proxy=socks5://127.0.0.1:%d",
							snap.ProxyPort, snap.ProxyPort, snap.ProxyPort)
						if vm.app != nil {
							mygo.Clipboard.WriteText(cmd)
						}
						vm.toastMsg = fmt.Sprintf("已复制代理命令（端口 %d）", snap.ProxyPort)
					}
				})
			})

			if snap.State == engine.StateConnected || snap.State == engine.StateConnecting {
				if ui.Button(c.Key("btn-disconnect"), "断开").Padding(5, 11).Radius(7).Clicked() {
					vm.runAsync(func() error { return vm.app.eng.Disconnect() }, "已断开隧道")
				}
			} else {
				if ui.PrimaryButton(c.Key("btn-connect"), "连接").Padding(5, 12).Radius(7).Clicked() {
					vm.runAsync(func() error { return vm.app.eng.Connect() }, "")
				}
			}
		})

		if snap.State == engine.StateError && snap.LastError != "" {
			ui.Row(c).Padding(5, 8).Radius(7).Background(p.dangerSoft).Children(func() {
				ui.Text(c, snap.LastError).FontSize(11).TextColor(p.danger).Wrap()
			})
		}

		// 第二行：代理模式三等分切换栏
		vm.renderModeBar(c, p, snap.ModeCode)

		// 第三行：三个紧凑快捷开关（系统代理 / UDP 加速 / 原生 UDP）
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

func (vm *viewModel) renderModeBar(c *ui.Context, p palette, active core.ProxyMode) {
	options := []struct {
		mode  core.ProxyMode
		label string
	}{
		{core.ProxyModeRule, "规则 Rule"},
		{core.ProxyModeGlobal, "全局 Global"},
		{core.ProxyModeDirect, "直连 Direct"},
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

	// 搜索与筛选工具栏
	ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
		ui.SearchField(c.Key("node-search"), &vm.searchQuery).Grow(1).Placeholder("搜索节点名称、地区或备注…")

		starBtn := ui.Button(c.Key("filter-star"), "收藏").Padding(5, 9).Radius(7)
		if vm.onlyStarred {
			starBtn.Background(p.warningSoft).Border(1, p.warning).TextColor(p.warning)
		}
		if starBtn.Clicked() {
			vm.onlyStarred = !vm.onlyStarred
		}

		sortLabel := "默认"
		if vm.sortByDelay {
			sortLabel = "延迟↑"
		}
		sortBtn := ui.Button(c.Key("sort-nodes"), sortLabel).Padding(5, 9).Radius(7)
		if vm.sortByDelay {
			sortBtn.Background(p.accentSoft).Border(1, p.borderStrong).TextColor(p.accent)
		}
		if sortBtn.Clicked() {
			vm.sortByDelay = !vm.sortByDelay
		}

		pingText := "测速"
		if snap.Pinging {
			pingText = fmt.Sprintf("%d/%d", snap.PingCompleted, snap.PingTotal)
		}
		pingBtn := ui.Button(c.Key("btn-ping"), pingText).Disabled(snap.Pinging).Padding(5, 9).Radius(7)
		if pingBtn.Clicked() {
			vm.runAsync(func() error {
				_, err := vm.app.eng.PingAll(context.Background())
				return err
			}, "全部节点测速完成")
		}
	})

	filtered := vm.filteredNodes()

	// 节点统计摘要行
	ui.Row(c).AlignItems(ui.Center).Padding(0, 2).Children(func() {
		ui.Text(c, fmt.Sprintf("共 %d 个节点（显示 %d 个）", len(snap.Nodes), len(filtered))).
			FontSize(10.5).TextColor(p.textMuted)
		ui.Spacer(c)
		if snap.Pinging {
			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				ui.Spinner(c).Size(11, 11)
				ui.Text(c, "并发测速中…").FontSize(10.5).TextColor(p.accent)
			})
		}
	})

	// 紧凑高容量节点列表（780px 高度下一屏直接可见 10+ 个节点）
	ui.Scroll(c.Key("node-scroll")).Grow(1).Children(func() {
		ui.Column(c).Gap(4).Padding(0, 2, 6, 0).Children(func() {
			if len(filtered) == 0 {
				ui.Column(c).Padding(32, 16).Center().Gap(6).Children(func() {
					ui.Text(c, "没有匹配的节点").FontSize(13).Bold().TextColor(p.textSecondary)
					ui.Text(c, "尝试清空搜索词或取消收藏筛选").FontSize(11.5).TextColor(p.textMuted)
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
			Tooltip("点击单独测速")
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

	ui.Scroll(c.Key("settings-scroll")).Grow(1).Children(func() {
		ui.Column(c).Gap(10).Padding(0, 2, 10, 0).Children(func() {

			// 1. 本地代理设置卡片
			renderSectionCard(c, p, "本地代理", func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "混合代理端口 (HTTP/SOCKS5)").Grow(1).FontSize(12.5).TextColor(p.text)
					inp := ui.TextInput(c.Key("port-input"), &vm.portDraft).Width(84)
					if inp.Changed() {
						vm.portDirty = true
					}
					saveBtn := ui.PrimaryButton(c.Key("save-port"), "应用").Padding(5, 12).Radius(7)
					if saveBtn.Clicked() || inp.Submitted() {
						port, err := strconv.Atoi(strings.TrimSpace(vm.portDraft))
						if err != nil || port <= 0 || port > 65535 {
							vm.toastMsg = "端口号必须是 1-65535 的整数"
						} else {
							vm.portDirty = false
							allowLAN := snap.AllowLAN
							vm.runAsync(func() error {
								return vm.app.eng.UpdateLocalProxySettings(port, allowLAN)
							}, fmt.Sprintf("本地代理端口已更新为 %d", port))
						}
					}
				})

				ui.Divider(c)
				allowLAN := snap.AllowLAN
				renderSettingSwitch(c, p, "sw-lan", "允许局域网设备共享代理", "开启后监听 0.0.0.0，关闭仅监听 127.0.0.1", &allowLAN, func(v bool) {
					port := snap.ProxyPort
					vm.runAsync(func() error {
						return vm.app.eng.UpdateLocalProxySettings(port, v)
					}, toggleToast("局域网共享", v))
				})

				ui.Divider(c)
				autoSys := snap.AutoSetSysProxy
				renderSettingSwitch(c, p, "sw-auto-sys", "启动连上后自动开启系统代理", "退出时仍会自动安全关闭系统代理", &autoSys, func(v bool) {
					vm.runAsync(func() error {
						return vm.app.eng.SetAutoSysProxyOnStartUp(v)
					}, toggleToast("启动自动开启系统代理", v))
				})

				if snap.ProxyAuthEnabled {
					ui.Text(c, "已从 FlyShadow 配置继承本地代理鉴权凭据").FontSize(11).TextColor(p.textMuted)
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
				renderSettingSwitch(c, p, "sw-dns", "使用核心内置 DNS", "", &dns, func(v bool) {
					vm.runAsync(func() error {
						return vm.app.eng.UpdateNetworkSettings(snap.IPv6Enable, v, snap.DirectConnPriority)
					}, toggleToast("内置 DNS", v))
				})

				ui.Divider(c)
				directPrio := snap.DirectConnPriority
				renderSettingSwitch(c, p, "sw-dprio", "直连优先探测", "", &directPrio, func(v bool) {
					vm.runAsync(func() error {
						return vm.app.eng.UpdateNetworkSettings(snap.IPv6Enable, snap.UseBuildInDNS, v)
					}, toggleToast("直连优先", v))
				})

				ui.Divider(c)
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					geoState := "未加载"
					if snap.GeoIPLoaded {
						geoState = "已加载 CN GeoIP"
					}
					ui.Text(c, fmt.Sprintf("生效规则 %d 条（自定义 %d 条）· %s", snap.RuleCount, len(snap.CustomRules), geoState)).
						Grow(1).FontSize(11.5).TextColor(p.textMuted)
					if ui.Button(c.Key("btn-reload-cfg"), "重载配置").Padding(5, 11).Radius(7).Clicked() {
						vm.runAsync(func() error { return vm.app.eng.Reload() }, "已重载 ~/.FlyShadow/config.json")
					}
				})
			})

			// 3. 界面与系统偏好卡片
			renderSectionCard(c, p, "界面与偏好", func() {
				themeIdx := themeModeToIndex(vm.currentPrefs().ThemeMode)
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "外观主题").Grow(1).FontSize(12.5).TextColor(p.text)
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
					renderSettingSwitch(c, p, "sw-tray-speed", "菜单栏显示实时速率", "使用等宽字体展示上下行速度", &showSpeed, func(v bool) {
						if vm.app != nil {
							vm.app.setShowTraySpeed(v)
						}
					})
				}

				ui.Divider(c)
				login := vm.openAtLogin
				renderSettingSwitch(c, p, "sw-autostart", "登录时自动启动", "随系统启动并静默驻留托盘", &login, func(v bool) {
					vm.openAtLogin = v
					if vm.app != nil {
						if err := mygo.App.SetOpenAtLogin(v); err != nil {
							vm.openAtLogin = mygo.App.OpenAtLogin()
							vm.toastMsg = "设置开机自启失败: " + err.Error()
						} else {
							vm.toastMsg = toggleToast("开机自启", v)
						}
					}
				})
			})

			// 4. 订阅用量、核心环境与更新检查卡片
			renderSectionCard(c, p, "订阅、环境与更新", func() {
				if snap.Traffic != nil && snap.Traffic.TrafficSize > 0 {
					used := snap.Traffic.TrafficUpload + snap.Traffic.TrafficDownload
					remain := max(int64(0), snap.Traffic.TrafficSize-used)
					renderKVRow(c, p, "订阅流量（本地快照）", fmt.Sprintf("已用 %s / 总计 %s（剩余 %s · 到期 %s）",
						FormatBytes(used), FormatBytes(snap.Traffic.TrafficSize), FormatBytes(remain), FormatExpireDate(snap.Traffic.ExpireTime)))
					ui.Divider(c)
				}
				renderKVRow(c, p, "客户端版本", version.Full())
				renderKVRow(c, p, "核心库版本", nonEmpty(snap.CoreVersion, "未知"))
				renderKVRow(c, p, "核心库路径", nonEmpty(snap.CoreLibPath, "未检测到"))
				renderKVRow(c, p, "配置文件", nonEmpty(snap.ConfigPath, "未知"))

				ui.Divider(c)
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					updateText := "基于 GitHub Releases 检查更新"
					updateColor := p.textMuted
					if vm.checkingUpdate {
						updateText = "正在检查新版本…"
						updateColor = p.accent
					} else if vm.updateErr != "" {
						updateText = vm.updateErr
						updateColor = p.danger
					} else if vm.updateResult != nil {
						if vm.updateResult.HasUpdate {
							updateText = fmt.Sprintf("发现新版本 %s", vm.updateResult.LatestVersion)
							updateColor = p.success
						} else {
							updateText = fmt.Sprintf("已是最新版本 (%s)", version.Version)
							updateColor = p.textSecondary
						}
					}
					ui.Text(c, updateText).Grow(1).FontSize(11.5).TextColor(updateColor).Wrap()

					if vm.updateResult != nil && vm.updateResult.HasUpdate && vm.updateResult.ReleaseURL != "" {
						if ui.PrimaryButton(c.Key("btn-open-release"), "前往下载").Padding(5, 11).Radius(7).Clicked() {
							url := vm.updateResult.ReleaseURL
							if vm.app != nil {
								go func() { _ = mygo.Shell.OpenExternal(url) }()
							}
						}
					}
					if ui.Button(c.Key("btn-check-update"), "检查更新").Disabled(vm.checkingUpdate).Padding(5, 11).Radius(7).Clicked() {
						vm.checkUpdate()
					}
				})
			})
		})
	})
}

func (vm *viewModel) checkUpdate() {
	if vm.app == nil {
		return
	}
	vm.checkingUpdate = true
	vm.updateErr = ""
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
