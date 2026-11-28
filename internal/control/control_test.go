package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
)

type stubCore struct {
	connected bool
	mode      core.ProxyMode
	nativeUDP bool
}

func (s *stubCore) LibPath() string                               { return "/stub" }
func (s *stubCore) Version() string                               { return "2.0.2" }
func (s *stubCore) Init() error                                   { return nil }
func (s *stubCore) SetClientUUID(string)                          {}
func (s *stubCore) SetProxyType(m core.ProxyMode)                 { s.mode = m }
func (s *stubCore) SetUDPProxyType(core.ProxyMode)                {}
func (s *stubCore) SetNativeUDP(v bool)                           { s.nativeUDP = v }
func (s *stubCore) SetIPv6Enable(bool)                            {}
func (s *stubCore) SetUseBuildInDNS(bool)                         {}
func (s *stubCore) SetDirectConnPriority(bool, int64)             {}
func (s *stubCore) SetTunModeEnable(bool)                         {}
func (s *stubCore) ClearDomainRules()                             {}
func (s *stubCore) SetDomainRule(string, int32, int32, int32)     {}
func (s *stubCore) SetGeoIP(string, []byte)                       {}
func (s *stubCore) StartProxy(int, bool, string, string) error    { return nil }
func (s *stubCore) StopProxy()                                    {}
func (s *stubCore) ConnectTunnel(string, int, string, bool) error { s.connected = true; return nil }
func (s *stubCore) CloseTunnel() int32                            { s.connected = false; return 0 }
func (s *stubCore) TestTunnelDelay(config.Node, bool) int64       { return 55 }
func (s *stubCore) TunnelUploadBytes() int64                      { return 1024 }
func (s *stubCore) TunnelDownloadBytes() int64                    { return 4096 }
func (s *stubCore) TunnelPingDelay() int32                        { return 33 }
func (s *stubCore) TunnelStatus() int32 {
	if s.connected {
		return 0
	}
	return 1
}
func (s *stubCore) TunnelConnections() ([]core.ConnectionInfo, error) {
	return []core.ConnectionInfo{
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 51234, TargetAddr: "github.com", TargetPort: 443, ProxyType: "Proxy", ActiveTime: 10},
	}, nil
}
func (s *stubCore) CloseConnection(string) {}
func (s *stubCore) Close()                 {}

type stubSysProxy struct {
	on bool
}

func (s *stubSysProxy) Enable(string, int) error            { s.on = true; return nil }
func (s *stubSysProxy) Disable() error                      { s.on = false; return nil }
func (s *stubSysProxy) IsEnabled(string, int) (bool, error) { return s.on, nil }

