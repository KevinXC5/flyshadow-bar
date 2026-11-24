package engine

import (
	"testing"

	"github.com/KevinXC5/flyshadow-bar/internal/core"
)

func TestParseLsofClientMap(t *testing.T) {
	sample := "" +
		"p24386\n" +
		"cFlyShadow Bar\n" +
		"f25\n" +
		"n127.0.0.1:7890->127.0.0.1:59900\n" +
		"p30549\n" +
		"cChromium Helper (Aperitif)\n" +
		"f26\n" +
		"n127.0.0.1:59900->127.0.0.1:7890\n" +
		"p20728\n" +
		"cT3\\x20Code\n" +
		"f13\n" +
		"n[::1]:59901->[::1]:7890\n"

	got := parseLsofClientMap(sample, 24386)
	if got["127.0.0.1:59900"] != "Chromium Helper (Aperitif)" {
		t.Fatalf("IPv4 客户端进程解析错误: got %q", got["127.0.0.1:59900"])
	}
	if got["[::1]:59901"] != "T3 Code" {
		t.Fatalf("IPv6/转义进程名解析错误: got %q", got["[::1]:59901"])
	}
	if _, exists := got["127.0.0.1:7890"]; exists {
		t.Fatalf("不应包含代理服务端自身端点")
	}
}

func TestEngineConnectionsResolvesClientProcessAndCaches(t *testing.T) {
	cfgPath := writeTestConfig(t)
	mc := &mockCore{}
	sp := &mockSysProxy{}
	eng, err := New(Options{
		ConfigPath:          cfgPath,
		CoreClient:          mc,
		SysProxy:            sp,
		DisableAutoSysProxy: true,
	})
	if err != nil {
		t.Fatalf("New engine failed: %v", err)
	}
	defer eng.Shutdown()

	// 注入模拟的批量反查函数
	liveMap := map[string]string{
		"127.0.0.1:59900": "Chromium",
	}
	eng.procLookupFn = func(int) map[string]string {
		return liveMap
	}

	mc.mu.Lock()
	mc.conns = []core.ConnectionInfo{
		{
			Protocol:    "TCP",
			SourceAddr:  "127.0.0.1",
			SourcePort:  59900,
			TargetAddr:  "github.com",
			TargetPort:  443,
			ProcessName: "FlyShadow", // 核心库误报服务端自身
			Status:      "Connected",
			ActiveTime:  200,
		},
		{
			Protocol:    "TCP",
			SourceAddr:  "127.0.0.1",
			SourcePort:  59901,
			TargetAddr:  "example.com",
			TargetPort:  443,
			ProcessName: "FlyShadow", // 已断开且未查到真实进程
			Status:      "Disconnected",
			ActiveTime:  100,
		},
	}
	mc.mu.Unlock()

	got, err := eng.Connections()
	if err != nil {
		t.Fatalf("Connections failed: %v", err)
	}
	if got[0].ProcessName != "Chromium" {
		t.Fatalf("期望替换为真实客户端进程 Chromium，实际为 %q", got[0].ProcessName)
	}
	if got[1].ProcessName != "" {
		t.Fatalf("未命中且为核心库误报自身时应清空进程名，实际为 %q", got[1].ProcessName)
	}

	// 连接随后断开（lsof 已查不到），应从缓存保留之前识别出的 Chromium
	liveMap = nil
	mc.mu.Lock()
	mc.conns[0].Status = "Disconnected"
	mc.mu.Unlock()

	got2, err := eng.Connections()
	if err != nil {
		t.Fatalf("Connections second call failed: %v", err)
	}
	if got2[0].ProcessName != "Chromium" {
		t.Fatalf("断开后应保留缓存的真实进程名 Chromium，实际为 %q", got2[0].ProcessName)
	}
}
