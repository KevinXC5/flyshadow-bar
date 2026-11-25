package core

import (
	"archive/zip"
	"bytes"
	"net"
	"os"
	"path/filepath"
	"runtime"
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

func TestDarwinJarEntries(t *testing.T) {
	arm := darwinJarEntries("arm64")
	if len(arm) == 0 || arm[0] != DarwinJarEntry {
		t.Fatalf("darwinJarEntries(arm64) = %v, want %q", arm, DarwinJarEntry)
	}
	amd := darwinJarEntries("amd64")
	if len(amd) == 0 || amd[0] != DarwinAMD64JarEntry {
		t.Fatalf("darwinJarEntries(amd64) = %v, want %q", amd, DarwinAMD64JarEntry)
	}
}

func writeTestJar(t *testing.T, jarPath string, entries map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(jarPath), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(jarPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocateDarwinCoreLibPrefersOfficialJarAndRefreshesCache(t *testing.T) {
	barDir := t.TempDir()
	legacyDir := t.TempDir()
	appDir := t.TempDir()
	t.Setenv("FLYSHADOW_BAR_DIR", barDir)
	t.Setenv("FLYSHADOW_CORE_LIB", "")

	// 旧目录遗留旧版动态库（如 ~/.FlyShadow/lib/v2.0.2）
	legacyLib := filepath.Join(legacyDir, "lib", "v2.0.2", DarwinLibName)
	if err := os.MkdirAll(filepath.Dir(legacyLib), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyLib, []byte("legacy-v2.0.2"), 0o644); err != nil {
		t.Fatal(err)
	}

	entry := darwinJarEntries(runtime.GOARCH)[0]
	jarV1 := filepath.Join(appDir, "common-desktop-aaa.jar")
	writeTestJar(t, jarV1, map[string][]byte{
		entry: []byte("jar-v2.0.3-payload"),
	})

	origOfficial := officialJarsFinder
	origLegacy := legacyDirResolver
	origExec := executableDirsFinder
	t.Cleanup(func() {
		officialJarsFinder = origOfficial
		legacyDirResolver = origLegacy
		executableDirsFinder = origExec
	})
	currentJars := []string{jarV1}
	officialJarsFinder = func() []string { return currentJars }
	legacyDirResolver = func() (string, error) { return legacyDir, nil }
	executableDirsFinder = func() []string { return nil }

	// 1. 应优先从官方 JAR 提取，而非命中旧目录 ~/.FlyShadow/lib/v2.0.2
	got, err := locateDarwinCoreLib()
	if err != nil {
		t.Fatalf("locateDarwinCoreLib v1: %v", err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("jar-v2.0.3-payload")) {
		t.Fatalf("got payload %q, want jar-v2.0.3-payload", string(data))
	}

	// 2. 模拟通过 DMG 更新官方客户端：新 JAR 的 mtime 甚至早于当前缓存文件，仍必须刷新缓存
	jarV2 := filepath.Join(appDir, "common-desktop-bbb.jar")
	writeTestJar(t, jarV2, map[string][]byte{
		entry: []byte("jar-v2.0.4-new-payload"),
	})
	older := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(jarV2, older, older); err != nil {
		t.Fatal(err)
	}
	currentJars = []string{jarV2}

	got2, err := locateDarwinCoreLib()
	if err != nil {
		t.Fatalf("locateDarwinCoreLib v2: %v", err)
	}
	data2, err := os.ReadFile(got2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data2, []byte("jar-v2.0.4-new-payload")) {
		t.Fatalf("cache did not refresh on JAR update: got %q", string(data2))
	}

	// 3. 当官方 JAR 不存在时，按顺序回退至 ~/.FlyShadow/lib，再回退至已有缓存
	currentJars = nil
	gotLegacy, err := locateDarwinCoreLib()
	if err != nil {
		t.Fatalf("locateDarwinCoreLib legacy fallback: %v", err)
	}
	if gotLegacy != legacyLib {
		t.Fatalf("expected legacy fallback %s, got %s", legacyLib, gotLegacy)
	}
	if err := os.Remove(legacyLib); err != nil {
		t.Fatal(err)
	}
	gotCache, err := locateDarwinCoreLib()
	if err != nil {
		t.Fatalf("locateDarwinCoreLib cache fallback: %v", err)
	}
	if gotCache != got2 {
		t.Fatalf("expected cache fallback %s, got %s", got2, gotCache)
	}
}

func TestLocateGeoIPRefreshesFromOfficialJar(t *testing.T) {
	barDir := t.TempDir()
	legacyDir := t.TempDir()
	appDir := t.TempDir()
	t.Setenv("FLYSHADOW_BAR_DIR", barDir)
	t.Setenv("FLYSHADOW_GEOIP", "")

	jar1 := filepath.Join(appDir, "common-desktop-111.jar")
	writeTestJar(t, jar1, map[string][]byte{
		GeoIPJarEntry: []byte("geoip-v1"),
	})

	origOfficial := officialJarsFinder
	origLegacy := legacyDirResolver
	origExec := executableDirsFinder
	t.Cleanup(func() {
		officialJarsFinder = origOfficial
		legacyDirResolver = origLegacy
		executableDirsFinder = origExec
	})
	currentJars := []string{jar1}
	officialJarsFinder = func() []string { return currentJars }
	legacyDirResolver = func() (string, error) { return legacyDir, nil }
	executableDirsFinder = func() []string { return nil }

	_, b1, err := LocateGeoIP()
	if err != nil || !bytes.Equal(b1, []byte("geoip-v1")) {
		t.Fatalf("LocateGeoIP v1 = (%q, %v), want geoip-v1", string(b1), err)
	}

	jar2 := filepath.Join(appDir, "common-desktop-222.jar")
	writeTestJar(t, jar2, map[string][]byte{
		GeoIPJarEntry: []byte("geoip-v2-updated"),
	})
	currentJars = []string{jar2}

	_, b2, err := LocateGeoIP()
	if err != nil || !bytes.Equal(b2, []byte("geoip-v2-updated")) {
		t.Fatalf("LocateGeoIP v2 = (%q, %v), want geoip-v2-updated", string(b2), err)
	}
}

func TestLocateWindowsCoreLibSupportsSingleFlyShadowJarUpgrade(t *testing.T) {
	barDir := t.TempDir()
	legacyDir := t.TempDir()
	installDir := t.TempDir()
	appDir := filepath.Join(installDir, "app")
	t.Setenv("FLYSHADOW_BAR_DIR", barDir)
	t.Setenv("FLYSHADOW_CORE_LIB", "")

	// 模拟旧版 Windows 安装：app\common-desktop-*.jar 包含 2.0.2 DLL
	oldJar := filepath.Join(appDir, "common-desktop-oldhash.jar")
	writeTestJar(t, oldJar, map[string][]byte{
		WindowsJarEntry: []byte("win-core-2.0.2"),
		GeoIPJarEntry:   []byte("win-geoip-v1"),
	})

	origOfficial := officialJarsFinder
	origLegacy := legacyDirResolver
	origExec := executableDirsFinder
	origWinDirs := windowsInstallDirsFinder
	t.Cleanup(func() {
		officialJarsFinder = origOfficial
		legacyDirResolver = origLegacy
		executableDirsFinder = origExec
		windowsInstallDirsFinder = origWinDirs
	})
	officialJarsFinder = func() []string {
		return collectOfficialJarsFromDirs([]string{appDir, installDir})
	}
	legacyDirResolver = func() (string, error) { return legacyDir, nil }
	executableDirsFinder = func() []string { return nil }
	windowsInstallDirsFinder = func() []string { return []string{installDir} }

	got1, err := locateWindowsCoreLib()
	if err != nil {
		t.Fatalf("locateWindowsCoreLib old jar: %v", err)
	}
	data1, err := os.ReadFile(got1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data1, []byte("win-core-2.0.2")) {
		t.Fatalf("got payload %q, want win-core-2.0.2", string(data1))
	}

	// 模拟升级至 Windows 10.5.9：MSI 移除了 common-desktop-*.jar，合并为单一 app\FlyShadow.jar（即便 mtime 早于旧缓存也必须刷新）
	if err := os.Remove(oldJar); err != nil {
		t.Fatal(err)
	}
	uberJar := filepath.Join(appDir, "FlyShadow.jar")
	writeTestJar(t, uberJar, map[string][]byte{
		WindowsJarEntry: []byte("win-core-2.0.4-uberjar"),
		GeoIPJarEntry:   []byte("win-geoip-v2-uberjar"),
	})
	older := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(uberJar, older, older); err != nil {
		t.Fatal(err)
	}

	got2, err := locateWindowsCoreLib()
	if err != nil {
		t.Fatalf("locateWindowsCoreLib FlyShadow.jar: %v", err)
	}
	data2, err := os.ReadFile(got2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data2, []byte("win-core-2.0.4-uberjar")) {
		t.Fatalf("cache did not refresh from FlyShadow.jar: got %q", string(data2))
	}

	_, geoBytes, err := LocateGeoIP()
	if err != nil || !bytes.Equal(geoBytes, []byte("win-geoip-v2-uberjar")) {
		t.Fatalf("LocateGeoIP from FlyShadow.jar = (%q, %v), want win-geoip-v2-uberjar", string(geoBytes), err)
	}
}

func TestCollectOfficialJarsFromDirsDedupeAndSort(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	jarOld := filepath.Join(dir1, "common-desktop-111.jar")
	jarNew := filepath.Join(dir2, "FlyShadow.jar")
	if err := os.WriteFile(jarOld, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jarNew, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-1 * time.Hour)
	newTime := time.Now()
	_ = os.Chtimes(jarOld, oldTime, oldTime)
	_ = os.Chtimes(jarNew, newTime, newTime)

	got := collectOfficialJarsFromDirs([]string{dir1, dir2, dir1})
	if len(got) != 2 {
		t.Fatalf("got %d jars (%v), want 2", len(got), got)
	}
	if got[0] != jarNew || got[1] != jarOld {
		t.Fatalf("got order %v, want [%s %s]", got, jarNew, jarOld)
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
	for i := 0; i < 32; i++ {
		if c.TunnelStatus() == 0 {
			connected = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("隧道建连轮询结果: connected=%v status=%d ping=%dms up=%d down=%d",
		connected, c.TunnelStatus(), c.TunnelPingDelay(), c.TunnelUploadBytes(), c.TunnelDownloadBytes())
	if !connected {
		t.Fatalf("隧道在 8s 内未就绪 (status=%d)", c.TunnelStatus())
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
