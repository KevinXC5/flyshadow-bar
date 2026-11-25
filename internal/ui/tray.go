package ui

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
)

const (
	// traySpeedRefreshInterval 控制菜单栏上下行速率的刷新节奏（每 3 秒更新一次，避免每秒跳动干扰视线）。
	traySpeedRefreshInterval = 3 * time.Second
	// traySpeedRefreshTolerance 为 1 秒定时器抖动留出 100ms 容差，确保第 3 秒采样稳定命中而非滑落到第 4 秒。
	traySpeedRefreshTolerance = 100 * time.Millisecond

	// 托盘菜单视觉列宽上限（ASCII 计 1 列、宽字符计 2 列），避免 Windows 单行菜单被长节点名或错误文本撑宽。
	trayMenuNodeNameMaxCols = 22
	trayMenuSubNodeMaxCols  = 30
	trayMenuErrorMaxCols    = 26
)

// trayController 负责菜单栏 / 系统托盘的构建、状态刷新与快捷控制。
type trayController struct {
	app            *App
	tray           *mygo.Tray
	menu           *mygo.Menu
	lastNodeSig    string
	lastConnected  bool
	lastSpeedKey   string
	lastSpeedDraw  time.Time
	nowFn          func() time.Time
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

	// macOS 上若给 Tray 设置常驻 Menu，左键会直接弹菜单而无法触发 OnClick；
	// 因此 macOS 保持 Menu 为 nil，左键弹/收主窗口，右键（或 Control+点击）临时弹出快捷菜单。
	// Windows 后端在左键始终触发 OnClick、右键自动弹出 Menu，双端交互完全一致。
	var attachedMenu *mygo.Menu
	if runtime.GOOS != "darwin" {
		attachedMenu = tc.menu
	}

	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:              "flyshadow-bar-main",
		Icon:            icon,
		IconIsTemplate:  true,
		MonospacedTitle: true,
		ToolTip:         trayTooltip(snap),
		Menu:            attachedMenu,
	})
	if err != nil {
		return nil, err
	}
	tc.tray = tray
	tc.tray.OnClick(func() {
		tc.app.toggleWindowFromTray()
	})
	if runtime.GOOS == "darwin" {
		tc.tray.OnRightClick(func() {
			tc.popUpContextMenu()
		})
	}
	tc.syncDarwinVisual(snap)
	return tc, nil
}

// popUpContextMenu 在 macOS 右键（或 Control+点击）菜单栏图标时弹出快捷菜单，
// 关闭菜单后立即卸载 Menu，确保下次左键点击依然弹出主窗口。
func (tc *trayController) popUpContextMenu() {
	if tc == nil || tc.tray == nil || tc.menu == nil {
		return
	}
	tc.tray.SetMenu(tc.menu)
	tc.tray.PopUpMenu()
	tc.tray.SetMenu(nil)
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
	if runtime.GOOS != "darwin" {
		if !tc.hasInitialized || connected != tc.lastConnected {
			tc.lastConnected = connected
			if connected {
				tc.tray.SetIcon(tc.iconConnected, true)
			} else {
				tc.tray.SetIcon(tc.iconIdle, true)
			}
		}
	} else {
		tc.syncDarwinVisual(snap)
	}

	tc.tray.SetToolTip(trayTooltip(snap))

	// 若节点列表或更新提示发生变化，重建菜单；否则增量更新避免打断用户浏览菜单
	sig := tc.menuSignature(snap)
	if sig != tc.lastNodeSig {
		tc.lastNodeSig = sig
		tc.menu = tc.buildMenu(snap)
		// macOS 保持常驻 Menu 为 nil（右键按需弹出），仅在非 macOS 平台同步绑定菜单
		if runtime.GOOS != "darwin" {
			tc.tray.SetMenu(tc.menu)
		}
		return
	}

	tc.patchMenuInPlace(snap)
}

