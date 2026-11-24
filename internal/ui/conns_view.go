package ui

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
)

// connSortField 定义连接监控列表支持的排序字段。
type connSortField int

const (
	connSortByCreatedAt connSortField = iota // 创建时间（默认）
	connSortByDownSpeed                      // 下载速度
	connSortByDownTotal                      // 下载总量
)

var connSortOptions = []struct {
	field connSortField
	label string
}{
	{connSortByDownSpeed, "下载速度"},
	{connSortByDownTotal, "下载总量"},
	{connSortByCreatedAt, "创建时间"},
}

func connSortFieldLabel(f connSortField) string {
	switch f {
	case connSortByDownSpeed:
		return "下载速度"
	case connSortByDownTotal:
		return "下载总量"
	default:
		return "创建时间"
	}
}

func (vm *viewModel) connSortDirArrow() string {
	if vm.connSortAsc {
		return "↑"
	}
	return "↓"
}

func (vm *viewModel) connSortDirText() string {
	if vm.connSortAsc {
		return "升序"
	}
	return "降序"
}

func (vm *viewModel) isCustomConnSort() bool {
	return vm.connSortField != connSortByCreatedAt || vm.connSortAsc
}

func (vm *viewModel) renderConnectionsView(c *ui.Context, p palette) {
	// 1. 工具栏：搜索、排序、暂停/继续、手动刷新
	var sortBtn ui.Element
	ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
		ui.SearchField(c.Key("conn-search"), &vm.connSearchQuery).Grow(1).Placeholder("搜索连接…")

		sortIcon := svgSortDesc
		if vm.connSortAsc {
			sortIcon = svgSortAsc
		}
		sortActive := vm.connSortMenuOpen || vm.isCustomConnSort()
		sortTip := fmt.Sprintf("排序：%s（%s）", connSortFieldLabel(vm.connSortField), vm.connSortDirText())
		sortBtn = ui.Box(c.Key("conn-sort")).Size(30, 30).Radius(8).Center().
			Label("排序").Tooltip(sortTip).Cursor(ui.CursorPointer)
		sortFg := p.textSecondary
		if sortActive {
			sortBtn.Background(p.accentSoft).Border(1, p.borderStrong)
			sortFg = p.accent
		} else {
			sortBtn.Background(p.card).Border(1, p.border)
			if sortBtn.Hovered() {
				sortBtn.Background(p.cardHover)
				sortFg = p.text
			}
		}
		sortBtn.Children(func() {
			ui.Icon(c, sortIcon).Size(14.5, 14.5).TextColor(sortFg)
		})
		if sortBtn.Clicked() {
			vm.connSortMenuOpen = !vm.connSortMenuOpen
		}

		pauseIcon := svgPause
		pauseTip := "暂停刷新"
		if vm.connsPaused {
			pauseIcon = svgPlay
			pauseTip = "继续刷新"
		}
		if renderToolbarIconBtn(c, p, "conn-pause", pauseTip, pauseTip, pauseIcon, vm.connsPaused, p.warning, p.warningSoft, p.warning, false) {
			vm.connsPaused = !vm.connsPaused
		}

		if renderToolbarIconBtn(c, p, "conn-refresh", "刷新", "刷新", svgRefresh, false, p.accent, p.accentSoft, p.borderStrong, false) {
			if vm.app != nil {
				go vm.app.refreshConnections()
			}
		}
	})

	vm.renderConnSortPopover(c, p, sortBtn)

	filtered := vm.filteredConnections()

	// 2. 状态摘要行（左侧显示连接数与当前排序，右侧提示快捷添加规则操作）
	ui.Row(c).AlignItems(ui.Center).Gap(8).Padding(0, 2).Children(func() {
		summary := fmt.Sprintf("共 %d 条连接", len(vm.connections))
		if len(filtered) != len(vm.connections) {
			summary = fmt.Sprintf("显示 %d / %d 条连接", len(filtered), len(vm.connections))
		}
		summary += fmt.Sprintf(" · %s%s", connSortFieldLabel(vm.connSortField), vm.connSortDirArrow())
		ui.Text(c, summary).FontSize(11).TextColor(p.textMuted).NoWrap()
		ui.Spacer(c)
		ui.Text(c, "可直接点击直连/代理按钮快速添加规则").FontSize(10.5).TextColor(p.textMuted).Ellipsis("…")
	})

	// 3. 连接列表（右侧独立细滚动条槽道）
	ui.Scroll(c.Key("conns-scroll")).Grow(1).Margin(0, -10, 0, 0).Children(func() {
		ui.Column(c).Gap(5).Padding(0, 10, 6, 0).Children(func() {
			if len(filtered) == 0 {
				emptyText := "无匹配连接"
				if len(vm.connections) == 0 {
					emptyText = "暂无活跃连接"
				}
				ui.Column(c).Padding(28, 16).Center().Children(func() {
					ui.Text(c, emptyText).FontSize(12.5).TextColor(p.textMuted)
				})
				return
			}
			for i, conn := range filtered {
				vm.renderConnectionRow(c, p, i, conn)
			}
		})
	})
}

