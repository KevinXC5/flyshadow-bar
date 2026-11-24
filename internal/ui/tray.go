package ui

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/egoist/mygo"

	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
)

// trayController 负责菜单栏 / 系统托盘的构建、状态刷新与快捷控制。
type trayController struct {
	app            *App
	tray           *mygo.Tray
	menu           *mygo.Menu
	lastNodeSig    string
	lastConnected  bool
	hasInitialized bool
	iconConnected  []byte
	iconIdle       []byte
}

func newTrayController(app *App) (*trayController, error) {
	iconConn := makeTrayIconPNG(true)
	iconIdle := makeTrayIconPNG(false)

	tc := &trayController{
		app:           app,
		iconConnected: iconConn,
		iconIdle:      iconIdle,
	}

	snap := app.eng.Snapshot()
	tc.menu = tc.buildMenu(snap)
	tc.lastNodeSig = tc.menuSignature(snap)
	tc.lastConnected = snap.State == engine.StateConnected
	tc.hasInitialized = true

	icon := iconIdle
	if tc.lastConnected {
		icon = iconConn
	}

	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:              "flyshadow-bar-main",
		Icon:            icon,
		IconIsTemplate:  runtime.GOOS == "darwin",
		MonospacedTitle: true,
		ToolTip:         trayTooltip(snap),
		Menu:            tc.menu,
	})
	if err != nil {
		return nil, err
	}
	tc.tray = tray
	tc.syncTitle(snap)
	return tc, nil
}

func (tc *trayController) Destroy() {
	if tc != nil && tc.tray != nil {
		tc.tray.Destroy()
	}
}

// Update 在主线程根据最新快照刷新托盘图标、速率标题和菜单状态。
func (tc *trayController) Update(snap engine.Snapshot) {
	if tc == nil || tc.tray == nil {
		return
	}

	connected := snap.State == engine.StateConnected
	if !tc.hasInitialized || connected != tc.lastConnected {
		tc.lastConnected = connected
		if connected {
			tc.tray.SetIcon(tc.iconConnected, runtime.GOOS == "darwin")
		} else {
			tc.tray.SetIcon(tc.iconIdle, runtime.GOOS == "darwin")
		}
	}

	tc.tray.SetToolTip(trayTooltip(snap))
	tc.syncTitle(snap)

	// 若节点列表或更新提示发生变化，重建菜单；否则增量更新避免打断用户浏览菜单
	sig := tc.menuSignature(snap)
	if sig != tc.lastNodeSig {
		tc.lastNodeSig = sig
		tc.menu = tc.buildMenu(snap)
		tc.tray.SetMenu(tc.menu)
		return
	}

	tc.patchMenuInPlace(snap)
}

func (tc *trayController) syncTitle(snap engine.Snapshot) {
	if runtime.GOOS != "darwin" {
		return
	}
	if !tc.app.prefs.ShowTraySpeed || snap.State != engine.StateConnected {
		tc.tray.SetTitle("")
		return
	}
	tc.tray.SetTitle("↓" + FormatCompactSpeed(snap.DownloadSpeedBps) + " ↑" + FormatCompactSpeed(snap.UploadSpeedBps))
}

func (tc *trayController) patchMenuInPlace(snap engine.Snapshot) {
	if tc.menu == nil {
		return
	}
	if item := tc.menu.ItemByID("status-header"); item != nil {
		item.SetLabel(statusHeaderLabel(snap))
		item.SetSubtitle(statusHeaderSubtitle(snap))
	}
	if item := tc.menu.ItemByID("toggle-window"); item != nil {
		if tc.app.isWindowVisible() {
			item.SetLabel("隐藏主窗口")
		} else {
			item.SetLabel("打开主窗口")
		}
	}
	if item := tc.menu.ItemByID("mode-rule"); item != nil {
		item.SetChecked(snap.ModeCode == core.ProxyModeRule)
	}
	if item := tc.menu.ItemByID("mode-global"); item != nil {
		item.SetChecked(snap.ModeCode == core.ProxyModeGlobal)
	}
	if item := tc.menu.ItemByID("mode-direct"); item != nil {
		item.SetChecked(snap.ModeCode == core.ProxyModeDirect)
	}
	if item := tc.menu.ItemByID("toggle-sysproxy"); item != nil {
		item.SetChecked(snap.SystemProxy)
	}
	if item := tc.menu.ItemByID("toggle-udp"); item != nil {
		item.SetChecked(snap.UDPAccelerationEnabled)
	}
	if item := tc.menu.ItemByID("toggle-native-udp"); item != nil {
		item.SetChecked(snap.NativeUDP)
	}
	if item := tc.menu.ItemByID("action-ping"); item != nil {
		if snap.Pinging {
			item.SetLabel(fmt.Sprintf("正在测速 (%d/%d)…", snap.PingCompleted, snap.PingTotal))
			item.SetEnabled(false)
		} else {
			item.SetLabel("节点一键测速")
			item.SetEnabled(true)
		}
	}
	if item := tc.menu.ItemByID("nodes-submenu"); item != nil {
		label := "选择节点"
		if snap.CurrentNode != nil {
			label = "节点: " + snap.CurrentNode.DisplayName
		}
		item.SetLabel(label)
	}

	for _, n := range snap.Nodes {
		id := fmt.Sprintf("node-%d", n.ID)
		if item := tc.menu.ItemByID(id); item != nil {
			item.SetChecked(n.Selected)
			item.SetBadge(FormatDelay(n.DelayMs, n.Testing))
			if n.DelayMs > 0 {
				item.SetBadgeTextColor(DelayBadgeColor(n.DelayMs))
			} else if n.DelayMs < 0 {
				item.SetBadgeTextColor("#ef4444")
			} else {
				item.SetBadgeTextColor("#94a3b8")
			}
		}
	}
}

