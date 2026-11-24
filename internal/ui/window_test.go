package ui

import (
	"testing"

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
		TunnelPingMs:     42,
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
		},
	}

	vm := newViewModel(nil, snap)
	vm.connections = []core.ConnectionInfo{
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 54321, TargetAddr: "api.openai.com", TargetPort: 443, ProcessName: "curl", ProxyType: "Proxy", UploadTrafficTotal: 2048, DownloadTrafficTotal: 8192},
	}

	// 验证节点页渲染与筛选交互
	tt := ui.NewTester(vm.render, 500, 780)
	if !tt.HasText("香港 IEPL 01") || !tt.HasText("日本 东京 02") {
		t.Fatalf("node list missing expected text, got: %v", tt.Texts())
	}
	if err := tt.Click("收藏"); err != nil {
		t.Fatalf("click 收藏: %v", err)
	}
	if !vm.onlyStarred || tt.HasText("日本 东京 02") {
		t.Fatalf("onlyStarred filter did not take effect: onlyStarred=%v", vm.onlyStarred)
	}

	// 切换到规则页验证渲染
	if err := tt.Click("规则"); err != nil {
		t.Fatalf("click 规则: %v", err)
	}
	if vm.activeTab != 1 || !tt.HasText("openai.com") {
		t.Fatalf("rules tab did not render expected rule, activeTab=%d", vm.activeTab)
	}

	// 切换到连接页验证渲染
	if err := tt.Click("连接"); err != nil {
		t.Fatalf("click 连接: %v", err)
	}
	if vm.activeTab != 2 || !tt.HasText("api.openai.com:443") {
		t.Fatalf("connections tab did not render expected connection, activeTab=%d", vm.activeTab)
	}

	// 切换到设置页验证渲染
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click 设置: %v", err)
	}
	if vm.activeTab != 3 || !tt.HasText("混合代理端口 (HTTP/SOCKS5)") {
		t.Fatalf("settings tab did not render expected content, activeTab=%d", vm.activeTab)
	}

	// 验证图标 PNG 生成有效
	pngConn := makeTrayIconPNG(true)
	pngIdle := makeTrayIconPNG(false)
	if len(pngConn) < 100 || len(pngIdle) < 100 {
		t.Fatalf("tray icon PNG too small: conn=%d idle=%d", len(pngConn), len(pngIdle))
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
	if got := FormatDelay(48, false); got != "48 ms" {
		t.Fatalf("FormatDelay(48) = %q", got)
	}
	if got := FormatDelay(-1, false); got != "超时" {
		t.Fatalf("FormatDelay(-1) = %q", got)
	}
}
