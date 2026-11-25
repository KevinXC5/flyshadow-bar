package ui

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
)

func TestMainViewRendersCleanly(t *testing.T) {
	snap := engine.Snapshot{
		AppVersion:             "0.1.0-test",
		CoreVersion:            "2.0.2",
		CoreLibPath:            "/mock/libflyshadow_core_lib.dylib",
		ConfigPath:             "/mock/config.json",
		GeoIPLoaded:            true,
		RuleCount:              1024,
		State:                  engine.StateConnected,
		Mode:                   "rule",
		ModeCode:               core.ProxyModeRule,
		ProxyPort:              7890,
		SystemProxy:            true,
		UDPAccelerationEnabled: true,
		NativeUDP:              false,
		EffectiveTransport: core.TransportDecision{
			EnableQuic:      true,
			EnableNativeUDP: false,
		},
		UploadSpeedBps:   12800,
		DownloadSpeedBps: 2048000,
		SpeedHistory: []engine.SpeedSample{
			{UploadBps: 4096, DownloadBps: 120000},
			{UploadBps: 8192, DownloadBps: 850000},
			{UploadBps: 12800, DownloadBps: 2048000},
		},
		HasCloudAuth: true,
		TunnelPingMs: 42,
		CurrentNode: &engine.NodeSnapshot{
			ID:          1,
			DisplayName: "香港 IEPL 01",
			EnableQuic:  true,
			NativeUDP:   true,
			Starred:     true,
			Selected:    true,
			DelayMs:     42,
		},
		Nodes: []engine.NodeSnapshot{
			{ID: 1, DisplayName: "香港 IEPL 01", EnableQuic: true, NativeUDP: true, Starred: true, Selected: true, DelayMs: 42},
			{ID: 2, DisplayName: "日本 东京 02", EnableQuic: true, NativeUDP: false, Starred: false, Selected: false, DelayMs: 168},
			{ID: 3, DisplayName: "美国 洛杉矶 03", EnableQuic: false, NativeUDP: true, Starred: false, Selected: false, DelayMs: -1},
		},
		CustomRules: []config.DomainRule{
			{ID: 1, Enable: config.IntPtr(1), Domain: "openai.com", Matching: config.MatchDomainSuffix, ProxyType: 2, Label: "ai"},
			{ID: 2, Enable: config.IntPtr(1), Domain: "192.168.0.0/16", Matching: config.MatchCIDR4, ProxyType: 0},
		},
		RuleLabels:       []string{config.DefaultRuleActiveSentinel, "ai"},
		ActiveRuleLabels: []string{config.DefaultRuleActiveSentinel, "ai"},
		Traffic: &config.UserTrafficInfo{
			TrafficSize:     500 * 1024 * 1024 * 1024,
			TrafficUpload:   12 * 1024 * 1024 * 1024,
			TrafficDownload: 88 * 1024 * 1024 * 1024,
			Level:           3,
			ExpireTime:      config.FlexMillis(1796610616000),
		},
	}

	vm := newViewModel(nil, snap)
	vm.connections = []core.ConnectionInfo{
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 54321, TargetAddr: "api.openai.com", TargetPort: 443, ProcessName: "curl", ProxyType: "Proxy", UploadTrafficTotal: 2048, DownloadTrafficTotal: 8192},
	}

	// 验证节点页渲染、无冗余计数与筛选交互
	tt := ui.NewTester(vm.render, 500, 780)
	if !tt.HasText("香港 IEPL 01") || !tt.HasText("日本 东京 02") {
		t.Fatalf("node list missing expected text, got: %v", tt.Texts())
	}
	if !tt.HasText("共 3 个节点") {
		t.Fatalf("unfiltered node summary should be concise, got: %v", tt.Texts())
	}
	if err := tt.Click("收藏"); err != nil {
		t.Fatalf("click 收藏: %v", err)
	}
	if !vm.onlyStarred || tt.HasText("日本 东京 02") || !tt.HasText("显示 1 / 3 个节点") {
		t.Fatalf("onlyStarred filter did not take effect: onlyStarred=%v, texts=%v", vm.onlyStarred, tt.Texts())
	}

	// 验证点击流量卡上的订阅密码图标可打开模态弹窗，面板严格居中且左右对称，点击取消可关闭
	if err := tt.Click("配置订阅密码"); err != nil {
		t.Fatalf("click 配置订阅密码: %v", err)
	}
	if !vm.subModalOpen || !tt.HasText("更换订阅密码") || !tt.HasText("保存并同步") {
		t.Fatalf("subscribe password modal did not open cleanly: open=%v texts=%v", vm.subModalOpen, tt.Texts())
	}
	if dlg, ok := tt.AccessByRole(ui.RoleDialog, ""); !ok {
		t.Fatalf("subscribe modal dialog role not found")
	} else {
		assertDialogCentered(t, "subscribe-modal", dlg.Bounds, 500, 780)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("click 取消: %v", err)
	}
	if vm.subModalOpen {
		t.Fatalf("subscribe password modal did not close after clicking 取消")
	}

	// 切换到规则页验证渲染
	if err := tt.Click("规则"); err != nil {
		t.Fatalf("click 规则: %v", err)
	}
	if vm.activeTab != 1 || !tt.HasText("openai.com") {
		t.Fatalf("rules tab did not render expected rule, activeTab=%d", vm.activeTab)
	}

	// 切换到连接页验证渲染与快捷添加规则提示
	if err := tt.Click("连接"); err != nil {
		t.Fatalf("click 连接: %v", err)
	}
	if vm.activeTab != 2 || !tt.HasText("api.openai.com:443") || !tt.HasText("可直接点击直连/代理按钮快速添加规则") {
		t.Fatalf("connections tab did not render expected connection/hint, activeTab=%d texts=%v", vm.activeTab, tt.Texts())
	}

	// 切换到设置页验证渲染（关于与更新不再重复展示订阅流量）
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click 设置: %v", err)
	}
	if vm.activeTab != 3 || !tt.HasText("代理端口") {
		t.Fatalf("settings tab did not render expected content, activeTab=%d", vm.activeTab)
	}
	if tt.HasText("订阅流量") {
		t.Fatalf("settings tab should not duplicate 订阅流量 row, texts=%v", tt.Texts())
	}

	// 验证 Markdown 更新说明渲染与更新下载进度条
	vm.updateNativeEnabled = true
	vm.updateAvailable = "0.2.6"
	vm.updateNotes = "# FlyShadow Bar v0.2.6 更新说明\n\n#### 新增功能\n- 新增 `--relaunch` 启动标志\n"
	vm.updateDownloading = true
	vm.updateDownloaded = 1536 * 1024
	vm.updateTotal = 3072 * 1024
	ttUpdating := ui.NewTester(vm.render, 500, 960)
	if !ttUpdating.HasText("正在下载 0.2.6") || !ttUpdating.HasText("50%") {
		t.Fatalf("update progress banner/section missing expected text, got: %v", ttUpdating.Texts())
	}
	if _, ok := ttUpdating.Access("更新下载进度"); !ok {
		t.Fatalf("expected accessible progress bar 更新下载进度")
	}
	if !ttUpdating.HasText("FlyShadow Bar v0.2.6 更新说明") || !ttUpdating.HasText("新增功能") {
		t.Fatalf("markdown release notes did not render expected headings, got: %v", ttUpdating.Texts())
	}
	if ttUpdating.HasText("#### 新增功能") || ttUpdating.HasText("# FlyShadow Bar") {
		t.Fatalf("raw markdown markers should not appear verbatim, got: %v", ttUpdating.Texts())
	}

	// 验证后台增量更新就绪后顶部醒目「立即重启」横幅渲染
	vm.updateDownloading = false
	vm.updateInstalled = true
	vm.updateAvailable = "0.2.6"
	ttReady := ui.NewTester(vm.render, 500, 780)
	if !ttReady.HasText("新版本 0.2.6 已就绪") || !ttReady.HasText("立即重启") {
		t.Fatalf("update ready banner missing expected text, got: %v", ttReady.Texts())
	}

	// 验证标题栏应用 Logo 位图解码有效
	if logo := appLogoBitmap(); logo == nil {
		t.Fatalf("appLogoBitmap() returned nil")
	}

	// 验证图标 PNG 生成有效
	pngConn := makeTrayIconPNG(true)
	pngIdle := makeTrayIconPNG(false)
	if len(pngConn) < 100 || len(pngIdle) < 100 {
		t.Fatalf("tray icon PNG too small: conn=%d idle=%d", len(pngConn), len(pngIdle))
	}
}

