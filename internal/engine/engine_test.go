package engine

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
)

type mockCore struct {
	mu           sync.Mutex
	proxyPort    int
	allowLAN     bool
	mode         core.ProxyMode
	udpMode      core.ProxyMode
	nativeUDP    bool
	connected    bool
	lastHost     string
	lastPort     int
	lastQuic     bool
	ruleCount    int
	upload       int64
	download     int64
	delayResults map[int]int64
}

func (m *mockCore) LibPath() string                  { return "/mock/libflyshadow_core_lib.dylib" }
func (m *mockCore) Version() string                  { return "2.0.2-mock" }
func (m *mockCore) Init() error                      { return nil }
func (m *mockCore) SetClientUUID(string)             {}
func (m *mockCore) SetIPv6Enable(bool)               {}
func (m *mockCore) SetUseBuildInDNS(bool)            {}
func (m *mockCore) SetDirectConnPriority(bool, int64) {}
func (m *mockCore) SetGeoIP(string, []byte)          {}

func (m *mockCore) SetProxyType(mode core.ProxyMode) {
	m.mu.Lock()
	m.mode = mode
	m.mu.Unlock()
}

func (m *mockCore) SetUDPProxyType(mode core.ProxyMode) {
	m.mu.Lock()
	m.udpMode = mode
	m.mu.Unlock()
}

func (m *mockCore) SetNativeUDP(enabled bool) {
	m.mu.Lock()
	m.nativeUDP = enabled
	m.mu.Unlock()
}

func (m *mockCore) ClearDomainRules() {
	m.mu.Lock()
	m.ruleCount = 0
	m.mu.Unlock()
}

func (m *mockCore) SetDomainRule(string, int32, int32, int32) {
	m.mu.Lock()
	m.ruleCount++
	m.mu.Unlock()
}

func (m *mockCore) StartProxy(port int, allowLAN bool, _, _ string) error {
	m.mu.Lock()
	m.proxyPort = port
	m.allowLAN = allowLAN
	m.mu.Unlock()
	return nil
}

func (m *mockCore) StopProxy() {}

func (m *mockCore) ConnectTunnel(host string, port int, _ string, enableQuic bool) error {
	m.mu.Lock()
	m.lastHost = host
	m.lastPort = port
	m.lastQuic = enableQuic
	m.connected = true
	m.mu.Unlock()
	return nil
}

func (m *mockCore) CloseTunnel() int32 {
	m.mu.Lock()
	m.connected = false
	m.mu.Unlock()
	return 0
}

func (m *mockCore) TestTunnelDelay(node config.Node, _ bool) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.delayResults[node.ID]; ok {
		return v
	}
	return 88
}

func (m *mockCore) TunnelUploadBytes() int64   { return m.upload }
func (m *mockCore) TunnelDownloadBytes() int64 { return m.download }
func (m *mockCore) TunnelPingDelay() int32     { return 42 }
func (m *mockCore) TunnelStatus() int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connected {
		return 0
	}
	return 1
}
func (m *mockCore) TunnelConnections() ([]core.ConnectionInfo, error) {
	return []core.ConnectionInfo{
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50001, TargetAddr: "openai.com", TargetPort: 443, ProxyType: "Proxy", ActiveTime: 100},
	}, nil
}
func (m *mockCore) CloseConnection(string) {}
func (m *mockCore) Close()                 { m.CloseTunnel() }

type mockSysProxy struct {
	mu      sync.Mutex
	enabled bool
	host    string
	port    int
}

func (s *mockSysProxy) Enable(host string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = true
	s.host = host
	s.port = port
	return nil
}

func (s *mockSysProxy) Disable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = false
	return nil
}

func (s *mockSysProxy) IsEnabled(host string, port int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled && s.host == host && s.port == port, nil
}

func writeTestConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	content := `{
  "appAccountToken": "preserve-me",
  "proxyPort": 17890,
  "allowLanConnection": false,
  "proxyType": 3,
  "udpProxyType": 3,
  "udpAccelerationEnabled": true,
  "nativeUdp": false,
  "starNodeIdList": [1],
  "subscribeInfo": {
    "node": [
      {"id": 1, "name": "HK-01", "cnName": "香港 01", "host": "127.0.0.1", "port": 1001, "password": "p1", "enableQuic": 1, "nativeUdp": 1},
      {"id": 2, "name": "JP-01", "cnName": "日本 01", "host": "127.0.0.1", "port": 1002, "password": "p2", "enableQuic": 0, "nativeUdp": 1}
    ],
    "customRule": [
      {"domain": "example.com", "matching": 1, "proxyType": 2, "directConnPriority": 0}
    ]
  },
  "usedNodeInfo": {"id": 1, "name": "HK-01", "cnName": "香港 01", "host": "127.0.0.1", "port": 1001, "password": "p1", "enableQuic": 1, "nativeUdp": 1}
}`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEngineLifecycleAndControls(t *testing.T) {
	cfgPath := writeTestConfig(t)
	mc := &mockCore{delayResults: map[int]int64{1: 120, 2: 45}}
	sp := &mockSysProxy{}

	eng, err := New(Options{
		ConfigPath: cfgPath,
		CoreClient: mc,
		SysProxy:   sp,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := eng.Start(true); err != nil {
		t.Fatalf("Start: %v", err)
	}
	snap := eng.Snapshot()
	if snap.State != StateConnected || snap.CurrentNode == nil || snap.CurrentNode.ID != 1 {
		t.Fatalf("unexpected initial snapshot: %+v", snap)
	}
	if !snap.EffectiveTransport.EnableQuic || snap.EffectiveTransport.EnableNativeUDP {
		t.Fatalf("unexpected transport decision: %+v", snap.EffectiveTransport)
	}

	// 热切节点到 2（不支持 QUIC）
	if _, err := eng.SelectNodeByQuery("日本"); err != nil {
		t.Fatalf("SelectNodeByQuery: %v", err)
	}
	if mc.lastPort != 1002 || mc.lastQuic {
		t.Fatalf("expected node 2 without quic, got port=%d quic=%v", mc.lastPort, mc.lastQuic)
	}

	// 切换模式与原生 UDP
	if err := eng.SetMode(core.ProxyModeGlobal); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if mc.mode != core.ProxyModeGlobal {
		t.Fatalf("core mode not updated")
	}
	if err := eng.SetNativeUDP(true); err != nil {
		t.Fatalf("SetNativeUDP: %v", err)
	}
	if !mc.nativeUDP {
		t.Fatalf("nativeUDP not applied")
	}

	// 开启系统代理，再验证 Shutdown 会自动关闭系统代理
	if err := eng.SetSystemProxy(true); err != nil {
		t.Fatalf("SetSystemProxy: %v", err)
	}
	if !sp.enabled || sp.port != 17890 {
		t.Fatalf("sysproxy not enabled")
	}

	// 并发测速排序验证
	ranked, err := eng.PingAll(context.Background())
	if err != nil {
		t.Fatalf("PingAll: %v", err)
	}
	if len(ranked) != 2 || ranked[0].ID != 2 || ranked[0].DelayMs != 45 {
		t.Fatalf("unexpected ping ranking: %+v", ranked)
	}

	eng.Shutdown()
	if sp.enabled {
		t.Fatalf("Shutdown did not disable system proxy")
	}
}
