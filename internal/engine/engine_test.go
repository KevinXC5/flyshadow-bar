package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/sysproxy"
)

type mockCore struct {
	mu           sync.Mutex
	proxyPort    int
	allowLAN     bool
	mode         core.ProxyMode
	udpMode      core.ProxyMode
	nativeUDP    bool
	connected    bool
	neverConnect bool
	closeCalls   int
	lastHost     string
	lastPort     int
	lastQuic     bool
	ruleCount    int
	upload       int64
	download     int64
	delayResults map[int]int64
	delaySeq     map[int][]int64
	delayCalls   map[int]int
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
	m.connected = !m.neverConnect
	m.mu.Unlock()
	return nil
}

func (m *mockCore) CloseTunnel() int32 {
	m.mu.Lock()
	m.connected = false
	m.closeCalls++
	m.mu.Unlock()
	return 0
}

func (m *mockCore) TestTunnelDelay(node config.Node, _ bool) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.delayCalls == nil {
		m.delayCalls = make(map[int]int)
	}
	idx := m.delayCalls[node.ID]
	m.delayCalls[node.ID] = idx + 1
	if seq, ok := m.delaySeq[node.ID]; ok && len(seq) > 0 {
		if idx < len(seq) {
			return seq[idx]
		}
		return seq[len(seq)-1]
	}
	if v, ok := m.delayResults[node.ID]; ok {
		return v
	}
	return 88
}

func (m *mockCore) TunnelUploadBytes() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.upload
}
func (m *mockCore) TunnelDownloadBytes() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.download
}
func (m *mockCore) TunnelPingDelay() int32 { return 42 }
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
	fetchCalls       int
	fetchErr         error
	savedCalls       int
	updatedCalls     int
	removedIDs       []int
	nextID           int
}

func (m *mockCloud) FetchSubscribe(_ context.Context, pw, _ string) (*config.SubscribeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetchCalls++
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
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
	if got := eng.SubscribePassword(); got != "valid-mock-pw" {
		t.Fatalf("SubscribePassword() = %q, want valid-mock-pw", got)
	}
}

