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
	// 1. 工具栏：搜索、筛选、暂停/继续、手动刷新
	ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
		ui.SearchField(c.Key("conn-search"), &vm.connSearchQuery).Grow(1).Placeholder("搜索域名、IP、进程或命中规则…")

		pauseLabel := "暂停"
		if vm.connsPaused {
			pauseLabel = "继续"
		}
		pauseBtn := ui.Button(c.Key("conn-pause"), pauseLabel).Padding(5, 9).Radius(7)
		if vm.connsPaused {
			pauseBtn.Background(p.warningSoft).Border(1, p.warning).TextColor(p.warning)
		}
		if pauseBtn.Clicked() {
			vm.connsPaused = !vm.connsPaused
		}

		if ui.Button(c.Key("conn-refresh"), "刷新").Padding(5, 9).Radius(7).Clicked() {
			if vm.app != nil {
				go vm.app.refreshConnections()
			}
		}
	})

	filtered := vm.filteredConnections()

	// 2. 状态摘要行
	ui.Row(c).AlignItems(ui.Center).Padding(0, 2).Children(func() {
		ui.Text(c, fmt.Sprintf("活跃连接 %d 条（显示 %d 条）· 右侧可一键加规则并热生效",
			len(vm.connections), len(filtered))).
			FontSize(11).TextColor(p.textMuted)
	})

	// 3. 连接列表
	ui.Scroll(c.Key("conns-scroll")).Grow(1).Children(func() {
		ui.Column(c).Gap(5).Padding(0, 2, 6, 0).Children(func() {
			if len(filtered) == 0 {
				ui.Column(c).Padding(28, 16).Center().Gap(6).Children(func() {
					ui.Text(c, "当前没有活跃连接").FontSize(13).Bold().TextColor(p.textSecondary)
					ui.Text(c, "当浏览器或终端通过本地代理访问网络时，会在此实时显示").FontSize(11.5).TextColor(p.textMuted)
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
		ui.Column(c).Grow(1).Gap(2.5).Children(func() {
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
					renderMiniTag(c, conn.ProcessName, p.textSecondary, p.pillBg)
				}
				traffic := fmt.Sprintf("↑%s ↓%s", FormatBytes(conn.UploadTrafficTotal), FormatBytes(conn.DownloadTrafficTotal))
				ui.Text(c, traffic).FontSize(10).TextColor(p.textMuted).NoWrap()
			})
		})

		// 右侧快捷操作：一键加直连 / 一键加代理 / 断开连接
		if conn.TargetAddr != "" {
			if ui.Button(c.Key("add-direct"), "直连").Padding(3, 7).Radius(6).
				TextColor(p.success).Tooltip("将该目标加入自定义直连规则并重连").Clicked() {
				vm.quickAddRuleFromConn(conn, 0)
			}
			if ui.Button(c.Key("add-proxy"), "代理").Padding(3, 7).Radius(6).
				TextColor(p.accent).Tooltip("将该目标加入自定义代理规则并重连").Clicked() {
				vm.quickAddRuleFromConn(conn, 2)
			}
		}
		if ui.Button(c.Key("close"), "×").Padding(3, 7).Radius(6).
			TextColor(p.danger).Tooltip("立即断开此连接").Clicked() {
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
	}, fmt.Sprintf("已添加 %s -> %s 并刷新连接", domain, config.RuleActionLabel(action)))
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
