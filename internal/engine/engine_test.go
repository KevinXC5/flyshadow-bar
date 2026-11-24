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
	conns        []core.ConnectionInfo
}

func (m *mockCore) LibPath() string                   { return "/mock/libflyshadow_core_lib.dylib" }
func (m *mockCore) Version() string                   { return "2.0.2-mock" }
func (m *mockCore) Init() error                       { return nil }
func (m *mockCore) SetClientUUID(string)              {}
func (m *mockCore) SetIPv6Enable(bool)                {}
func (m *mockCore) SetUseBuildInDNS(bool)             {}
func (m *mockCore) SetDirectConnPriority(bool, int64) {}
func (m *mockCore) SetGeoIP(string, []byte)           {}

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
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.conns) > 0 {
		out := make([]core.ConnectionInfo, len(m.conns))
		copy(out, m.conns)
		return out, nil
	}
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

type mockCloud struct {
	mu               sync.Mutex
	expectedPassword string
	sub              config.SubscribeInfo
	rules            []config.DomainRule
	savedCalls       int
	updatedCalls     int
	removedIDs       []int
	nextID           int
}

func (m *mockCloud) FetchSubscribe(_ context.Context, pw, _ string) (*config.SubscribeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.expectedPassword != "" && pw != m.expectedPassword {
		return nil, os.ErrPermission
	}
	cp := m.sub
	return &cp, nil
}

func (m *mockCloud) FetchCustomRules(_ context.Context, _, _ string) ([]config.DomainRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]config.DomainRule, len(m.rules))
	copy(out, m.rules)
	return out, nil
}

func (m *mockCloud) SaveCustomRule(_ context.Context, _ string, rule config.DomainRule, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.savedCalls++
	m.nextID++
	rule.ID = m.nextID
	m.rules = append([]config.DomainRule{rule}, m.rules...)
	return nil
}

func (m *mockCloud) UpdateCustomRule(_ context.Context, _ string, rule config.DomainRule, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedCalls++
	for i := range m.rules {
		if m.rules[i].ID == rule.ID {
			m.rules[i] = rule
		}
	}
	return nil
}

func (m *mockCloud) RemoveCustomRule(_ context.Context, _ string, id int, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removedIDs = append(m.removedIDs, id)
	for i := range m.rules {
		if m.rules[i].ID == id {
			m.rules = append(m.rules[:i], m.rules[i+1:]...)
			break
		}
	}
	return nil
}