// TestDarwinTrayIconIsPureMonochromeTemplate 确保 macOS 与 Windows 托盘图标均为严格纯黑透明模板图
// （零彩色像素，双端均由系统/mygo 随浅/深状态栏自动切黑白），同时验证“已连接=宽尾实心 / 未连接=宽尾空心”。
func TestDarwinTrayIconIsPureMonochromeTemplate(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		var connVisible, idleVisible int
		for _, connected := range []bool{true, false} {
			raw := makeTrayIconPNGForOS(connected, goos)
			img, err := png.Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("decode %s tray icon (connected=%v): %v", goos, connected, err)
			}
			b := img.Bounds()
			if b.Dx() < 32 || b.Dy() < 32 {
				t.Fatalf("%s tray icon too small: %dx%d", goos, b.Dx(), b.Dy())
			}
			visiblePixels := 0
			var maxAlpha uint32
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					r, g, bl, a := img.At(x, y).RGBA()
					if a > 0 {
						visiblePixels++
						if a > maxAlpha {
							maxAlpha = a
						}
						if r != 0 || g != 0 || bl != 0 {
							t.Fatalf("%s tray icon (connected=%v) at (%d,%d) must be monochrome template (RGB=0), got RGBA=(%d,%d,%d,%d)",
								goos, connected, x, y, r, g, bl, a)
						}
					}
				}
			}
			if visiblePixels == 0 {
				t.Fatalf("%s tray icon (connected=%v) has no visible pixels", goos, connected)
			}
			// 无论已连接还是未连接，核心线条/填充都必须保持高不透明度，避免在深浅菜单栏/任务栏发淡
			if maxAlpha < 230*257 {
				t.Fatalf("%s tray icon (connected=%v) too faint: maxAlpha=%d", goos, connected, maxAlpha>>8)
			}

			// 尾翼内部点 (17,24)：已连接为实心填充、未连接为空心透明
			_, _, _, tailInnerA := img.At(17, 24).RGBA()
			if connected {
				connVisible = visiblePixels
				if tailInnerA < 240*257 {
					t.Fatalf("%s connected tray icon tail interior at (17,24) should be solid, alpha=%d", goos, tailInnerA>>8)
				}
			} else {
				idleVisible = visiblePixels
				if tailInnerA != 0 {
					t.Fatalf("%s disconnected tray icon tail interior at (17,24) should be hollow, alpha=%d", goos, tailInnerA>>8)
				}
			}
		}
		if connVisible <= idleVisible {
			t.Fatalf("%s connected solid icon should have more visible pixels than disconnected outline: conn=%d idle=%d", goos, connVisible, idleVisible)
		}
	}

	// 验证 macOS 双行网速菜单栏模板图：严格纯黑透明像素（RGB=0），且两次不同速率渲染出的像素有变化
	rawSpeed1 := makeDarwinTraySpeedIconPNG(true, 23*1024, 3686)
	rawSpeed2 := makeDarwinTraySpeedIconPNG(true, 1024*1024*5, 1024*1024*18)
	if bytes.Equal(rawSpeed1, rawSpeed2) {
		t.Fatalf("darwin speed tray icon did not redraw for updated speed values")
	}
	imgSpeed, err := png.Decode(bytes.NewReader(rawSpeed1))
	if err != nil {
		t.Fatalf("decode darwin speed tray icon: %v", err)
	}
	sb := imgSpeed.Bounds()
	if sb.Dy() != 36 || sb.Dx() <= 36 || sb.Dx() > 140 {
		t.Fatalf("unexpected darwin speed tray icon dimensions: %dx%d", sb.Dx(), sb.Dy())
	}
	speedVisible := 0
	for y := sb.Min.Y; y < sb.Max.Y; y++ {
		for x := sb.Min.X; x < sb.Max.X; x++ {
			r, g, bl, a := imgSpeed.At(x, y).RGBA()
			if a > 0 {
				speedVisible++
				if r != 0 || g != 0 || bl != 0 {
					t.Fatalf("darwin speed tray icon at (%d,%d) must be monochrome template (RGB=0), got RGBA=(%d,%d,%d,%d)",
						x, y, r, g, bl, a)
				}
			}
		}
	}
	if speedVisible == 0 {
		t.Fatalf("darwin speed tray icon has no visible pixels")
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := FormatSpeed(512); got != "512 B/s" {
		t.Fatalf("FormatSpeed(512) = %q", got)
	}
	if got := FormatSpeed(2048); got != "2.0 KB/s" {
		t.Fatalf("FormatSpeed(2048) = %q", got)
	}
	if got := FormatSpeed(3 * 1024 * 1024); got != "3.00 MB/s" {
		t.Fatalf("FormatSpeed(3MB) = %q", got)
	}
	if got := FormatTraySpeed(0); got != "0B" {
		t.Fatalf("FormatTraySpeed(0) = %q", got)
	}
	if got := FormatTraySpeed(3686); got != "3.6K" {
		t.Fatalf("FormatTraySpeed(3686) = %q", got)
	}
	if got := FormatTraySpeed(23 * 1024); got != "23K" {
		t.Fatalf("FormatTraySpeed(23K) = %q", got)
	}
	if got := FormatTraySpeed(5 * 1024 * 1024); got != "5.0M" {
		t.Fatalf("FormatTraySpeed(5M) = %q", got)
	}
	if got := FormatDelay(48, false); got != "48 ms" {
		t.Fatalf("FormatDelay(48) = %q", got)
	}
	if got := FormatDelay(-1, false); got != "超时" {
		t.Fatalf("FormatDelay(-1) = %q", got)
	}
}