func TestControlServerAndClient(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	runPath := filepath.Join(dir, "flyshadow-bar.run.json")

	rawCfg := `{
  "proxyPort": 18890,
  "proxyType": 3,
  "udpProxyType": 3,
  "udpAccelerationEnabled": true,
  "nativeUdp": false,
  "subscribeInfo": {
    "node": [
      {"id": 10, "name": "SG-01", "cnName": "新加坡 01", "host": "127.0.0.1", "port": 2001, "password": "fake", "enableQuic": 1, "nativeUdp": 1},
      {"id": 20, "name": "US-01", "cnName": "美国 01", "host": "127.0.0.1", "port": 2002, "password": "fake", "enableQuic": 1, "nativeUdp": 0}
    ],
    "customRule": []
  },
  "usedNodeInfo": {"id": 10, "name": "SG-01", "cnName": "新加坡 01", "host": "127.0.0.1", "port": 2001, "password": "fake", "enableQuic": 1, "nativeUdp": 1}
}`
	if err := os.WriteFile(cfgPath, []byte(rawCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	eng, err := engine.New(engine.Options{
		ConfigPath: cfgPath,
		CoreClient: &stubCore{},
		SysProxy:   &stubSysProxy{},
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	if err := eng.Start(true); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	defer eng.Shutdown()

	srv, err := StartServer(eng, ServerOptions{RuntimePath: runPath})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	defer srv.Close()

	cli, err := Discover(runPath)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	ctx := context.Background()
	snap, err := cli.Status(ctx)
	if err != nil || snap.CurrentNode == nil || snap.CurrentNode.ID != 10 {
		t.Fatalf("Status = (%+v, %v)", snap, err)
	}

	snap, err = cli.SelectNode(ctx, "美国")
	if err != nil || snap.CurrentNode.ID != 20 {
		t.Fatalf("SelectNode = (%+v, %v)", snap, err)
	}

	snap, err = cli.SetMode(ctx, "global")
	if err != nil || snap.Mode != "global" {
		t.Fatalf("SetMode = (%+v, %v)", snap, err)
	}

	snap, err = cli.SetUDP(ctx, "off")
	if err != nil || snap.UDPAccelerationEnabled {
		t.Fatalf("SetUDP = (%+v, %v)", snap, err)
	}

	snap, err = cli.SetNativeUDP(ctx, "on")
	if err != nil || !snap.NativeUDP {
		t.Fatalf("SetNativeUDP = (%+v, %v)", snap, err)
	}

	snap, err = cli.SetSysProxy(ctx, "on")
	if err != nil || !snap.SystemProxy {
		t.Fatalf("SetSysProxy = (%+v, %v)", snap, err)
	}

	ranked, err := cli.Ping(ctx)
	if err != nil || len(ranked) != 2 || ranked[0].DelayMs != 55 {
		t.Fatalf("Ping = (%+v, %v)", ranked, err)
	}

	if _, err := cli.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// 验证自定义规则与连接监控控制 API
	added, err := cli.AddRule(ctx, config.DomainRule{
		Domain:    "openai.com",
		Matching:  config.MatchDomainSuffix,
		ProxyType: 2,
	})
	if err != nil || added.ID <= 0 {
		t.Fatalf("AddRule = (%+v, %v)", added, err)
	}
	rules, err := cli.Rules(ctx)
	if err != nil || len(rules) != 1 {
		t.Fatalf("Rules = (%+v, %v)", rules, err)
	}
	if enabled, err := cli.ToggleRule(ctx, added.ID); err != nil || enabled {
		t.Fatalf("ToggleRule = (%v, %v)", enabled, err)
	}
	if err := cli.DeleteRule(ctx, added.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	conns, err := cli.Connections(ctx)
	if err != nil || len(conns) != 1 {
		t.Fatalf("Connections = (%+v, %v)", conns, err)
	}
	if err := cli.CloseConnection(ctx, conns[0].CloseKey()); err != nil {
		t.Fatalf("CloseConnection: %v", err)
	}

	// 普通 stubSysProxy（模拟 Windows 全局系统代理）应返回 Supported=false 且修改排除列表返回错误
	svcs, err := cli.SysProxyServices(ctx)
	if err != nil || svcs.Supported {
		t.Fatalf("SysProxyServices on unsupported manager = (%+v, %v)", svcs, err)
	}
	if _, err := cli.SetSysProxyServices(ctx, "exclude", "USB 10/100/1000 LAN", nil); err == nil {
		t.Fatalf("SetSysProxyServices on unsupported manager expected error")
	}
}

type stubFilterSysProxy struct {
	stubSysProxy
	services []string
	excluded []string
}

func (s *stubFilterSysProxy) ListServices() ([]string, error) {
	return append([]string(nil), s.services...), nil
}
func (s *stubFilterSysProxy) SetExcludedServices(excluded []string) {
	s.excluded = append([]string(nil), excluded...)
}
func (s *stubFilterSysProxy) ExcludedServices() []string {
	return append([]string(nil), s.excluded...)
}
func (s *stubFilterSysProxy) EnableService(string, string, int) error { return nil }
func (s *stubFilterSysProxy) DisableServiceIfMatches(string, string, int) (bool, error) {
	return true, nil
}

func TestControlSysProxyServicesFilterAPI(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	runPath := filepath.Join(dir, "flyshadow-bar.run.json")
	if err := os.WriteFile(cfgPath, []byte(`{"proxyPort":18891,"proxyType":3,"subscribeInfo":{"node":[],"customRule":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	sp := &stubFilterSysProxy{services: []string{"Wi-Fi", "USB 10/100/1000 LAN"}}
	eng, err := engine.New(engine.Options{
		ConfigPath:          cfgPath,
		CoreClient:          &stubCore{},
		SysProxy:            sp,
		DisableAutoSysProxy: true,
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	defer eng.Shutdown()

	srv, err := StartServer(eng, ServerOptions{RuntimePath: runPath})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	defer srv.Close()

	cli, err := Discover(runPath)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	ctx := context.Background()
	st, err := cli.SysProxyServices(ctx)
	if err != nil || !st.Supported || len(st.Services) != 2 {
		t.Fatalf("SysProxyServices = (%+v, %v)", st, err)
	}

	st, err = cli.SetSysProxyServices(ctx, "exclude", "USB 10/100/1000 LAN", nil)
	if err != nil || len(st.Excluded) != 1 || st.Excluded[0] != "USB 10/100/1000 LAN" {
		t.Fatalf("exclude failed: (%+v, %v)", st, err)
	}

	st, err = cli.SetSysProxyServices(ctx, "include", "USB 10/100/1000 LAN", nil)
	if err != nil || len(st.Excluded) != 0 {
		t.Fatalf("include failed: (%+v, %v)", st, err)
	}
}
