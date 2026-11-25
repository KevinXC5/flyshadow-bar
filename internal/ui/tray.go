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
	lastToolTip    string
	lastConnected  bool
	lastSpeedKey   string
	lastSpeedDraw  time.Time
	nowFn          func() time.Time
	hasInitialized bool
	menuOpen       bool
	iconConnected  []byte
	iconIdle       []byte
}

// usesOnDemandContextMenu 判断当前平台是否应保持托盘常驻 Menu 为 nil，
// 并在 OnRightClick 回调中按需弹出菜单：
// - macOS：常驻 Menu 会拦截左键点击，导致无法触发 OnClick 弹出主窗口；
// - Windows：按需在右键时弹出并卸载 Menu，可避免菜单打开期间被后台定时刷新触发 SetMenuItemInfoW
//   引发 Windows 11 亚克力自绘菜单单帧透明闪烁，同时保持左键弹主窗口、右键弹菜单的一致交互。
func usesOnDemandContextMenu(goos string) bool {
	return goos == "darwin" || goos == "windows"
}

// buildsFreshMenuOnRightClick 判断是否在右键弹出时按最新快照即时构建全新菜单，
// 不复用常驻菜单对象也不在后台对其调用 SetLabel/SetChecked，从而彻底避免异步 postMain
// 队列中的旧 UpdateMenuItem 落到刚打开或正打开的 Win32 亚克力菜单上造成闪烁。
func buildsFreshMenuOnRightClick(goos string) bool {
	return goos == "windows"
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
	if !buildsFreshMenuOnRightClick(runtime.GOOS) {
		tc.menu = tc.buildMenu(snap)
		tc.lastNodeSig = tc.menuSignature(snap)
	}
	tc.lastConnected = snap.State == engine.StateConnected
	tc.lastToolTip = trayTooltip(snap)
	tc.hasInitialized = true

	icon := iconIdle
	if tc.lastConnected {
		icon = iconConn
	}

	// macOS 与 Windows 均保持常驻 Menu 为 nil，左键弹/收主窗口，右键（macOS 亦支持 Control+点击）
	// 按需弹出快捷菜单；Linux 等需要常驻托盘菜单的平台直接绑定 tc.menu。
	var attachedMenu *mygo.Menu
	if !usesOnDemandContextMenu(runtime.GOOS) {
		attachedMenu = tc.menu
	}

	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:              "flyshadow-bar-main",
		Icon:            icon,
		IconIsTemplate:  true,
		MonospacedTitle: true,
		ToolTip:         tc.lastToolTip,
		Menu:            attachedMenu,
	})
	if err != nil {
		return nil, err
	}
	tc.tray = tray
	tc.tray.OnClick(func() {
		tc.app.toggleWindowFromTray()
	})
	if usesOnDemandContextMenu(runtime.GOOS) {
		tc.tray.OnRightClick(func() {
			tc.popUpContextMenu()
		})
	}
	tc.syncDarwinVisual(snap)
	return tc, nil
}