func TestCalcTrayPopoverPos(t *testing.T) {
	work := mygo.Rectangle{X: 0, Y: 25, Width: 1440, Height: 875}

	// macOS：靠近右侧菜单栏图标，应夹紧在屏幕右边缘内并位于顶栏下方
	x, y, ok := calcTrayPopoverPos(mygo.Rectangle{X: 1320, Y: 0, Width: 28, Height: 25}, work, 500, 780, "darwin")
	if !ok {
		t.Fatalf("darwin popover pos should succeed")
	}
	if x != 1440-500-8 || y != 31 {
		t.Fatalf("darwin right-edge pos = (%d,%d), want (%d,%d)", x, y, 1440-500-8, 31)
	}

	// Windows：底部任务栏托盘图标上方弹出
	winWork := mygo.Rectangle{X: 0, Y: 0, Width: 1920, Height: 1032}
	x, y, ok = calcTrayPopoverPos(mygo.Rectangle{X: 1700, Y: 1032, Width: 32, Height: 48}, winWork, 500, 780, "windows")
	if !ok {
		t.Fatalf("windows popover pos should succeed")
	}
	if x != 1920-500-8 || y != 1032-780-8 {
		t.Fatalf("windows bottom-taskbar pos = (%d,%d), want (%d,%d)", x, y, 1920-500-8, 1032-780-8)
	}

	// 兜底：无有效托盘矩形时（例如 Windows 溢出折叠区），贴右下角工作区弹出
	x, y, ok = calcTrayPopoverPos(mygo.Rectangle{}, winWork, 500, 780, "windows")
	if !ok || x != 1920-500-8 || y != 1032-780-8 {
		t.Fatalf("windows fallback pos = (%d,%d,ok=%v)", x, y, ok)
	}
}