func TestEngineCloudSyncSubscriptionAndRules(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	rawCfg := `{
  "subscribePassword": "mock-cloud-password",
  "proxyPort": 17891,
  "proxyType": 3,
  "subscribeInfo": {
    "node": [{"id": 1, "name": "OLD-01", "cnName": "旧节点", "host": "old.example.invalid", "port": 443}],
    "customRule": []
  },
  "usedNodeInfo": {"id": 1, "name": "OLD-01", "cnName": "旧节点", "host": "old.example.invalid", "port": 443}
}`
	if err := os.WriteFile(p, []byte(rawCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	mc := &mockCore{}
	sp := &mockSysProxy{}
	cc := &mockCloud{
		nextID: 100,
		sub: config.SubscribeInfo{
			Nodes: []config.Node{
				{ID: 1, Name: "HK-NEW", CNName: "香港新节点", Host: "hk-new.example.invalid", Port: 443, EnableQuic: 1},
				{ID: 2, Name: "SG-NEW", CNName: "新加坡新节点", Host: "sg-new.example.invalid", Port: 443, NativeUDP: 1},
			},
			User: &config.UserTrafficInfo{
				TrafficSize:     200 * 1024 * 1024 * 1024,
				TrafficUpload:   10 * 1024 * 1024 * 1024,
				TrafficDownload: 50 * 1024 * 1024 * 1024,
				Level:           3,
				ExpireTime:      config.FlexMillis(1796610616000),
			},
		},
		rules: []config.DomainRule{
			{ID: 100, Enable: config.IntPtr(1), Domain: "cloud.example.com", Matching: config.MatchDomainSuffix, ProxyType: 2, Label: "ai"},
		},
	}

	eng, err := New(Options{
		ConfigPath:          p,
		CoreClient:          mc,
		SysProxy:            sp,
		CloudClient:         cc,
		DisableAutoSysProxy: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Shutdown()

	// 1. 同步订阅节点、流量、到期时间与云端规则
	snap, err := eng.SyncSubscription(context.Background())
	if err != nil {
		t.Fatalf("SyncSubscription: %v", err)
	}
	if len(snap.Nodes) != 2 || snap.CurrentNode == nil || snap.CurrentNode.DisplayName != "香港新节点" {
		t.Fatalf("nodes did not sync: %+v", snap.Nodes)
	}
	if snap.Traffic == nil || snap.Traffic.Level != 3 || snap.Traffic.TrafficDownload != 50*1024*1024*1024 {
		t.Fatalf("traffic did not sync: %+v", snap.Traffic)
	}
	if len(snap.CustomRules) != 1 || snap.CustomRules[0].ID != 100 {
		t.Fatalf("custom rules did not sync: %+v", snap.CustomRules)
	}

	// 2. 新增自定义规则应调用云端 save 并拿回云端 ID=101
	added, err := eng.AddCustomRule(config.DomainRule{
		Domain:    "openai.com",
		Matching:  config.MatchDomainSuffix,
		ProxyType: 2,
		Label:     "ai",
	})
	if err != nil {
		t.Fatalf("AddCustomRule: %v", err)
	}
	if cc.savedCalls != 1 || added.ID != 101 {
		t.Fatalf("expected cloud save with id=101, savedCalls=%d added=%+v", cc.savedCalls, added)
	}

	// 3. 切换启用状态应同步调用云端 update
	enabled, err := eng.ToggleCustomRule(101)
	if err != nil || enabled || cc.updatedCalls != 1 {
		t.Fatalf("ToggleCustomRule = (%v, %v), updatedCalls=%d", enabled, err, cc.updatedCalls)
	}

	// 4. 删除规则应同步调用云端 remove
	if err := eng.DeleteCustomRule(101); err != nil {
		t.Fatalf("DeleteCustomRule: %v", err)
	}
	if len(cc.removedIDs) != 1 || cc.removedIDs[0] != 101 {
		t.Fatalf("expected cloud remove id=101, got %v", cc.removedIDs)
	}
}

func TestEngineSetSubscribePasswordVerifyBeforeSave(t *testing.T) {
	// 零配置启动（config.json 尚不存在）
	p := filepath.Join(t.TempDir(), "config.json")
	mc := &mockCore{}
	sp := &mockSysProxy{}
	cc := &mockCloud{
		expectedPassword: "valid-mock-pw",
		sub: config.SubscribeInfo{
			Nodes: []config.Node{
				{ID: 10, Name: "HK-INIT", CNName: "香港初始化节点", Host: "hk.example.invalid", Port: 443, EnableQuic: 1},
			},
			User: &config.UserTrafficInfo{
				TrafficSize:     100 * 1024 * 1024 * 1024,
				TrafficDownload: 10 * 1024 * 1024 * 1024,
				Level:           2,
			},
		},
	}

	eng, err := New(Options{
		ConfigPath:          p,
		CoreClient:          mc,
		SysProxy:            sp,
		CloudClient:         cc,
		DisableAutoSysProxy: true,
	})
	if err != nil {
		t.Fatalf("New with missing config failed: %v", err)
	}
	defer eng.Shutdown()

	if eng.Snapshot().HasCloudAuth {
		t.Fatalf("expected HasCloudAuth=false initially")
	}

	// 错误密码不得写入磁盘或污染内存状态
	if _, err := eng.SetSubscribePassword(context.Background(), "wrong-pw"); err == nil {
		t.Fatalf("expected error for wrong password")
	}
	if eng.Snapshot().HasCloudAuth {
		t.Fatalf("wrong password should not set HasCloudAuth")
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Fatalf("wrong password should not create config file")
	}

	// 正确密码校验通过后自动落盘并同步节点与流量
	snap, err := eng.SetSubscribePassword(context.Background(), "valid-mock-pw")
	if err != nil {
		t.Fatalf("SetSubscribePassword failed: %v", err)
	}
	if !snap.HasCloudAuth || len(snap.Nodes) != 1 || snap.CurrentNode == nil || snap.CurrentNode.ID != 10 {
		t.Fatalf("unexpected snapshot after SetSubscribePassword: %+v", snap)
	}
	reloaded, err := config.Load(p)
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if reloaded.SubscribePassword != "valid-mock-pw" {
		t.Fatalf("saved subscribePassword = %q, want valid-mock-pw", reloaded.SubscribePassword)
	}
}