// renderConnSortPopover 渲染排序按钮下方的弹出菜单，支持一键选择排序维度及升/降序。
func (vm *viewModel) renderConnSortPopover(c *ui.Context, p palette, anchor ui.Element) {
	if !vm.connSortMenuOpen {
		return
	}
	shadowAlpha := float32(0.14)
	if c.Theme().Dark {
		shadowAlpha = 0.34
	}
	ui.PopoverBase(c.Key("conn-sort-popover"), anchor, &vm.connSortMenuOpen, func(panel ui.Element) {
		panel.AttachTo(anchor, ui.AnchorBottomRight, ui.AnchorTopRight).Margin(6, 0, 0, 0).
			Width(218).Padding(6).Radius(11).
			Background(p.card).Border(1, p.borderStrong).
			Shadow(0, 6, 18, 0, ui.RGBA(0, 0, 0, shadowAlpha)).
			Children(func() {
				ui.Column(c).Gap(3).Children(func() {
					ui.Row(c).Padding(2, 6, 3, 6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "排序方式").FontSize(10.5).Bold().TextColor(p.textMuted)
					})
					for _, opt := range connSortOptions {
						field := opt.field
						label := opt.label
						isFieldActive := vm.connSortField == field

						row := ui.Row(c.Key(fmt.Sprintf("sort-row-%d", field))).
							Padding(4, 6).Radius(7.5).AlignItems(ui.Center).Gap(5)
						if isFieldActive {
							row.Background(p.cardSelected)
						} else if row.Hovered() {
							row.Background(p.cardHover)
						}

						row.Children(func() {
							// 点击左侧字段名：切到该字段默认降序；若已是当前字段则翻转升降序
							lblBox := ui.Row(c.Key("label")).Grow(1).AlignItems(ui.Center).
								Cursor(ui.CursorPointer).Label(label)
							lblBox.Children(func() {
								txt := ui.Text(c, label).FontSize(12).NoWrap()
								if isFieldActive {
									txt.Bold().TextColor(p.accent)
								} else {
									txt.TextColor(p.text)
								}
							})
							if lblBox.Clicked() {
								if vm.connSortField == field {
									vm.connSortAsc = !vm.connSortAsc
								} else {
									vm.connSortField = field
									vm.connSortAsc = false
								}
								vm.connSortMenuOpen = false
							}

							vm.renderConnSortDirChip(c, p, field, false, label+"降序", "↓降序")
							vm.renderConnSortDirChip(c, p, field, true, label+"升序", "↑升序")
						})
					}
				})
			})
	})
}

func (vm *viewModel) renderConnSortDirChip(c *ui.Context, p palette, field connSortField, asc bool, a11yLabel, text string) {
	selected := vm.connSortField == field && vm.connSortAsc == asc
	key := "desc"
	if asc {
		key = "asc"
	}
	chip := ui.Row(c.Key(key)).Padding(2.5, 6).Radius(6).Center().
		Cursor(ui.CursorPointer).Label(a11yLabel)
	if selected {
		chip.Background(p.accentSoft).Border(1, p.borderStrong)
	} else {
		chip.Background(p.cardElevated).Border(1, p.border)
		if chip.Hovered() {
			chip.Background(p.cardHover)
		}
	}
	chip.Children(func() {
		txt := ui.Text(c, text).FontSize(10.5).NoWrap()
		if selected {
			txt.Bold().TextColor(p.accent)
		} else {
			txt.TextColor(p.textSecondary)
		}
	})
	if chip.Clicked() {
		vm.connSortField = field
		vm.connSortAsc = asc
		vm.connSortMenuOpen = false
	}
}

