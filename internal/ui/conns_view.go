package ui

import (
	"fmt"
	"net"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
)

func (vm *viewModel) renderConnectionsView(c *ui.Context, p palette) {
	// 1. 工具栏：搜索、暂停/继续、手动刷新
	ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
		ui.SearchField(c.Key("conn-search"), &vm.connSearchQuery).Grow(1).Placeholder("搜索连接…")

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

	filtered := vm.filteredConnections()

	// 2. 状态摘要行（未筛选时只显示总数，避免重复）
	ui.Row(c).AlignItems(ui.Center).Padding(0, 2).Children(func() {
		summary := fmt.Sprintf("共 %d 条连接", len(vm.connections))
		if len(filtered) != len(vm.connections) {
			summary = fmt.Sprintf("显示 %d / %d 条连接", len(filtered), len(vm.connections))
		}
		ui.Text(c, summary).FontSize(11).TextColor(p.textMuted)
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
				traffic := fmt.Sprintf("↑%s ↓%s", FormatBytes(conn.UploadTrafficTotal), FormatBytes(conn.DownloadTrafficTotal))
				ui.Text(c, traffic).FontSize(10).TextColor(p.textMuted).NoWrap()
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
	if q == "" {
		return vm.connections
	}
	out := make([]core.ConnectionInfo, 0, len(vm.connections))
	for _, c := range vm.connections {
		if strings.Contains(strings.ToLower(c.TargetAddr), q) ||
			strings.Contains(strings.ToLower(c.ProcessName), q) ||
			strings.Contains(strings.ToLower(c.ProxyType), q) ||
			strings.Contains(strings.ToLower(c.MatcherRule), q) {
			out = append(out, c)
		}
	}
	return out
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