func TestConnectionSortingAndPopover(t *testing.T) {
	vm := newViewModel(nil, engine.Snapshot{
		State:     engine.StateConnected,
		ModeCode:  core.ProxyModeRule,
		ProxyPort: 7890,
	})
	vm.activeTab = 2
	vm.connections = []core.ConnectionInfo{
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50001, TargetAddr: "slow-old.example", TargetPort: 443, ProxyType: "Proxy", ActiveTime: 1000, DownloadTraffic: 50 * 1024, DownloadTrafficTotal: 900 * 1024},
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50002, TargetAddr: "fast-mid.example", TargetPort: 443, ProxyType: "Proxy", ActiveTime: 2000, DownloadTraffic: 5 * 1024 * 1024, DownloadTrafficTotal: 300 * 1024},
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50003, TargetAddr: "idle-new.example", TargetPort: 443, ProxyType: "Direct", ActiveTime: 3000, DownloadTraffic: 0, DownloadTrafficTotal: 100 * 1024},
	}

	// 默认：创建时间降序（新 -> 旧）
	got := vm.filteredConnections()
	if got[0].TargetAddr != "idle-new.example" || got[1].TargetAddr != "fast-mid.example" || got[2].TargetAddr != "slow-old.example" {
		t.Fatalf("default created-at desc order mismatch: %+v", got)
	}

	// 点击工具栏排序按钮打开弹窗，选择「下载速度降序」
	tt := ui.NewTester(vm.render, 500, 780)
	if err := tt.Click("排序"); err != nil {
		t.Fatalf("click 排序: %v", err)
	}
	if !vm.connSortMenuOpen || !tt.HasText("排序方式") || !tt.HasText("下载速度") || !tt.HasText("下载总量") || !tt.HasText("创建时间") {
		t.Fatalf("sort popover did not open with expected options: open=%v texts=%v", vm.connSortMenuOpen, tt.Texts())
	}
	if err := tt.Click("下载速度降序"); err != nil {
		t.Fatalf("click 下载速度降序: %v", err)
	}
	if vm.connSortMenuOpen || vm.connSortField != connSortByDownSpeed || vm.connSortAsc {
		t.Fatalf("expected down speed desc & menu closed, got field=%v asc=%v open=%v", vm.connSortField, vm.connSortAsc, vm.connSortMenuOpen)
	}
	got = vm.filteredConnections()
	if got[0].TargetAddr != "fast-mid.example" || got[1].TargetAddr != "slow-old.example" || got[2].TargetAddr != "idle-new.example" {
		t.Fatalf("download speed desc order mismatch: %+v", got)
	}
	// 按下载速度排序时页面应展示实时下载速率
	if !tt.HasText("↓5.00 MB/s") {
		t.Fatalf("expected live download speed label in connection row, texts=%v", tt.Texts())
	}

	// 再次打开弹窗选择「下载总量升序」
	if err := tt.Click("排序"); err != nil {
		t.Fatalf("reopen 排序: %v", err)
	}
	if err := tt.Click("下载总量升序"); err != nil {
		t.Fatalf("click 下载总量升序: %v", err)
	}
	got = vm.filteredConnections()
	if got[0].TargetAddr != "idle-new.example" || got[1].TargetAddr != "fast-mid.example" || got[2].TargetAddr != "slow-old.example" {
		t.Fatalf("download total asc order mismatch: %+v", got)
	}

	// 再次打开弹窗选择「创建时间升序」
	if err := tt.Click("排序"); err != nil {
		t.Fatalf("reopen 排序 2: %v", err)
	}
	if err := tt.Click("创建时间升序"); err != nil {
		t.Fatalf("click 创建时间升序: %v", err)
	}
	got = vm.filteredConnections()
	if got[0].TargetAddr != "slow-old.example" || got[1].TargetAddr != "fast-mid.example" || got[2].TargetAddr != "idle-new.example" {
		t.Fatalf("created-at asc order mismatch: %+v", got)
	}
}