func (vm *viewModel) renderConnectionRow(c *ui.Context, p palette, idx int, conn core.ConnectionInfo) {
	key := fmt.Sprintf("conn-%d-%s", idx, conn.CloseKey())

	ui.Row(c.Key(key)).Padding(7, 10).Radius(10).Background(p.card).Border(1, p.border).
		AlignItems(ui.Center).Gap(8).Children(func() {

		// 左侧目标信息与元标签
		ui.Column(c).Grow(1).MinWidth(0).Gap(2.5).Children(func() {
			target := conn.TargetAddr
			if conn.TargetPort > 0 {
				target = fmt.Sprintf("%s:%d", conn.TargetAddr, conn.TargetPort)
			}
			ui.Text(c, target).FontSize(12).Bold().TextColor(p.text).Ellipsis("…")

			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				if conn.Protocol != "" {
					renderMiniTag(c, strings.ToUpper(conn.Protocol), p.textSecondary, p.pillBg)
				}
				routeFg, routeBg, routeLabel := connRouteStyle(conn.ProxyType, p)
				renderMiniTag(c, routeLabel, routeFg, routeBg)
				if conn.ProcessName != "" {
					procLabel := compactProcessLabel(conn.ProcessName, 16)
					ui.Row(c).Padding(1, 5).Radius(4.5).Background(p.pillBg).
						AlignItems(ui.Center).Tooltip(conn.ProcessName).Children(func() {
						ui.Text(c, procLabel).FontSize(9.5).Bold().TextColor(p.textSecondary).NoWrap()
					})
				}
				trafficTip := fmt.Sprintf("实时 ↑%s ↓%s · 累计 ↑%s ↓%s",
					FormatSpeed(conn.UploadTraffic), FormatSpeed(conn.DownloadTraffic),
					FormatBytes(conn.UploadTrafficTotal), FormatBytes(conn.DownloadTrafficTotal))
				if vm.connSortField == connSortByDownSpeed {
					speedColor := p.textMuted
					if conn.DownloadTraffic > 0 {
						speedColor = p.accent
					}
					ui.Text(c, "↓"+FormatSpeed(conn.DownloadTraffic)).FontSize(10).Bold().
						TextColor(speedColor).NoWrap().Tooltip(trafficTip)
					ui.Text(c, "总↓"+FormatBytes(conn.DownloadTrafficTotal)).FontSize(10).
						TextColor(p.textMuted).NoWrap().Tooltip(trafficTip)
				} else {
					traffic := fmt.Sprintf("↑%s ↓%s", FormatBytes(conn.UploadTrafficTotal), FormatBytes(conn.DownloadTrafficTotal))
					ui.Text(c, traffic).FontSize(10).TextColor(p.textMuted).NoWrap().Tooltip(trafficTip)
				}
			})
		})

		// 右侧快捷操作：加直连 / 加代理 / 断开
		if conn.TargetAddr != "" {
			if ui.Button(c.Key("add-direct"), "直连").Padding(3, 7).Radius(6).
				TextColor(p.success).Tooltip("加为直连规则").Clicked() {
				vm.quickAddRuleFromConn(conn, 0)
			}
			if ui.Button(c.Key("add-proxy"), "代理").Padding(3, 7).Radius(6).
				TextColor(p.accent).Tooltip("加为代理规则").Clicked() {
				vm.quickAddRuleFromConn(conn, 2)
			}
		}
		closeBtn := ui.Box(c.Key("close")).Size(24, 24).Center().Radius(6).
			Cursor(ui.CursorPointer).Label("断开连接").Tooltip("断开连接")
		closeColor := p.textMuted
		if closeBtn.Hovered() {
			closeBtn.Background(p.dangerSoft)
			closeColor = p.danger
		}
		closeBtn.Children(func() {
			ui.Icon(c, svgClose).Size(13, 13).TextColor(closeColor)
		})
		if closeBtn.Clicked() {
			closeKey := conn.CloseKey()
			vm.runAsync(func() error {
				vm.app.eng.CloseConnection(closeKey)
				vm.app.refreshConnections()
				return nil
			}, "已断开连接")
		}
	})
}

