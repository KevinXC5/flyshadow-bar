package ui

import (
	"strings"
	"testing"

	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
)

func TestTrayMenuCompactLayoutOnWindowsAndDarwin(t *testing.T) {
	longNodeName := "V3 香港17 UDP加速 1Gbps 1x 超长线路名称测试"
	snap := engine.Snapshot{
		State:            engine.StateConnected,
		ModeCode:         core.ProxyModeRule,
		ProxyPort:        7890,
		UploadSpeedBps:   12800,
		DownloadSpeedBps: 2048000,
		CurrentNode: &engine.NodeSnapshot{
			ID:          1,
			DisplayName: longNodeName,
			EnableQuic:  true,
			NativeUDP:   true,
			Starred:     true,
			Selected:    true,
			DelayMs:     42,
		},
		Nodes: []engine.NodeSnapshot{
			{
				ID:          1,
				DisplayName: longNodeName,
				EnableQuic:  true,
				NativeUDP:   true,
				Starred:     true,
				Selected:    true,
				DelayMs:     42,
			},
		},
	}

	tc := &trayController{}

	// Windows：顶部状态拆成两行禁用项且不设置 Subtitle，防止被 mygo 拼成超长单行撑宽托盘菜单。
	winMenu := tc.buildMenuForOS(snap, "windows")
	tc.menu = winMenu

	header := winMenu.ItemByID("status-header")
	detail := winMenu.ItemByID("status-detail")
	nodesItem := winMenu.ItemByID("nodes-submenu")
	if header == nil || detail == nil || nodesItem == nil {
		t.Fatalf("windows menu missing expected items: header=%v detail=%v nodes=%v", header, detail, nodesItem)
	}
	if header.Subtitle != "" || detail.Subtitle != "" {
		t.Fatalf("windows status items must not use Subtitle (concatenated inline): header=%q detail=%q", header.Subtitle, detail.Subtitle)
	}
	if header.Label != "● 已连接 · 127.0.0.1:7890" {
		t.Fatalf("unexpected windows status header label: %q", header.Label)
	}
	if detail.Label != "↑ 12.5 KB/s  ↓ 1.95 MB/s" {
		t.Fatalf("unexpected windows status detail label: %q", detail.Label)
	}
	if strings.Contains(header.Label, "香港") {
		t.Fatalf("status header should not duplicate node name: %q", header.Label)
	}
	if !strings.HasPrefix(nodesItem.Label, "节点: ") || !strings.HasSuffix(nodesItem.Label, "…") {
		t.Fatalf("expected truncated node submenu label, got %q", nodesItem.Label)
	}
	if cols := menuDisplayCols(nodesItem.Label); cols > 6+trayMenuNodeNameMaxCols {
		t.Fatalf("node submenu label too wide: %d cols (%q)", cols, nodesItem.Label)
	}
	if len(nodesItem.Submenu) != 1 {
		t.Fatalf("expected 1 node submenu item, got %d", len(nodesItem.Submenu))
	}
	if nodesItem.Submenu[0].Subtitle != "" {
		t.Fatalf("windows node submenu item should omit Subtitle to stay compact, got %q", nodesItem.Submenu[0].Subtitle)
	}

	// 验证增量刷新能同步更新 Windows 的两行状态与节点入口文案。
	snap.UploadSpeedBps = 0
	snap.DownloadSpeedBps = 512
	snap.CurrentNode.DisplayName = "香港 01"
	tc.patchMenuInPlaceForOS(snap, "windows")
	if detail.Label != "↑ 0 B/s  ↓ 512 B/s" {
		t.Fatalf("patchMenuInPlaceForOS did not update windows status-detail: %q", detail.Label)
	}
	if nodesItem.Label != "节点: 香港 01" {
		t.Fatalf("patchMenuInPlaceForOS did not update nodes-submenu: %q", nodesItem.Label)
	}

	// macOS：保留 status-header 的原生双行 Subtitle 与节点协议副标题，不重复创建 status-detail。
	snap.CurrentNode.DisplayName = longNodeName
	darwinMenu := tc.buildMenuForOS(snap, "darwin")
	if darwinMenu.ItemByID("status-detail") != nil {
		t.Fatalf("darwin menu should use native Subtitle instead of status-detail item")
	}
	dHeader := darwinMenu.ItemByID("status-header")
	if dHeader == nil || dHeader.Subtitle != "↑ 0 B/s  ↓ 512 B/s" {
		t.Fatalf("darwin status-header missing expected Subtitle: %+v", dHeader)
	}
	dNodes := darwinMenu.ItemByID("nodes-submenu")
	if dNodes == nil || len(dNodes.Submenu) != 1 || dNodes.Submenu[0].Subtitle != "QUIC · UDP" {
		t.Fatalf("darwin node submenu should keep protocol subtitle, got %+v", dNodes)
	}
}