func TestTraySpeedRefreshThrottledTo3Seconds(t *testing.T) {
	base := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	tc := &trayController{lastConnected: true}

	// 首次展示必须立即绘制
	if !tc.shouldRedrawTraySpeed(true, "10K|20K", base) {
		t.Fatalf("first tray speed draw must happen immediately")
	}
	tc.lastSpeedKey = "10K|20K"
	tc.lastSpeedDraw = base

	// 1 秒与 2 秒后即使速度变化也应跳过重绘
	if tc.shouldRedrawTraySpeed(true, "30K|80K", base.Add(1*time.Second)) {
		t.Fatalf("1s update should be throttled")
	}
	if tc.shouldRedrawTraySpeed(true, "50K|120K", base.Add(2*time.Second)) {
		t.Fatalf("2s update should be throttled")
	}

	// 第 3 秒（包含少量定时器提前抖动，例如 2950ms）应触发重绘
	if !tc.shouldRedrawTraySpeed(true, "50K|120K", base.Add(2950*time.Millisecond)) {
		t.Fatalf("3s update (within jitter tolerance) should redraw")
	}

	// 超过 3 秒但数值完全没变时不重复渲染
	if tc.shouldRedrawTraySpeed(true, "10K|20K", base.Add(4*time.Second)) {
		t.Fatalf("unchanged speed key should skip redraw")
	}

	// 连接状态刚变化时立即响应，不受 3 秒节流限制
	tc.lastConnected = false
	if !tc.shouldRedrawTraySpeed(true, "10K|20K", base.Add(500*time.Millisecond)) {
		t.Fatalf("connection state transition should redraw immediately")
	}
}