func TestEngineRefreshSystemProxyAndDirectSpeedSampling(t *testing.T) {
	cfgPath := writeTestConfig(t)
	mc := &mockCore{upload: 128000, download: 5 * 1024 * 1024}
	sp := &mockSysProxy{}

	eng, err := New(Options{
		ConfigPath:          cfgPath,
		CoreClient:          mc,
		SysProxy:            sp,
		DisableAutoSysProxy: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Shutdown()

	if err := eng.Start(true); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 开启系统代理后，模拟外部进程清空系统代理
	if err := eng.SetSystemProxy(true); err != nil {
		t.Fatalf("SetSystemProxy(true): %v", err)
	}
	if !eng.Snapshot().SystemProxy {
		t.Fatalf("expected SystemProxy=true")
	}

	var notified bool
	var mu sync.Mutex
	unsub := eng.Subscribe(func(s Snapshot) {
		mu.Lock()
		if !s.SystemProxy {
			notified = true
		}
		mu.Unlock()
	})
	defer unsub()

	// 外部清空系统代理
	_ = sp.Disable()
	if got := eng.RefreshSystemProxy(); got {
		t.Fatalf("RefreshSystemProxy() = true, want false after external disable")
	}
	if eng.Snapshot().SystemProxy {
		t.Fatalf("Snapshot().SystemProxy should be false after RefreshSystemProxy")
	}
	mu.Lock()
	wasNotified := notified
	mu.Unlock()
	if !wasNotified {
		t.Fatalf("expected subscriber notification when system proxy changed externally")
	}

	// 验证速率采样直接使用核心库返回的每秒字节数，而非前后两次读数相减导致归零
	eng.sampleStats(time.Now())
	eng.sampleStats(time.Now().Add(time.Second))
	snap := eng.Snapshot()
	if snap.DownloadSpeedBps != 5*1024*1024 || snap.UploadSpeedBps != 128000 {
		t.Fatalf("speed sampling mismatch: down=%d up=%d", snap.DownloadSpeedBps, snap.UploadSpeedBps)
	}
}

func TestEngineConnectClosesOldTunnelAndCleansUpOnTimeout(t *testing.T) {
	cfgPath := writeTestConfig(t)
	mc := &mockCore{neverConnect: true}
	sp := &mockSysProxy{}

	eng, err := New(Options{
		ConfigPath:          cfgPath,
		CoreClient:          mc,
		SysProxy:            sp,
		DisableAutoSysProxy: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Shutdown()

	eng.connectPollInterval = 5 * time.Millisecond
	eng.connectPollAttempts = 3

	if err := eng.Connect(); err == nil {
		t.Fatalf("expected connect timeout error")
	}
	snap := eng.Snapshot()
	if snap.State != StateError {
		t.Fatalf("state = %q, want %q", snap.State, StateError)
	}
	mc.mu.Lock()
	closes := mc.closeCalls
	mc.mu.Unlock()
	// 建连前关闭一次 + 超时后清理关闭一次
	if closes != 2 {
		t.Fatalf("closeCalls = %d, want 2 (before connect + on timeout)", closes)
	}
}

func TestEngineSampleStatsSyncsDisconnectAndReconnect(t *testing.T) {
	cfgPath := writeTestConfig(t)
	mc := &mockCore{upload: 4096, download: 65536}
	sp := &mockSysProxy{}

	eng, err := New(Options{
		ConfigPath:          cfgPath,
		CoreClient:          mc,
		SysProxy:            sp,
		DisableAutoSysProxy: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Shutdown()

	eng.disconnectErrorThreshold = 3
	if err := eng.Start(true); err != nil {
		t.Fatalf("Start: %v", err)
	}

	now := time.Now()
	eng.sampleStats(now)
	snap := eng.Snapshot()
	if snap.State != StateConnected || snap.TunnelPingMs != 42 || snap.DownloadSpeedBps != 65536 {
		t.Fatalf("unexpected connected snapshot: state=%q ping=%d down=%d", snap.State, snap.TunnelPingMs, snap.DownloadSpeedBps)
	}

	// 模拟底层节点失联：TunnelStatus 变为非 0
	mc.mu.Lock()
	mc.connected = false
	mc.mu.Unlock()

	// 第 1 次采样应立即离开「已连接」，转为「连接中」，清零速率和旧延迟，且本地代理不再对外宣称可用
	eng.sampleStats(now.Add(time.Second))
	snap = eng.Snapshot()
	if snap.State != StateConnecting {
		t.Fatalf("state after 1st disconnect sample = %q, want %q", snap.State, StateConnecting)
	}
	if snap.UploadSpeedBps != 0 || snap.DownloadSpeedBps != 0 || snap.TunnelPingMs != -1 {
		t.Fatalf("disconnected metrics not cleared: up=%d down=%d ping=%d", snap.UploadSpeedBps, snap.DownloadSpeedBps, snap.TunnelPingMs)
	}
	if got := eng.LocalProxyURL(); got != "" {
		t.Fatalf("LocalProxyURL() while reconnecting = %q, want empty", got)
	}

	// 第 2 次仍在重连宽限期内，保持「连接中」
	eng.sampleStats(now.Add(2 * time.Second))
	if got := eng.Snapshot().State; got != StateConnecting {
		t.Fatalf("state after 2nd disconnect sample = %q, want %q", got, StateConnecting)
	}

	// 第 3 次达到阈值，转为「连接异常」并附带错误提示
	eng.sampleStats(now.Add(3 * time.Second))
	snap = eng.Snapshot()
	if snap.State != StateError || snap.LastError == "" {
		t.Fatalf("expected StateError with LastError after threshold, got state=%q err=%q", snap.State, snap.LastError)
	}

	// 模拟底层核心库后台自动重连成功（status 恢复为 0）
	mc.mu.Lock()
	mc.connected = true
	mc.mu.Unlock()

	eng.sampleStats(now.Add(4 * time.Second))
	snap = eng.Snapshot()
	if snap.State != StateConnected || snap.LastError != "" || snap.TunnelPingMs != 42 || snap.DownloadSpeedBps != 65536 {
		t.Fatalf("expected automatic recovery to StateConnected, got state=%q err=%q ping=%d down=%d",
			snap.State, snap.LastError, snap.TunnelPingMs, snap.DownloadSpeedBps)
	}

	// 用户主动断开后，即便底层状态再变化，sampleStats 也不得重新切回已连接
	if err := eng.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mc.mu.Lock()
	mc.connected = true
	mc.mu.Unlock()
	eng.sampleStats(now.Add(5 * time.Second))
	if got := eng.Snapshot().State; got != StateDisconnected {
		t.Fatalf("state after explicit Disconnect = %q, want %q", got, StateDisconnected)
	}
}

type mockFilterSysProxy struct {
	mu          sync.Mutex
	enabled     bool
	host        string
	port        int
	services    []string
	excluded    []string
	enabledSvcs []string
	cleanedSvcs []string
}

func (s *mockFilterSysProxy) Enable(host string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = true
	s.host = host
	s.port = port
	return nil
}

func (s *mockFilterSysProxy) Disable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = false
	return nil
}

func (s *mockFilterSysProxy) IsEnabled(host string, port int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled && s.host == host && s.port == port, nil
}

func (s *mockFilterSysProxy) ListServices() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.services...), nil
}

func (s *mockFilterSysProxy) SetExcludedServices(excluded []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.excluded = append([]string(nil), excluded...)
}

func (s *mockFilterSysProxy) ExcludedServices() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.excluded...)
}

func (s *mockFilterSysProxy) EnableService(service, host string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabledSvcs = append(s.enabledSvcs, service)
	return nil
}

func (s *mockFilterSysProxy) DisableServiceIfMatches(service, host string, port int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanedSvcs = append(s.cleanedSvcs, service)
	return true, nil
}

func TestEngineSysProxyServiceFilteringLifecycle(t *testing.T) {
	cfgPath := writeTestConfig(t)
	prefsPath := config.PrefsPathForConfig(cfgPath)

	// 预先写入主题偏好和初始排除网卡，验证加载与保真
	if _, err := config.UpdateAppPrefs(prefsPath, func(p *config.AppPrefs) {
		p.ThemeMode = "dark"
		p.ShowTraySpeed = true
		p.SysProxyExcludedServices = []string{"Thunderbolt Bridge"}
	}); err != nil {
		t.Fatalf("seed prefs: %v", err)
	}

	mc := &mockCore{}
	sp := &mockFilterSysProxy{
		services: []string{"Wi-Fi", "USB 10/100/1000 LAN", "Thunderbolt Bridge"},
	}
	eng, err := New(Options{
		ConfigPath:          cfgPath,
		CoreClient:          mc,
		SysProxy:            sp,
		DisableAutoSysProxy: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Shutdown()

	snap := eng.Snapshot()
	if !snap.SysProxyServiceSelectionSupported {
		t.Fatalf("expected SysProxyServiceSelectionSupported=true")
	}
	if len(snap.SysProxyServices) != 3 || len(snap.SysProxyExcludedServices) != 1 || snap.SysProxyExcludedServices[0] != "Thunderbolt Bridge" {
		t.Fatalf("initial snapshot services/excluded mismatch: services=%v excluded=%v", snap.SysProxyServices, snap.SysProxyExcludedServices)
	}

	// 开启系统代理后排除 USB 网卡，应清理该网卡代理并持久化偏好（且不覆盖主题偏好）
	if err := eng.SetSystemProxy(true); err != nil {
		t.Fatalf("SetSystemProxy(true): %v", err)
	}
	st, err := eng.SetSysProxyServiceEnabled("usb 10/100/1000 lan", false)
	if err != nil {
		t.Fatalf("exclude USB service: %v", err)
	}
	if len(st.Excluded) != 2 || st.Excluded[1] != "USB 10/100/1000 LAN" {
		t.Fatalf("unexpected excluded list: %+v", st.Excluded)
	}
	sp.mu.Lock()
	cleaned := append([]string(nil), sp.cleanedSvcs...)
	sp.mu.Unlock()
	if len(cleaned) != 1 || cleaned[0] != "USB 10/100/1000 LAN" {
		t.Fatalf("expected DisableServiceIfMatches on USB service, got %v", cleaned)
	}

	// 重新勾选 USB 网卡时因系统代理处于开启状态，应立即为其补开系统代理
	if _, err := eng.SetSysProxyServiceEnabled("USB 10/100/1000 LAN", true); err != nil {
		t.Fatalf("re-include USB service: %v", err)
	}
	sp.mu.Lock()
	reEnabled := append([]string(nil), sp.enabledSvcs...)
	sp.mu.Unlock()
	if len(reEnabled) != 1 || reEnabled[0] != "USB 10/100/1000 LAN" {
		t.Fatalf("expected EnableService on re-included USB service, got %v", reEnabled)
	}

	// 校验偏好文件既保存了最新排除列表，又保留了原有主题偏好
	prefs := config.LoadAppPrefs(prefsPath)
	if prefs.ThemeMode != "dark" || !prefs.ShowTraySpeed {
		t.Fatalf("existing app prefs lost: %+v", prefs)
	}
	if len(prefs.SysProxyExcludedServices) != 1 || prefs.SysProxyExcludedServices[0] != "Thunderbolt Bridge" {
		t.Fatalf("persisted excluded services mismatch: %+v", prefs.SysProxyExcludedServices)
	}
}

func TestEngineSysProxyServiceFilteringUnsupportedPlatform(t *testing.T) {
	cfgPath := writeTestConfig(t)
	eng, err := New(Options{
		ConfigPath:          cfgPath,
		CoreClient:          &mockCore{},
		SysProxy:            &mockSysProxy{},
		DisableAutoSysProxy: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Shutdown()

	if eng.Snapshot().SysProxyServiceSelectionSupported {
		t.Fatalf("unsupported sysproxy should report SysProxyServiceSelectionSupported=false")
	}
	st, err := eng.SysProxyServices(true)
	if err != nil || st.Supported {
		t.Fatalf("SysProxyServices on unsupported = (%+v, %v)", st, err)
	}
	if _, err := eng.SetSysProxyServiceEnabled("Wi-Fi", false); !errors.Is(err, sysproxy.ErrServiceSelectionUnsupported) {
		t.Fatalf("expected ErrServiceSelectionUnsupported, got %v", err)
	}
}

func TestEngineSyncSubscriptionIfStale(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	baseNow := time.Date(2026, 11, 27, 23, 0, 0, 0, time.UTC)
	staleMillis := baseNow.Add(-6 * time.Minute).UnixMilli()
	rawCfg := `{
  "subscribePassword": "mock-cloud-password",
  "proxyPort": 17892,
  "proxyType": 3,
  "lastUpdateTime": ` + strconv.FormatInt(staleMillis, 10) + `,
  "subscribeInfo": {
    "node": [{"id": 1, "name": "HK-01", "cnName": "香港节点", "host": "hk.example.invalid", "port": 443}],
    "user": {"trafficSize": 200000000000, "trafficUpload": 1000000000, "trafficDownload": 2000000000, "level": 3}
  },
  "usedNodeInfo": {"id": 1, "name": "HK-01", "cnName": "香港节点", "host": "hk.example.invalid", "port": 443}
}`
	if err := os.WriteFile(p, []byte(rawCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	mc := &mockCore{}
	sp := &mockSysProxy{}
	cc := &mockCloud{
		sub: config.SubscribeInfo{
			Nodes: []config.Node{
				{ID: 1, Name: "HK-01", CNName: "香港节点", Host: "hk.example.invalid", Port: 443},
			},
			User: &config.UserTrafficInfo{
				TrafficSize:     200000000000,
				TrafficUpload:   3000000000,
				TrafficDownload: 7000000000,
				Level:           3,
			},
		},
	}

	eng, err := New(Options{
		ConfigPath:          p,
		CoreClient:          mc,
		SysProxy:            sp,
		CloudClient:         cc,
		DisableAutoSysProxy: false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Shutdown()

	curTime := baseNow
	eng.mu.Lock()
	eng.nowFn = func() time.Time { return curTime }
	eng.mu.Unlock()

	// 1. 引擎尚未 Start（本地代理未启动）时跳过自动同步
	triggered, err := eng.SyncSubscriptionIfStale(context.Background(), DefaultAutoSyncInterval)
	if err != nil || triggered || cc.fetchCalls != 0 {
		t.Fatalf("before Start expected skip, got triggered=%v err=%v calls=%d", triggered, err, cc.fetchCalls)
	}

	// 启动本地代理（不自动连节点，单独测 SyncSubscriptionIfStale）
	if err := eng.Start(false); err != nil {
		t.Fatalf("Start(false): %v", err)
	}

	// 2. 距上次同步已超过 5 分钟，应触发一次静默同步并刷新流量
	triggered, err = eng.SyncSubscriptionIfStale(context.Background(), DefaultAutoSyncInterval)
	if err != nil || !triggered || cc.fetchCalls != 1 {
		t.Fatalf("first stale sync got triggered=%v err=%v calls=%d", triggered, err, cc.fetchCalls)
	}
	if snap := eng.Snapshot(); snap.Traffic == nil || snap.Traffic.TrafficDownload != 7000000000 {
		t.Fatalf("traffic not updated: %+v", snap.Traffic)
	}

	// 3. 3 分钟后（未满 5 分钟）再次触发应直接跳过
	curTime = curTime.Add(3 * time.Minute)
	triggered, err = eng.SyncSubscriptionIfStale(context.Background(), DefaultAutoSyncInterval)
	if err != nil || triggered || cc.fetchCalls != 1 {
		t.Fatalf("within 5m should skip, got triggered=%v err=%v calls=%d", triggered, err, cc.fetchCalls)
	}

	// 4. 再过 3 分钟（累计 6 分钟）触发，但本次云端失败；失败后 5 分钟冷却期内不应反复打请求
	curTime = curTime.Add(3 * time.Minute)
	cc.mu.Lock()
	cc.fetchErr = errors.New("mock cloud down")
	cc.mu.Unlock()

	triggered, err = eng.SyncSubscriptionIfStale(context.Background(), DefaultAutoSyncInterval)
	if err == nil || !triggered || cc.fetchCalls != 2 {
		t.Fatalf("expected failed attempt, got triggered=%v err=%v calls=%d", triggered, err, cc.fetchCalls)
	}

	curTime = curTime.Add(2 * time.Minute)
	triggered, err = eng.SyncSubscriptionIfStale(context.Background(), DefaultAutoSyncInterval)
	if err != nil || triggered || cc.fetchCalls != 2 {
		t.Fatalf("within failure cooldown should skip, got triggered=%v err=%v calls=%d", triggered, err, cc.fetchCalls)
	}

	// 5. 冷却期满且云端恢复后，应再次触发并成功刷新流量
	curTime = curTime.Add(4 * time.Minute)
	cc.mu.Lock()
	cc.fetchErr = nil
	cc.sub.User.TrafficDownload = 9000000000
	cc.mu.Unlock()

	triggered, err = eng.SyncSubscriptionIfStale(context.Background(), DefaultAutoSyncInterval)
	if err != nil || !triggered || cc.fetchCalls != 3 {
		t.Fatalf("after cooldown expected sync, got triggered=%v err=%v calls=%d", triggered, err, cc.fetchCalls)
	}
	if snap := eng.Snapshot(); snap.Traffic == nil || snap.Traffic.TrafficDownload != 9000000000 {
		t.Fatalf("traffic not updated after recovery: %+v", snap.Traffic)
	}
}

func TestEnginePingCurrentNodeRetryAndStatusReconcile(t *testing.T) {
	cfgPath := writeTestConfig(t)
	mc := &mockCore{
		delaySeq: map[int][]int64{
			// 首次抖动超时，复测成功返回 68ms
			1: {-1, 68},
		},
	}
	sp := &mockSysProxy{}
	eng, err := New(Options{
		ConfigPath:          cfgPath,
		CoreClient:          mc,
		SysProxy:            sp,
		DisableAutoSysProxy: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Shutdown()

	curTime := time.Date(2026, 11, 27, 23, 10, 0, 0, time.UTC)
	eng.mu.Lock()
	eng.pingRetryDelay = 0
	eng.nowFn = func() time.Time { return curTime }
	eng.mu.Unlock()

	if err := eng.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// 1. 当前节点单次抖动超时但复测成功：不应误报超时，顶部维持已连接
	delay, err := eng.PingNode(1)
	if err != nil || delay != 68 {
		t.Fatalf("expected retry delay 68, got (%d, %v)", delay, err)
	}
	if snap := eng.Snapshot(); snap.State != StateConnected {
		t.Fatalf("expected StateConnected after retry success, got %s", snap.State)
	}

	// 2. 当前节点连续复测均超时且无活跃流量（即使底层 TunnelStatus 仍为僵尸 0）：
	//    顶部状态应立即转为连接异常，且后续无流量的僵尸采样不能将其改回已连接
	mc.mu.Lock()
	mc.delaySeq = map[int][]int64{1: {-1, -1}}
	mc.delayCalls = map[int]int{}
	mc.upload = 0
	mc.download = 0
	mc.mu.Unlock()

	delay, err = eng.PingNode(1)
	if err != nil || delay != -1 {
		t.Fatalf("expected timeout (-1), got (%d, %v)", delay, err)
	}
	snap := eng.Snapshot()
	if snap.State != StateError || !strings.Contains(snap.LastError, "测速超时") || snap.TunnelPingMs != -1 {
		t.Fatalf("expected StateError on consecutive timeout, got state=%s err=%q ping=%d", snap.State, snap.LastError, snap.TunnelPingMs)
	}

	curTime = curTime.Add(time.Second)
	eng.sampleStats(curTime)
	if snap := eng.Snapshot(); snap.State != StateError {
		t.Fatalf("zombie status==0 with zero traffic must not revert to StateConnected, got %s", snap.State)
	}

	// 3. 测速恢复连通且底层 status==0：自动恢复已连接
	mc.mu.Lock()
	mc.delaySeq = map[int][]int64{1: {55}}
	mc.delayCalls = map[int]int{}
	mc.mu.Unlock()

	delay, err = eng.PingNode(1)
	if err != nil || delay != 55 {
		t.Fatalf("expected recovered delay 55, got (%d, %v)", delay, err)
	}
	snap = eng.Snapshot()
	if snap.State != StateConnected || snap.TunnelPingMs != 55 || snap.LastError != "" {
		t.Fatalf("expected StateConnected after delay recovery, got state=%s ping=%d err=%q", snap.State, snap.TunnelPingMs, snap.LastError)
	}

	// 4. 隧道存在近期活跃流量时，即使独立测速超时也不误判为断连
	mc.mu.Lock()
	mc.download = 128 * 1024
	mc.mu.Unlock()
	curTime = curTime.Add(time.Second)
	eng.sampleStats(curTime)

	mc.mu.Lock()
	mc.delaySeq = map[int][]int64{1: {-1, -1}}
	mc.delayCalls = map[int]int{}
	mc.mu.Unlock()
	curTime = curTime.Add(2 * time.Second)

	delay, err = eng.PingNode(1)
	if err != nil || delay != -1 {
		t.Fatalf("expected timeout (-1) during active traffic, got (%d, %v)", delay, err)
	}
	if snap := eng.Snapshot(); snap.State != StateConnected {
		t.Fatalf("active traffic tunnel should stay StateConnected, got %s", snap.State)
	}

	// 5. 超过活跃流量宽限期且测速超时转异常后，一旦再次观测到真实流量则自动恢复已连接
	mc.mu.Lock()
	mc.download = 0
	mc.delaySeq = map[int][]int64{1: {-1, -1}}
	mc.delayCalls = map[int]int{}
	mc.mu.Unlock()
	curTime = curTime.Add(10 * time.Second)

	_, _ = eng.PingNode(1)
	if snap := eng.Snapshot(); snap.State != StateError {
		t.Fatalf("expected StateError after grace window, got %s", snap.State)
	}

	mc.mu.Lock()
	mc.download = 4096
	mc.mu.Unlock()
	curTime = curTime.Add(time.Second)
	eng.sampleStats(curTime)
	if snap := eng.Snapshot(); snap.State != StateConnected {
		t.Fatalf("expected real traffic to clear probeFailed and restore StateConnected, got %s", snap.State)
	}
}