func (tc *trayController) buildMenu(snap engine.Snapshot) *mygo.Menu {
	winLabel := "打开主窗口"
	if tc.app.isWindowVisible() {
		winLabel = "隐藏主窗口"
	}

	nodeSubmenuLabel := "选择节点"
	if snap.CurrentNode != nil {
		nodeSubmenuLabel = "节点: " + snap.CurrentNode.DisplayName
	}

	// 节点菜单按「收藏优先 → 原序」排列，方便托盘快速点选常用节点
	orderedNodes := make([]engine.NodeSnapshot, len(snap.Nodes))
	copy(orderedNodes, snap.Nodes)
	sort.SliceStable(orderedNodes, func(i, j int) bool {
		if orderedNodes[i].Starred != orderedNodes[j].Starred {
			return orderedNodes[i].Starred
		}
		return false
	})

	nodeItems := make([]*mygo.MenuItem, 0, len(orderedNodes)+2)
	for _, n := range orderedNodes {
		node := n
		label := node.DisplayName
		if node.Starred {
			label = "★ " + label
		}
		var tags []string
		if node.EnableQuic {
			tags = append(tags, "QUIC")
		}
		if node.NativeUDP {
			tags = append(tags, "UDP")
		}
		subtitle := strings.Join(tags, " · ")

		badgeColor := "#94a3b8"
		if node.DelayMs > 0 {
			badgeColor = DelayBadgeColor(node.DelayMs)
		} else if node.DelayMs < 0 {
			badgeColor = "#ef4444"
		}

		nodeItems = append(nodeItems, &mygo.MenuItem{
			ID:             fmt.Sprintf("node-%d", node.ID),
			Label:          label,
			Subtitle:       subtitle,
			Badge:          FormatDelay(node.DelayMs, node.Testing),
			BadgeAlign:     mygo.BadgeAlignRight,
			BadgeTextColor: badgeColor,
			Type:           mygo.MenuItemCheckbox,
			Checked:        node.Selected,
			KeepOpen:       true,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func(id int) {
					_ = tc.app.eng.SelectNode(id)
				}(node.ID)
			},
		})
	}
	if len(nodeItems) == 0 {
		nodeItems = append(nodeItems, &mygo.MenuItem{
			Label:    "暂无可用节点",
			Disabled: true,
		})
	}

	pingLabel := "节点一键测速"
	if snap.Pinging {
		pingLabel = fmt.Sprintf("正在测速 (%d/%d)…", snap.PingCompleted, snap.PingTotal)
	}

	items := []*mygo.MenuItem{
		{
			ID:       "status-header",
			Label:    statusHeaderLabel(snap),
			Subtitle: statusHeaderSubtitle(snap),
			Disabled: true,
		},
		{
			ID:    "toggle-window",
			Label: winLabel,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				tc.app.toggleWindow()
			},
		},
		mygo.Separator(),
		{
			ID:       "mode-rule",
			Label:    "规则模式 (Rule)",
			Type:     mygo.MenuItemRadio,
			Checked:  snap.ModeCode == core.ProxyModeRule,
			KeepOpen: true,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _ = tc.app.eng.SetMode(core.ProxyModeRule) }()
			},
		},
		{
			ID:       "mode-global",
			Label:    "全局模式 (Global)",
			Type:     mygo.MenuItemRadio,
			Checked:  snap.ModeCode == core.ProxyModeGlobal,
			KeepOpen: true,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _ = tc.app.eng.SetMode(core.ProxyModeGlobal) }()
			},
		},
		{
			ID:       "mode-direct",
			Label:    "直连模式 (Direct)",
			Type:     mygo.MenuItemRadio,
			Checked:  snap.ModeCode == core.ProxyModeDirect,
			KeepOpen: true,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _ = tc.app.eng.SetMode(core.ProxyModeDirect) }()
			},
		},
		mygo.Separator(),
		{
			ID:      "nodes-submenu",
			Label:   nodeSubmenuLabel,
			Submenu: nodeItems,
		},
		mygo.Separator(),
		{
			ID:       "toggle-sysproxy",
			Label:    "系统代理",
			Type:     mygo.MenuItemCheckbox,
			Checked:  snap.SystemProxy,
			KeepOpen: true,
			Click: func(item *mygo.MenuItem, _ *mygo.Window) {
				next := item.Checked
				go func() { _ = tc.app.eng.SetSystemProxy(next) }()
			},
		},
		{
			ID:       "toggle-udp",
			Label:    "UDP 加速 (QUIC)",
			Type:     mygo.MenuItemCheckbox,
			Checked:  snap.UDPAccelerationEnabled,
			KeepOpen: true,
			Click: func(item *mygo.MenuItem, _ *mygo.Window) {
				next := item.Checked
				go func() { _ = tc.app.eng.SetUDPAcceleration(next) }()
			},
		},
		{
			ID:       "toggle-native-udp",
			Label:    "原生 UDP (Native UDP)",
			Type:     mygo.MenuItemCheckbox,
			Checked:  snap.NativeUDP,
			KeepOpen: true,
			Click: func(item *mygo.MenuItem, _ *mygo.Window) {
				next := item.Checked
				go func() { _ = tc.app.eng.SetNativeUDP(next) }()
			},
		},
		mygo.Separator(),
		{
			ID:       "action-ping",
			Label:    pingLabel,
			Disabled: snap.Pinging,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _, _ = tc.app.eng.PingAll(context.Background()) }()
			},
		},
		{
			ID:    "action-reload",
			Label: "重载配置",
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _ = tc.app.eng.Reload() }()
			},
		},
	}

	if tc.app != nil && tc.app.updates != nil {
		st := tc.app.updates.Status()
		if st.Installed {
			items = append(items, &mygo.MenuItem{
				ID:    "action-update-restart",
				Label: fmt.Sprintf("更新 %s 已就绪 · 立即重启", st.Available),
				Click: func(*mygo.MenuItem, *mygo.Window) {
					_ = tc.app.updates.Restart()
				},
			})
		} else if st.Available != "" {
			items = append(items, &mygo.MenuItem{
				ID:    "action-update-open",
				Label: fmt.Sprintf("发现新版本 %s（增量更新）", st.Available),
				Click: func(*mygo.MenuItem, *mygo.Window) {
					tc.app.openSettingsTab()
				},
			})
		}
	}

	items = append(items,
		mygo.Separator(),
		&mygo.MenuItem{
			ID:    "action-quit",
			Label: "退出 FlyShadow Bar",
			Click: func(*mygo.MenuItem, *mygo.Window) {
				mygo.App.Quit()
			},
		},
	)

	return mygo.NewMenu(items)
}