func TestShouldHideOnBlurAcrossAllTabs(t *testing.T) {
	vm := newViewModel(nil, engine.Snapshot{})

	// 四个主标签页（含连接页 activeTab==2）点击窗口外部都应自动收起
	for tab := 0; tab <= 3; tab++ {
		vm.activeTab = tab
		if !shouldHideOnBlur(false, false, true, vm) {
			t.Fatalf("tab %d should hide on blur", tab)
		}
	}

	// 仅在更新下载中、模态弹窗打开或 --show 钉住时保持不收起
	vm.activeTab = 2
	vm.activeRulesModal.open = true
	if shouldHideOnBlur(false, false, true, vm) {
		t.Fatalf("active rules modal open should prevent hide on blur")
	}
	vm.activeRulesModal.open = false
	vm.subModalOpen = true
	if shouldHideOnBlur(false, false, true, vm) {
		t.Fatalf("subscribe modal open should prevent hide on blur")
	}
	vm.subModalOpen = false
	vm.updateDownloading = true
	if shouldHideOnBlur(false, false, true, vm) {
		t.Fatalf("update downloading should prevent hide on blur")
	}
	vm.updateDownloading = false
	if shouldHideOnBlur(false, true, true, vm) {
		t.Fatalf("pinWindow (--show) should prevent hide on blur")
	}
}