// syncDarwinVisual 统一管理 macOS 菜单栏的纯黑模板图标与双行紧凑网速渲染：
// 开启「菜单栏显示网速」且已连接时，将飞标与上下两行速率合成为一张窄幅纯黑模板图，
// 避免单行标题占用过多菜单栏空间，并按 3 秒周期节流更新；关闭或断开时立即恢复标准单图标。
func (tc *trayController) syncDarwinVisual(snap engine.Snapshot) {
	if runtime.GOOS != "darwin" || tc.tray == nil {
		return
	}
	connected := snap.State == engine.StateConnected
	showSpeed := tc.app.prefs.ShowTraySpeed && connected
	if !showSpeed {
		if tc.lastSpeedKey != "" || !tc.lastSpeedDraw.IsZero() || !tc.hasInitialized || connected != tc.lastConnected {
			tc.lastSpeedKey = ""
			tc.lastSpeedDraw = time.Time{}
			tc.lastConnected = connected
			tc.tray.SetTitle("")
			if connected {
				_ = tc.tray.SetIcon(tc.iconConnected, true)
			} else {
				_ = tc.tray.SetIcon(tc.iconIdle, true)
			}
		}
		return
	}

	speedKey := FormatTraySpeed(snap.UploadSpeedBps) + "|" + FormatTraySpeed(snap.DownloadSpeedBps)
	now := tc.now()
	if !tc.shouldRedrawTraySpeed(connected, speedKey, now) {
		return
	}
	tc.lastSpeedKey = speedKey
	tc.lastSpeedDraw = now
	tc.lastConnected = connected
	tc.tray.SetTitle("")
	_ = tc.tray.SetIcon(makeDarwinTraySpeedIconPNG(connected, snap.UploadSpeedBps, snap.DownloadSpeedBps), true)
}

func (tc *trayController) now() time.Time {
	if tc != nil && tc.nowFn != nil {
		return tc.nowFn()
	}
	return time.Now()
}

// shouldRedrawTraySpeed 判断当前是否应刷新菜单栏网速模板图：
// 首次展示或连接状态刚切换时立即绘制，稳态下至少间隔 3 秒（含 100ms 定时抖动容差）且数值变化才重绘。
func (tc *trayController) shouldRedrawTraySpeed(connected bool, speedKey string, now time.Time) bool {
	if tc.lastSpeedKey == "" || tc.lastSpeedDraw.IsZero() || connected != tc.lastConnected {
		return true
	}
	if now.Sub(tc.lastSpeedDraw) < traySpeedRefreshInterval-traySpeedRefreshTolerance {
		return false
	}
	return speedKey != tc.lastSpeedKey
}

func (tc *trayController) patchMenuInPlace(snap engine.Snapshot) {
	tc.patchMenuInPlaceForOS(snap, runtime.GOOS)
}