// popUpContextMenu 在右键（macOS 亦支持 Control+点击）托盘图标时按需弹出快捷菜单，
// 关闭菜单后立即卸载 Menu，确保左键点击始终弹出主窗口，并避免 Windows 弹出期间收到菜单项更新而闪烁。
func (tc *trayController) popUpContextMenu() {
	if tc == nil || tc.tray == nil || tc.menuOpen {
		return
	}
	tc.menuOpen = true
	defer func() {
		tc.menuOpen = false
	}()

	menu := tc.menu
	if buildsFreshMenuOnRightClick(runtime.GOOS) && tc.app != nil && tc.app.eng != nil {
		menu = tc.buildMenu(tc.app.eng.Snapshot())
	}
	if menu == nil {
		return
	}
	tc.tray.SetMenu(menu)
	tc.tray.PopUpMenu()
	tc.tray.SetMenu(nil)
	runtime.KeepAlive(menu)
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

	if tip := trayTooltip(snap); tip != tc.lastToolTip {
		tc.lastToolTip = tip
		tc.tray.SetToolTip(tip)
	}

	// Windows 托盘菜单在右键弹出时即时按最新快照构建，后台定时快照不触碰菜单项，
	// 既省去每秒无谓的 postMain 派发，又杜绝 Windows 11 亚克力菜单在打开期间闪烁。
	if buildsFreshMenuOnRightClick(runtime.GOOS) {
		return
	}

	// 若节点列表或更新提示发生变化，重建菜单；否则按需增量更新避免打断用户浏览菜单
	sig := tc.menuSignature(snap)
	if sig != tc.lastNodeSig {
		tc.lastNodeSig = sig
		tc.menu = tc.buildMenu(snap)
		if !usesOnDemandContextMenu(runtime.GOOS) {
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

// patchMenuInPlaceForOS 按需对比并仅写回发生变化的菜单项属性，避免无变化的定时快照
// 反复调用 mygo MenuItem.Set* 触发不必要的底层原生菜单重绘；返回值表示本次实际写入次数。
func (tc *trayController) patchMenuInPlaceForOS(snap engine.Snapshot, goos string) int {
	if tc.menu == nil {
		return 0
	}
	changes := 0
	if item := tc.menu.ItemByID("status-header"); item != nil {
		changes += setMenuItemLabel(item, statusHeaderLabel(snap))
		if goos == "darwin" {
			changes += setMenuItemSubtitle(item, statusHeaderSubtitle(snap))
		} else {
			changes += setMenuItemSubtitle(item, "")
		}
	}
	if item := tc.menu.ItemByID("status-detail"); item != nil {
		changes += setMenuItemLabel(item, statusHeaderSubtitle(snap))
	}
	if item := tc.menu.ItemByID("toggle-window"); item != nil {
		if tc.app != nil && tc.app.isWindowVisible() {
			changes += setMenuItemLabel(item, "隐藏主窗口")
		} else {
			changes += setMenuItemLabel(item, "打开主窗口")
		}
	}
	// Radio 组先选中目标项（mygo 会自动取消同组其他项的勾选），避免单次切换触发多轮原生重绘。
	modeItems := [...]struct {
		id   string
		mode core.ProxyMode
	}{
		{"mode-rule", core.ProxyModeRule},
		{"mode-global", core.ProxyModeGlobal},
		{"mode-direct", core.ProxyModeDirect},
	}
	for _, m := range modeItems {
		if snap.ModeCode == m.mode {
			if item := tc.menu.ItemByID(m.id); item != nil {
				changes += setMenuItemChecked(item, true)
			}
		}
	}
	for _, m := range modeItems {
		if snap.ModeCode != m.mode {
			if item := tc.menu.ItemByID(m.id); item != nil {
				changes += setMenuItemChecked(item, false)
			}
		}
	}
	if item := tc.menu.ItemByID("toggle-sysproxy"); item != nil {
		changes += setMenuItemChecked(item, snap.SystemProxy)
	}
	if item := tc.menu.ItemByID("toggle-udp"); item != nil {
		changes += setMenuItemChecked(item, snap.UDPAccelerationEnabled)
	}
	if item := tc.menu.ItemByID("toggle-native-udp"); item != nil {
		changes += setMenuItemChecked(item, snap.NativeUDP)
	}
	if item := tc.menu.ItemByID("action-ping"); item != nil {
		if snap.Pinging {
			changes += setMenuItemLabel(item, fmt.Sprintf("测速中 (%d/%d)…", snap.PingCompleted, snap.PingTotal))
			changes += setMenuItemEnabled(item, false)
		} else {
			changes += setMenuItemLabel(item, "节点测速")
			changes += setMenuItemEnabled(item, true)
		}
	}
	if item := tc.menu.ItemByID("action-sync"); item != nil {
		if snap.Syncing {
			changes += setMenuItemLabel(item, "正在同步订阅…")
			changes += setMenuItemEnabled(item, false)
		} else {
			changes += setMenuItemLabel(item, "同步订阅与规则")
			changes += setMenuItemEnabled(item, snap.HasCloudAuth)
		}
	}
	if item := tc.menu.ItemByID("nodes-submenu"); item != nil {
		changes += setMenuItemLabel(item, nodeSubmenuLabel(snap))
	}

	for _, n := range snap.Nodes {
		id := fmt.Sprintf("node-%d", n.ID)
		if item := tc.menu.ItemByID(id); item != nil {
			changes += setMenuItemChecked(item, n.Selected)
			changes += setMenuItemBadge(item, FormatDelay(n.DelayMs, n.Testing))
			badgeColor := "#94a3b8"
			if n.DelayMs > 0 {
				badgeColor = DelayBadgeColor(n.DelayMs)
			} else if n.DelayMs < 0 {
				badgeColor = "#ef4444"
			}
			changes += setMenuItemBadgeTextColor(item, badgeColor)
		}
	}
	return changes
}

func setMenuItemLabel(item *mygo.MenuItem, label string) int {
	if item.Label == label {
		return 0
	}
	item.SetLabel(label)
	return 1
}

func setMenuItemSubtitle(item *mygo.MenuItem, subtitle string) int {
	if item.Subtitle == subtitle {
		return 0
	}
	item.SetSubtitle(subtitle)
	return 1
}

func setMenuItemChecked(item *mygo.MenuItem, checked bool) int {
	if item.IsChecked() == checked {
		return 0
	}
	item.SetChecked(checked)
	return 1
}

func setMenuItemEnabled(item *mygo.MenuItem, enabled bool) int {
	if item.IsEnabled() == enabled {
		return 0
	}
	item.SetEnabled(enabled)
	return 1
}

func setMenuItemBadge(item *mygo.MenuItem, badge string) int {
	if item.Badge == badge {
		return 0
	}
	item.SetBadge(badge)
	return 1
}

func setMenuItemBadgeTextColor(item *mygo.MenuItem, color string) int {
	if item.BadgeTextColor == color {
		return 0
	}
	item.SetBadgeTextColor(color)
	return 1
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
	syncLabel := "同步订阅与规则"
	if snap.Syncing {
		syncLabel = "正在同步订阅…"
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
			Label:    syncLabel,
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
			readyVer := st.InstalledVersion
			if readyVer == "" {
				readyVer = st.Available
			}
			items = append(items, &mygo.MenuItem{
				ID:    "action-update-restart",
				Label: fmt.Sprintf("重启更新至 %s", readyVer),
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
		fmt.Fprintf(&b, "|u:%s:%s:%t", st.InstalledVersion, st.Available, st.Installed)
	}
	return b.String()
}