func TestActiveRulesModalBrowseAndFilter(t *testing.T) {
	vm := newViewModel(nil, engine.Snapshot{
		State:       engine.StateConnected,
		ModeCode:    core.ProxyModeRule,
		ProxyPort:   7890,
		GeoIPLoaded: true,
		RuleCount:   4,
	})
	vm.activeTab = 3
	vm.activeRulesModal.entries = []config.ActiveRuleEntry{
		{Order: 1, Source: config.RuleSourceCustom, Rule: config.DomainRule{Domain: "openai.com", Matching: config.MatchDomainSuffix, ProxyType: 2, Label: "ai"}},
		{Order: 2, Source: config.RuleSourceCustom, Rule: config.DomainRule{Domain: "192.168.0.0/16", Matching: config.MatchCIDR4, ProxyType: 0, DirectConnPriority: 1}},
		{Order: 3, Source: config.RuleSourceSubscribe, Rule: config.DomainRule{Domain: "CN", Matching: config.MatchGeoIP, ProxyType: 0}},
		{Order: 4, Source: config.RuleSourceSubscribe, Rule: config.DomainRule{Domain: "ads.tracker.test", Matching: config.MatchDomainKeyword, ProxyType: 1}},
	}

	tt := ui.NewTester(vm.render, 500, 820)
	if !tt.HasText("查看规则") {
		t.Fatalf("settings tab missing 查看规则 button, texts=%v", tt.Texts())
	}
	if err := tt.Click("查看规则"); err != nil {
		t.Fatalf("click 查看规则: %v", err)
	}
	if !vm.activeRulesModal.open || !tt.HasText("生效分流规则") || !tt.HasText("openai.com") || !tt.HasText("ads.tracker.test") {
		t.Fatalf("active rules modal did not render expected rules: open=%v texts=%v", vm.activeRulesModal.open, tt.Texts())
	}

	// 验证单层弹窗面板在窗口内严格居中，且内部统计卡在面板内左右边距完全对称（无双层框挤压偏移）
	dlg, ok := tt.AccessByRole(ui.RoleDialog, "")
	if !ok {
		t.Fatalf("active rules modal dialog role not found")
	}
	assertDialogCentered(t, "active-rules-modal", dlg.Bounds, 500, 820)
	statAll, okAll := tt.Find("全部规则")
	statReject, okReject := tt.Find("拒绝")
	if !okAll || !okReject {
		t.Fatalf("expected stat cards bounds, got okAll=%v okReject=%v", okAll, okReject)
	}
	leftInset := statAll.X - dlg.Bounds.X
	rightInset := (dlg.Bounds.X + dlg.Bounds.W) - (statReject.X + statReject.W)
	if absFloat32(leftInset-rightInset) > 1.5 || leftInset < 12 || leftInset > 16 {
		t.Fatalf("active rules modal inner insets not symmetric: left=%.1f right=%.1f (dlg=%+v all=%+v reject=%+v)",
			leftInset, rightInset, dlg.Bounds, statAll, statReject)
	}

	// 点击统计卡「拒绝」应只显示拒绝规则
	if err := tt.Click("拒绝"); err != nil {
		t.Fatalf("click 拒绝 stat card: %v", err)
	}
	if !tt.HasText("ads.tracker.test") || tt.HasText("openai.com") {
		t.Fatalf("reject action filter did not take effect, texts=%v", tt.Texts())
	}

	// 点击重置筛选恢复全部规则
	if err := tt.Click("重置筛选"); err != nil {
		t.Fatalf("click 重置筛选: %v", err)
	}
	if !tt.HasText("openai.com") || !tt.HasText("CN") {
		t.Fatalf("reset filter did not restore all rules, texts=%v", tt.Texts())
	}

	// 点击来源「自定义 (2)」应只显示自定义规则
	if err := tt.Click("自定义 (2)"); err != nil {
		t.Fatalf("click 自定义 (2): %v", err)
	}
	if !tt.HasText("openai.com") || !tt.HasText("192.168.0.0/16") || tt.HasText("ads.tracker.test") {
		t.Fatalf("custom source filter did not take effect, texts=%v", tt.Texts())
	}

	// 关闭弹窗
	if err := tt.Click("关闭生效规则"); err != nil {
		t.Fatalf("click 关闭生效规则: %v", err)
	}
	if vm.activeRulesModal.open {
		t.Fatalf("active rules modal did not close")
	}
}

func assertDialogCentered(t *testing.T, name string, bounds ui.Rect, winW, winH float32) {
	t.Helper()
	if bounds.W <= 0 || bounds.H <= 0 {
		t.Fatalf("%s invalid bounds: %+v", name, bounds)
	}
	leftMargin := bounds.X
	rightMargin := winW - (bounds.X + bounds.W)
	topMargin := bounds.Y
	bottomMargin := winH - (bounds.Y + bounds.H)
	if absFloat32(leftMargin-rightMargin) > 1.5 {
		t.Fatalf("%s not horizontally centered: left=%.1f right=%.1f bounds=%+v", name, leftMargin, rightMargin, bounds)
	}
	if absFloat32(topMargin-bottomMargin) > 1.5 {
		t.Fatalf("%s not vertically centered: top=%.1f bottom=%.1f bounds=%+v", name, topMargin, bottomMargin, bounds)
	}
}

func absFloat32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