func (tc *trayController) patchMenuInPlaceForOS(snap engine.Snapshot, goos string) {
	if tc.menu == nil {
		return
	}
	if item := tc.menu.ItemByID("status-header"); item != nil {
		item.SetLabel(statusHeaderLabel(snap))
		if goos == "darwin" {
			item.SetSubtitle(statusHeaderSubtitle(snap))
		} else {
			item.SetSubtitle("")
		}
	}
	if item := tc.menu.ItemByID("status-detail"); item != nil {
		item.SetLabel(statusHeaderSubtitle(snap))
	}
	if item := tc.menu.ItemByID("toggle-window"); item != nil {
		if tc.app != nil && tc.app.isWindowVisible() {
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
			item.SetLabel(fmt.Sprintf("测速中 (%d/%d)…", snap.PingCompleted, snap.PingTotal))
			item.SetEnabled(false)
		} else {
			item.SetLabel("节点测速")
			item.SetEnabled(true)
		}
	}
	if item := tc.menu.ItemByID("action-sync"); item != nil {
		if snap.Syncing {
			item.SetLabel("正在同步订阅…")
			item.SetEnabled(false)
		} else {
			item.SetLabel("同步订阅与规则")
			item.SetEnabled(snap.HasCloudAuth)
		}
	}
	if item := tc.menu.ItemByID("nodes-submenu"); item != nil {
		item.SetLabel(nodeSubmenuLabel(snap))
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
	return tc.buildMenuForOS(snap, runtime.GOOS)
}

func (tc *trayController) buildMenuForOS(snap engine.Snapshot, goos string) *mygo.Menu {
	winLabel := "打开主窗口"
	if tc.app != nil && tc.app.isWindowVisible() {
		winLabel = "隐藏主窗口"
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
		displayName := node.DisplayName
		if goos != "darwin" {
			displayName = truncateMenuText(displayName, trayMenuSubNodeMaxCols)
		}
		label := displayName
		if node.Starred {
			label = "★ " + label
		}
		// 仅 macOS 菜单原生支持两行副标题；Windows 后端会将 Subtitle 用 " — " 拼接到同一行，
		// 因此非 macOS 端省略协议副标题，避免节点子菜单被拉宽。
		subtitle := ""
		if goos == "darwin" {
			var tags []string
			if node.EnableQuic {
				tags = append(tags, "QUIC")
			}
			if node.NativeUDP {
				tags = append(tags, "UDP")
			}
			subtitle = strings.Join(tags, " · ")
		}

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
			Label:    "暂无节点",
			Disabled: true,
		})
	}

	pingLabel := "节点测速"
	if snap.Pinging {
		pingLabel = fmt.Sprintf("测速中 (%d/%d)…", snap.PingCompleted, snap.PingTotal)
	}

	// macOS 14+ 原生支持两行副标题；Windows 等平台会将 Label 与 Subtitle 拼成一行，
	// 因此在非 macOS 平台拆为两条紧凑禁用项，防止顶部状态把整个托盘菜单横向撑宽。
	items := make([]*mygo.MenuItem, 0, 20)
	if goos == "darwin" {
		items = append(items, &mygo.MenuItem{
			ID:       "status-header",
			Label:    statusHeaderLabel(snap),
			Subtitle: statusHeaderSubtitle(snap),
			Disabled: true,
		})
	} else {
		items = append(items,
			&mygo.MenuItem{
				ID:       "status-header",
				Label:    statusHeaderLabel(snap),
				Disabled: true,
			},
			&mygo.MenuItem{
				ID:       "status-detail",
				Label:    statusHeaderSubtitle(snap),
				Disabled: true,
			},
		)
	}

	items = append(items,
		&mygo.MenuItem{
			ID:    "toggle-window",
			Label: winLabel,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				tc.app.toggleWindow()
			},
		},
		mygo.Separator(),
		&mygo.MenuItem{
			ID:       "mode-rule",
			Label:    "规则模式",
			Type:     mygo.MenuItemRadio,
			Checked:  snap.ModeCode == core.ProxyModeRule,
			KeepOpen: true,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _ = tc.app.eng.SetMode(core.ProxyModeRule) }()
			},
		},
		&mygo.MenuItem{
			ID:       "mode-global",
			Label:    "全局模式",
			Type:     mygo.MenuItemRadio,
			Checked:  snap.ModeCode == core.ProxyModeGlobal,
			KeepOpen: true,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _ = tc.app.eng.SetMode(core.ProxyModeGlobal) }()
			},
		},
		&mygo.MenuItem{
			ID:       "mode-direct",
			Label:    "直连模式",
			Type:     mygo.MenuItemRadio,
			Checked:  snap.ModeCode == core.ProxyModeDirect,
			KeepOpen: true,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _ = tc.app.eng.SetMode(core.ProxyModeDirect) }()
			},
		},
		mygo.Separator(),
		&mygo.MenuItem{
			ID:      "nodes-submenu",
			Label:   nodeSubmenuLabel(snap),
			Submenu: nodeItems,
		},
		mygo.Separator(),
		&mygo.MenuItem{
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
		&mygo.MenuItem{
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
		&mygo.MenuItem{
			ID:       "toggle-native-udp",
			Label:    "原生 UDP",
			Type:     mygo.MenuItemCheckbox,
			Checked:  snap.NativeUDP,
			KeepOpen: true,
			Click: func(item *mygo.MenuItem, _ *mygo.Window) {
				next := item.Checked
				go func() { _ = tc.app.eng.SetNativeUDP(next) }()
			},
		},
		mygo.Separator(),
		&mygo.MenuItem{
			ID:       "action-ping",
			Label:    pingLabel,
			Disabled: snap.Pinging,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _, _ = tc.app.eng.PingAll(context.Background()) }()
			},
		},
		&mygo.MenuItem{
			ID:       "action-sync",
			Label:    "同步订阅与规则",
			Disabled: snap.Syncing || !snap.HasCloudAuth,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _, _ = tc.app.eng.SyncSubscription(context.Background()) }()
			},
		},
		&mygo.MenuItem{
			ID:    "action-reload",
			Label: "重载配置",
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() { _ = tc.app.eng.Reload() }()
			},
		},
	)

	if tc.app != nil && tc.app.updates != nil {
		st := tc.app.updates.Status()
		if st.Installed {
			items = append(items, &mygo.MenuItem{
				ID:    "action-update-restart",
				Label: fmt.Sprintf("重启更新至 %s", st.Available),
				Click: func(*mygo.MenuItem, *mygo.Window) {
					_ = tc.app.updates.Restart()
				},
			})
		} else if st.Available != "" {
			items = append(items, &mygo.MenuItem{
				ID:    "action-update-open",
				Label: fmt.Sprintf("有新版本 %s", st.Available),
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
	if snap.ProxyPort > 0 && snap.State != engine.StateError {
		return fmt.Sprintf("%s %s · 127.0.0.1:%d", dot, StateLabel(snap.State), snap.ProxyPort)
	}
	return fmt.Sprintf("%s %s", dot, StateLabel(snap.State))
}

func statusHeaderSubtitle(snap engine.Snapshot) string {
	if snap.State == engine.StateError && snap.LastError != "" {
		return truncateMenuText(snap.LastError, trayMenuErrorMaxCols)
	}
	return fmt.Sprintf("↑ %s  ↓ %s",
		FormatSpeed(snap.UploadSpeedBps),
		FormatSpeed(snap.DownloadSpeedBps),
	)
}

func nodeSubmenuLabel(snap engine.Snapshot) string {
	if snap.CurrentNode == nil {
		return "选择节点"
	}
	return "节点: " + truncateMenuText(snap.CurrentNode.DisplayName, trayMenuNodeNameMaxCols)
}

// truncateMenuText 按视觉列宽截断菜单文案（ASCII 按 1 列、宽字符按 2 列），避免单行菜单项过宽。
func truncateMenuText(s string, maxCols int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	if maxCols <= 0 || menuDisplayCols(s) <= maxCols {
		return s
	}
	limit := maxCols - 1 // 为末尾省略号留出 1 列
	if limit < 1 {
		return "…"
	}
	var b strings.Builder
	cols := 0
	for _, r := range s {
		w := runeMenuCols(r)
		if cols+w > limit {
			break
		}
		b.WriteRune(r)
		cols += w
	}
	return strings.TrimRight(b.String(), " ") + "…"
}

func menuDisplayCols(s string) int {
	cols := 0
	for _, r := range s {
		cols += runeMenuCols(r)
	}
	return cols
}

func runeMenuCols(r rune) int {
	// CJK 统一表意文字、全角标点与假名/韩文等宽字符按 2 列计算，其余按 1 列计算。
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE10 && r <= 0xFE19,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6:
		return 2
	default:
		return 1
	}
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