func statusHeaderLabel(snap engine.Snapshot) string {
	dot := "○"
	switch snap.State {
	case engine.StateConnected:
		dot = "●"
	case engine.StateConnecting:
		dot = "◐"
	case engine.StateError:
		dot = "✕"
	}
	nodeName := "未选择节点"
	if snap.CurrentNode != nil {
		nodeName = snap.CurrentNode.DisplayName
	}
	return fmt.Sprintf("%s %s · %s", dot, StateLabel(snap.State), nodeName)
}

func statusHeaderSubtitle(snap engine.Snapshot) string {
	if snap.State == engine.StateError && snap.LastError != "" {
		return snap.LastError
	}
	return fmt.Sprintf("127.0.0.1:%d · ↑ %s  ↓ %s",
		snap.ProxyPort,
		FormatSpeed(snap.UploadSpeedBps),
		FormatSpeed(snap.DownloadSpeedBps),
	)
}

func trayTooltip(snap engine.Snapshot) string {
	nodeName := "未选择节点"
	if snap.CurrentNode != nil {
		nodeName = snap.CurrentNode.DisplayName
	}
	return fmt.Sprintf("FlyShadow Bar — %s | %s | %s",
		StateLabel(snap.State),
		snap.ModeCode.Label(),
		nodeName,
	)
}

func (tc *trayController) menuSignature(snap engine.Snapshot) string {
	var b strings.Builder
	for _, n := range snap.Nodes {
		fmt.Fprintf(&b, "%d:%s:%t;", n.ID, n.DisplayName, n.Starred)
	}
	if tc.app != nil && tc.app.updates != nil {
		st := tc.app.updates.Status()
		fmt.Fprintf(&b, "|u:%s:%t", st.Available, st.Installed)
	}
	return b.String()
}