func (vm *viewModel) quickAddRuleFromConn(conn core.ConnectionInfo, action int) {
	domain, matching := suggestRuleFromTarget(conn.TargetAddr)
	if domain == "" {
		return
	}
	closeKey := conn.CloseKey()
	vm.runAsync(func() error {
		_, err := vm.app.eng.AddCustomRule(config.DomainRule{
			Domain:    domain,
			Matching:  matching,
			ProxyType: action,
			Enable:    config.IntPtr(1),
		})
		if err != nil {
			return err
		}
		// 踢掉旧连接，使新分流规则立即对下一次请求生效
		vm.app.eng.CloseConnection(closeKey)
		vm.app.refreshConnections()
		return nil
	}, fmt.Sprintf("已设为%s：%s", config.RuleActionLabel(action), domain))
}

// suggestRuleFromTarget 根据连接目标自动推导合适的规则值与匹配类型（IP 用 /32 或 /128 CIDR，域名用后缀）。
func suggestRuleFromTarget(target string) (string, int) {
	t := strings.TrimSpace(target)
	if t == "" {
		return "", config.MatchDomainSuffix
	}
	if ip := net.ParseIP(t); ip != nil {
		if ip.To4() != nil {
			return ip.String() + "/32", config.MatchCIDR4
		}
		return ip.String() + "/128", config.MatchCIDR6
	}
	return t, config.MatchDomainSuffix
}

func (vm *viewModel) filteredConnections() []core.ConnectionInfo {
	q := strings.ToLower(strings.TrimSpace(vm.connSearchQuery))
	out := make([]core.ConnectionInfo, 0, len(vm.connections))
	if q == "" {
		out = append(out, vm.connections...)
	} else {
		for _, c := range vm.connections {
			if strings.Contains(strings.ToLower(c.TargetAddr), q) ||
				strings.Contains(strings.ToLower(c.ProcessName), q) ||
				strings.Contains(strings.ToLower(c.ProxyType), q) ||
				strings.Contains(strings.ToLower(c.MatcherRule), q) {
				out = append(out, c)
			}
		}
	}
	sortConnections(out, vm.connSortField, vm.connSortAsc)
	return out
}

func sortConnections(list []core.ConnectionInfo, field connSortField, asc bool) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		var va, vb int64
		switch field {
		case connSortByDownSpeed:
			va, vb = a.DownloadTraffic, b.DownloadTraffic
		case connSortByDownTotal:
			va, vb = a.DownloadTrafficTotal, b.DownloadTrafficTotal
		default:
			va, vb = a.ActiveTime, b.ActiveTime
		}
		if va != vb {
			if asc {
				return va < vb
			}
			return va > vb
		}
		// 主字段相同时按创建时间降序 → 总流量降序 → 连接键升序稳定兜底
		if a.ActiveTime != b.ActiveTime {
			return a.ActiveTime > b.ActiveTime
		}
		ti := a.UploadTrafficTotal + a.DownloadTrafficTotal
		tj := b.UploadTrafficTotal + b.DownloadTrafficTotal
		if ti != tj {
			return ti > tj
		}
		return a.CloseKey() < b.CloseKey()
	})
}

func connRouteStyle(proxyType string, p palette) (ui.Color, ui.Color, string) {
	switch strings.ToLower(strings.TrimSpace(proxyType)) {
	case "direct", "0":
		return p.success, p.successSoft, "直连"
	case "reject", "1":
		return p.danger, p.dangerSoft, "拒绝"
	default:
		return p.accent, p.accentSoft, "代理"
	}
}

// compactProcessLabel 将过长的进程名（如 "Chromium Helper (Network)"）截断为紧凑单行标签，完整名通过 Tooltip 查看。
func compactProcessLabel(name string, maxRunes int) string {
	r := []rune(strings.TrimSpace(name))
	if len(r) <= maxRunes || maxRunes < 4 {
		return string(r)
	}
	return strings.TrimSpace(string(r[:maxRunes-1])) + "…"
}