func TestTruncateMenuTextAndErrorHeader(t *testing.T) {
	short := "香港 01"
	if got := truncateMenuText(short, 22); got != short {
		t.Fatalf("short text should stay intact: got %q", got)
	}

	got := truncateMenuText("V3 香港17 UDP加速 1Gbps 1x", 22)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis for long text, got %q", got)
	}
	if cols := menuDisplayCols(got); cols > 22 {
		t.Fatalf("truncated text exceeded maxCols=22: %d (%q)", cols, got)
	}

	errSnap := engine.Snapshot{
		State:     engine.StateError,
		ProxyPort: 7890,
		LastError: "dial tcp 198.51.100.10:443: connect: operation timed out after multiple retries",
	}
	if label := statusHeaderLabel(errSnap); label != "✕ 连接异常" {
		t.Fatalf("unexpected error header label: %q", label)
	}
	sub := statusHeaderSubtitle(errSnap)
	if !strings.HasSuffix(sub, "…") || menuDisplayCols(sub) > trayMenuErrorMaxCols {
		t.Fatalf("error subtitle should be truncated within %d cols, got %d (%q)", trayMenuErrorMaxCols, menuDisplayCols(sub), sub)
	}
}

func TestTrayMenuPatchDiffOnlyWritesChangedFields(t *testing.T) {
	snap := engine.Snapshot{
		State:                  engine.StateConnected,
		ModeCode:               core.ProxyModeRule,
		ProxyPort:              7890,
		SystemProxy:            true,
		UDPAccelerationEnabled: true,
		NativeUDP:              false,
		HasCloudAuth:           true,
		UploadSpeedBps:         0,
		DownloadSpeedBps:       0,
		CurrentNode: &engine.NodeSnapshot{
			ID:          1,
			DisplayName: "香港 01",
			Selected:    true,
			DelayMs:     36,
		},
		Nodes: []engine.NodeSnapshot{
			{ID: 1, DisplayName: "香港 01", Selected: true, DelayMs: 36},
			{ID: 2, DisplayName: "日本 01", Selected: false, DelayMs: 88},
		},
	}

	for _, goos := range []string{"darwin", "windows"} {
		tc := &trayController{}
		tc.menu = tc.buildMenuForOS(snap, goos)

		// 相同快照重复刷新时不应触发任何原生菜单项写入，避免无谓重绘与闪烁。
		if n := tc.patchMenuInPlaceForOS(snap, goos); n != 0 {
			t.Fatalf("%s: expected 0 writes for unchanged snapshot, got %d", goos, n)
		}

		// 仅网速变化时只更新网速显示项 1 次。
		speedSnap := snap
		speedSnap.DownloadSpeedBps = 1024
		if n := tc.patchMenuInPlaceForOS(speedSnap, goos); n != 1 {
			t.Fatalf("%s: expected 1 write for speed-only change, got %d", goos, n)
		}
		if n := tc.patchMenuInPlaceForOS(speedSnap, goos); n != 0 {
			t.Fatalf("%s: expected 0 writes after speed settled, got %d", goos, n)
		}

		// 切换代理模式（Radio 组）时仅需 1 次 SetChecked(true)，同组旧项自动取消勾选。
		modeSnap := speedSnap
		modeSnap.ModeCode = core.ProxyModeGlobal
		if n := tc.patchMenuInPlaceForOS(modeSnap, goos); n != 1 {
			t.Fatalf("%s: expected 1 write for radio mode switch, got %d", goos, n)
		}
		if tc.menu.ItemByID("mode-rule").IsChecked() || !tc.menu.ItemByID("mode-global").IsChecked() || tc.menu.ItemByID("mode-direct").IsChecked() {
			t.Fatalf("%s: unexpected radio states after mode switch", goos)
		}
	}
}

func TestTrayContextMenuPlatformStrategy(t *testing.T) {
	if !usesOnDemandContextMenu("darwin") || !usesOnDemandContextMenu("windows") {
		t.Fatalf("darwin and windows should both use on-demand right-click context menus")
	}
	if usesOnDemandContextMenu("linux") {
		t.Fatalf("linux should keep persistent menu attached to tray")
	}
	if !buildsFreshMenuOnRightClick("windows") {
		t.Fatalf("windows should build a fresh menu on right-click to prevent Win11 acrylic redraw flicker")
	}
	if buildsFreshMenuOnRightClick("darwin") || buildsFreshMenuOnRightClick("linux") {
		t.Fatalf("darwin and linux should keep in-place menu patching")
	}
}
