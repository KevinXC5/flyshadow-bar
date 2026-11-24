package core

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
)

func TestResolveTransportDecision(t *testing.T) {
	nBoth := config.Node{EnableQuic: 1, NativeUDP: 1}
	nQuicOnly := config.Node{EnableQuic: 1, NativeUDP: 0}
	nNone := config.Node{EnableQuic: 0, NativeUDP: 0}

	d := ResolveTransportDecision(true, true, nBoth)
	if !d.EnableQuic || !d.EnableNativeUDP {
		t.Fatalf("expected both true, got %+v", d)
	}
	d = ResolveTransportDecision(true, true, nQuicOnly)
	if !d.EnableQuic || d.EnableNativeUDP {
		t.Fatalf("expected quic=true nativeUdp=false, got %+v", d)
	}
	d = ResolveTransportDecision(false, true, nBoth)
	if d.EnableQuic || !d.EnableNativeUDP {
		t.Fatalf("expected quic=false nativeUdp=true, got %+v", d)
	}
	d = ResolveTransportDecision(true, true, nNone)
	if d.EnableQuic || d.EnableNativeUDP {
		t.Fatalf("expected both false, got %+v", d)
	}
}

func TestFindLatestVersionedLib(t *testing.T) {
	root := t.TempDir()
	for _, ver := range []string{"v1.9.10", "v2.0.2", "v2.0.10", "v2.0.1"} {
		dir := filepath.Join(root, ver)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, DarwinLibName), []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := findLatestVersionedLib(root, DarwinLibName)
	if !ok {
		t.Fatalf("expected to find lib")
	}
	want := filepath.Join(root, "v2.0.10", DarwinLibName)
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestParseProxyMode(t *testing.T) {
	cases := map[string]ProxyMode{
		"rule":   ProxyModeRule,
		"GLOBAL": ProxyModeGlobal,
		"direct": ProxyModeDirect,
		"规则":     ProxyModeRule,
	}
	for input, want := range cases {
		got, err := ParseProxyMode(input)
		if err != nil || got != want {
			t.Fatalf("ParseProxyMode(%q) = (%v, %v), want %v", input, got, err, want)
		}
	}
	if _, err := ParseProxyMode("invalid"); err == nil {
		t.Fatalf("expected error for invalid mode")
	}
}

// TestLiveCoreSmoke 仅在显式设置 FLYSHADOW_LIVE_TEST=1 时运行，使用空闲随机高端口，绝不干扰 7890。
// 严格禁止输出任何节点密码、host 或用户敏感信息。
func TestLiveCoreSmoke(t *testing.T) {
	if os.Getenv("FLYSHADOW_LIVE_TEST") != "1" {
		t.Skip("设置 FLYSHADOW_LIVE_TEST=1 以运行真机核心库冒烟测试")
	}

	c, err := Open("")
	if err != nil {
		t.Fatalf("Open core lib: %v", err)
	}
	defer c.Close()

	ver := c.Version()
	if ver == "" {
		t.Fatalf("expected non-empty core version")
	}
	t.Logf("核心库加载成功，版本: %s", ver)

	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if geoPath, geoBytes, err := LocateGeoIP(); err == nil {
		c.SetGeoIP("CN", geoBytes)
		t.Logf("GeoIP 加载成功 (%d 字节, path=%s)", len(geoBytes), filepath.Base(geoPath))
	} else {
		t.Logf("GeoIP 未找到（可忽略）: %v", err)
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Skipf("本机未找到 FlyShadow 配置，跳过在线建连验证: %v", err)
	}
	if cfg.UUID != "" {
		c.SetClientUUID(cfg.UUID)
	}
	c.SetProxyType(ProxyMode(cfg.ProxyType))
	c.SetUDPProxyType(ProxyMode(cfg.UDPProxyType))
	c.SetIPv6Enable(cfg.IPv6Enable)
	c.SetUseBuildInDNS(cfg.UseBuildInDNS)
	c.SetDirectConnPriority(cfg.DirectConnPriority, cfg.DirectConnPriorityTimeout)

	rules := cfg.ActiveRules()
	c.ClearDomainRules()
	for _, r := range rules {
		c.SetDomainRule(r.Domain, int32(r.Matching), int32(r.ProxyType), int32(r.DirectConnPriority))
	}
	t.Logf("已加载域名分流规则: %d 条", len(rules))

	// 挑选一个空闲临时端口验证代理启动与停止，绝不占用 7890
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate temp port: %v", err)
	}
	tempPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	if err := c.StartProxy(tempPort, false, "", ""); err != nil {
		t.Fatalf("StartProxy on temp port %d: %v", tempPort, err)
	}
	defer c.StopProxy()

	// 确认端口确实在监听
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", "0"), 100*time.Millisecond)
	_ = conn
	conn, err = net.DialTimeout("tcp", (&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: tempPort}).String(), 2*time.Second)
	if err != nil {
		t.Fatalf("local proxy port %d not listening: %v", tempPort, err)
	}
	_ = conn.Close()
	t.Logf("临时本地代理监听成功 (127.0.0.1:%d)", tempPort)

	node := cfg.CurrentNode()
	if node == nil {
		t.Skip("配置中无节点")
	}

	decision := ResolveTransportDecision(cfg.UDPAccelerationEnabled, cfg.NativeUDP, *node)
	delay := c.TestTunnelDelay(*node, decision.EnableQuic)
	t.Logf("当前节点延迟测试完成: nodeID=%d quic=%v delay=%dms", node.ID, decision.EnableQuic, delay)

	c.SetNativeUDP(decision.EnableNativeUDP)
	if err := c.ConnectTunnel(node.Host, node.Port, node.Password, decision.EnableQuic); err != nil {
		t.Fatalf("ConnectTunnel: %v", err)
	}
	connected := false
	for i := 0; i < 16; i++ {
		if c.TunnelStatus() == 0 {
			connected = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("隧道建连轮询结果: connected=%v status=%d ping=%dms up=%d down=%d",
		connected, c.TunnelStatus(), c.TunnelPingDelay(), c.TunnelUploadBytes(), c.TunnelDownloadBytes())
	if !connected {
		t.Fatalf("隧道在 4s 内未就绪 (status=%d)", c.TunnelStatus())
	}

	// 验证 UDP 开关热切换重连
	c.SetNativeUDP(!decision.EnableNativeUDP)
	if err := c.ConnectTunnel(node.Host, node.Port, node.Password, !decision.EnableQuic); err != nil {
		t.Fatalf("ConnectTunnel hot toggle: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	t.Logf("UDP 开关热切后状态: status=%d", c.TunnelStatus())

	c.CloseTunnel()
}
