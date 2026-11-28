package capture

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMatchMITMHost(t *testing.T) {
	wl := []string{"github.com", "*.openai.com", ".example.org"}
	cases := []struct {
		host string
		want bool
	}{
		{"github.com", true},
		{"GITHUB.COM:443", true},
		{"api.github.com", false},
		{"api.openai.com", true},
		{"a.b.openai.com", true},
		{"openai.com", true},
		{"sub.example.org", true},
		{"example.org", true},
		{"notexample.org", false},
	}
	for _, tc := range cases {
		if got := MatchMITMHost(tc.host, wl); got != tc.want {
			t.Errorf("MatchMITMHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestStoreRingBufferAndPause(t *testing.T) {
	st := NewStore(3)
	for i := 1; i <= 5; i++ {
		st.Add(Entry{Kind: KindHTTP, Host: "h" + strconv.Itoa(i)})
	}
	if st.Len() != 3 {
		t.Fatalf("expected ring len 3, got %d", st.Len())
	}
	snap := st.Snapshot()
	if snap[0].Host != "h5" || snap[2].Host != "h3" {
		t.Fatalf("unexpected ring order: %+v", snap)
	}

	st.SetPaused(true)
	if id := st.Add(Entry{Kind: KindHTTP, Host: "paused"}); id != 0 {
		t.Fatalf("paused store should drop entries, got id=%d", id)
	}
	st.Clear()
	if st.Len() != 0 {
		t.Fatalf("expected empty store after Clear")
	}
}

func TestCAManagerGenerateAndVerifyLeafCert(t *testing.T) {
	ca := NewCAManager(t.TempDir())
	if err := ca.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	pool, err := ca.CertPool()
	if err != nil {
		t.Fatalf("CertPool: %v", err)
	}
	leaf, err := ca.CertForHost("api.github.com")
	if err != nil {
		t.Fatalf("CertForHost: %v", err)
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{
		DNSName: "api.github.com",
		Roots:   pool,
	}); err != nil {
		t.Fatalf("leaf verify failed: %v", err)
	}
}

func TestWindowsCertutilFingerprintAndPlatformEnvSnippets(t *testing.T) {
	// Windows certutil 常输出带空格分隔的十六进制 SHA-1 哈希
	sample := "Cert Hash(sha1): 3e f3 4a 0e af f3 a7 37 19 75 c3 2c 71 aa 8c 50 11 22 33 44\r\n"
	if !matchWindowsCertutilFingerprint(sample, "", "3EF34A0EAFF3A7371975C32C71AA8C5011223344") {
		t.Fatalf("expected spaced sha1 fingerprint to match")
	}
	if matchWindowsCertutilFingerprint(sample, "", "0000000000000000000000000000000000000000") {
		t.Fatalf("unexpected match for wrong fingerprint")
	}

	winSnippets := buildEnvSnippetsForOS(`C:\Users\demo\.flyshadowbar\mitm\flyshadow-bar-ca.crt`, "windows")
	if len(winSnippets) < 3 || !strings.Contains(winSnippets[0].Value, "$env:NODE_EXTRA_CA_CERTS") || !strings.Contains(winSnippets[2].Value, "schannel") {
		t.Fatalf("unexpected Windows env snippets: %+v", winSnippets)
	}
	macSnippets := buildEnvSnippetsForOS(`/Users/demo/.flyshadowbar/mitm/flyshadow-bar-ca.crt`, "darwin")
	if len(macSnippets) < 3 || !strings.Contains(macSnippets[0].Value, "export NODE_EXTRA_CA_CERTS=") {
		t.Fatalf("unexpected macOS env snippets: %+v", macSnippets)
	}
}

func TestParseTLSClientHelloAndDNSAndSTUN(t *testing.T) {
	// 1. 通过真实 tls.Client 产生 ClientHello 验证 SNI / TLS 版本 / ALPN 解析
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		tc := tls.Client(c1, &tls.Config{
			ServerName: "demo.example.com",
			NextProtos: []string{"h2", "http/1.1"},
		})
		_ = tc.Handshake()
		_ = tc.Close()
	}()

	buf := make([]byte, 2048)
	n, err := c2.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("read ClientHello: n=%d err=%v", n, err)
	}
	sni, ver, alpn, ok := ParseTLSClientHello(buf[:n])
	if !ok || sni != "demo.example.com" || ver != "TLS 1.3" || !strings.Contains(alpn, "h2") {
		t.Fatalf("ParseTLSClientHello = (sni=%q ver=%q alpn=%q ok=%v)", sni, ver, alpn, ok)
	}

	// 2. 构造 DNS 响应报文（查询 api.example.com A -> 93.184.216.34，含名称指针压缩）
	dnsResp := buildMockDNSResponse("api.example.com", net.IPv4(93, 184, 216, 34))
	proto, _, rec, _ := InspectUDPPacket(53, dnsResp, "in", time.Now())
	if proto != "DNS" || rec == nil || rec.Name != "api.example.com" || rec.QType != "A" || len(rec.Answers) != 1 || rec.Answers[0] != "93.184.216.34" {
		t.Fatalf("unexpected DNS inspect: proto=%q rec=%+v", proto, rec)
	}

	// 3. 构造 STUN Binding Request
	stunPkt := make([]byte, 20)
	binary.BigEndian.PutUint16(stunPkt[0:2], 0x0001)
	binary.BigEndian.PutUint32(stunPkt[4:8], 0x2112A442)
	proto, summary, _, _ := InspectUDPPacket(3478, stunPkt, "out", time.Now())
	if proto != "STUN" || !strings.Contains(summary, "Binding Request") {
		t.Fatalf("unexpected STUN inspect: proto=%q summary=%q", proto, summary)
	}
}

func TestProxyHTTPAndMITMAndOpaqueHTTPSAndUDP(t *testing.T) {
	// 1. 启动目标明文 HTTP 服务器（返回 gzip JSON）
	httpTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("X-Test-Echo", string(body))
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte(`{"ok":true,"path":"` + r.URL.Path + `"}`))
		_ = gz.Close()
	}))
	defer httpTarget.Close()

	// 2. 启动目标 HTTPS 服务器（自签服务端证书，上游 TLS 信任它）
	httpsTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"secret":"mitm-visible","q":"` + r.URL.Query().Get("k") + `"}`))
	}))
	defer httpsTarget.Close()

	// 3. 启动模拟上游混合代理（支持 HTTP 转发、HTTP CONNECT、SOCKS5 CONNECT 与 SOCKS5 UDP ASSOCIATE）
	upAddr, stopUp := startMockUpstreamMixedProxy(t)
	defer stopUp()

	// 4. 启动被测 Go 前置抓包代理
	store := NewStore(100)
	ca := NewCAManager(t.TempDir())
	if err := ca.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	caPool, err := ca.CertPool()
	if err != nil {
		t.Fatalf("CertPool: %v", err)
	}

	httpsURL, _ := url.Parse(httpsTarget.URL)
	httpsHost, _, _ := net.SplitHostPort(httpsURL.Host)

	upTLSConf := &tls.Config{InsecureSkipVerify: true}
	proxy, err := StartProxy(ProxyOptions{
		ListenAddr:        "127.0.0.1:0",
		UpstreamAddr:      upAddr,
		Store:             store,
		CA:                ca,
		MITMEnabled:       true,
		MITMHosts:         []string{httpsHost}, // 仅将测试 HTTPS 主机加入白名单
		UpstreamTLSConfig: upTLSConf,
		ResolveProcess:    func(string) string { return "unit-test-proc" },
	})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer proxy.Close()

	proxyURL, _ := url.Parse("http://" + proxy.Addr())

	// 场景 A：明文 HTTP POST（无需证书，自动解压 gzip JSON 并记录 Query/Headers/Body）
	plainClient := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}
	req, _ := http.NewRequest(http.MethodPost, httpTarget.URL+"/v1/items?foo=bar&n=2", strings.NewReader(`{"hello":"world"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client", "fsbar")
	resp, err := plainClient.Do(req)
	if err != nil {
		t.Fatalf("plain HTTP via capture proxy: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// 场景 B：白名单内 HTTPS GET（MITM 解密，完整采集 Query/Headers/Body）
	mitmClient := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: caPool},
		},
		Timeout: 5 * time.Second,
	}
	resp2, err := mitmClient.Get(httpsTarget.URL + "/api/data?k=hello")
	if err != nil {
		t.Fatalf("MITM HTTPS via capture proxy: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if !strings.Contains(string(body2), "mitm-visible") {
		t.Fatalf("unexpected MITM response body: %s", string(body2))
	}

	// 场景 C：移出白名单后同一 HTTPS 目标走透明透传（不解密，客户端直接校验证书池=上游服务器证书）
	proxy.UpdateMITMConfig(true, []string{"other.example.com"})
	opaqueClient := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}
	resp3, err := opaqueClient.Get(httpsTarget.URL + "/api/opaque")
	if err != nil {
		t.Fatalf("opaque HTTPS via capture proxy: %v", err)
	}
	_, _ = io.ReadAll(resp3.Body)
	_ = resp3.Body.Close()
	opaqueClient.CloseIdleConnections()

	// 场景 D：SOCKS5 UDP Associate 发送并接收 DNS 数据报
	testSOCKS5UDPDNS(t, proxy.Addr())

	// 等待异步记录落入 Store
	time.Sleep(80 * time.Millisecond)

	entries := store.Snapshot()
	var gotHTTP, gotMITM, gotOpaque, gotUDP bool
	for _, e := range entries {
		switch {
		case e.Kind == KindHTTP && e.Path == "/v1/items":
			gotHTTP = true
			if e.ProcessName != "unit-test-proc" || e.StatusCode != 200 || len(e.QueryParams) != 2 {
				t.Fatalf("HTTP entry fields incomplete: %+v", e)
			}
			if !strings.Contains(string(e.RespBody), `"ok":true`) {
				t.Fatalf("expected gunzipped JSON resp body, got %q", string(e.RespBody))
			}
			if curl := BuildCURL(e); !strings.Contains(curl, "curl -X 'POST'") {
				t.Fatalf("unexpected curl command: %s", curl)
			}
		case e.Kind == KindHTTPS && e.MITMDecrypted && e.Path == "/api/data":
			gotMITM = true
			if e.StatusCode != 200 || len(e.QueryParams) != 1 || e.QueryParams[0].Value != "hello" || !strings.Contains(string(e.RespBody), "mitm-visible") {
				t.Fatalf("MITM HTTPS entry incomplete: %+v", e)
			}
		case e.Kind == KindHTTPS && !e.MITMDecrypted:
			gotOpaque = true
			if e.TLSVersion == "" {
				t.Fatalf("expected sniffed TLS version on opaque HTTPS entry: %+v", e)
			}
		case e.Kind == KindUDP && e.UDPProtocol == "DNS":
			gotUDP = true
			if e.PacketsSent < 1 || e.PacketsRecv < 1 || len(e.DNSRecords) < 2 {
				t.Fatalf("UDP DNS entry incomplete: %+v", e)
			}
		}
	}
	if !gotHTTP || !gotMITM || !gotOpaque || !gotUDP {
		t.Fatalf("missing captured entries: http=%v mitm=%v opaque=%v udp=%v (total=%d)", gotHTTP, gotMITM, gotOpaque, gotUDP, len(entries))
	}
}

func testSOCKS5UDPDNS(t *testing.T, proxyAddr string) {
	t.Helper()
	ctrl, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial socks5: %v", err)
	}
	defer ctrl.Close()

	_, _ = ctrl.Write([]byte{0x05, 0x01, 0x00})
	resp := make([]byte, 2)
	if _, err := io.ReadFull(ctrl, resp); err != nil || resp[1] != 0x00 {
		t.Fatalf("socks5 auth reply: %v %v", resp, err)
	}
	_, _ = ctrl.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	hdr := make([]byte, 10)
	if _, err := io.ReadFull(ctrl, hdr); err != nil || hdr[1] != 0x00 {
		t.Fatalf("socks5 udp associate reply: %v %v", hdr, err)
	}
	relayPort := int(binary.BigEndian.Uint16(hdr[8:10]))
	relayAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: relayPort}

	uconn, err := net.DialUDP("udp", nil, relayAddr)
	if err != nil {
		t.Fatalf("dial udp relay: %v", err)
	}
	defer uconn.Close()

	// 发送发往 8.8.8.8:53 的 DNS 查询包
	dnsQuery := buildMockDNSQuery("capture.test.invalid")
	pkt := make([]byte, 0, 10+len(dnsQuery))
	pkt = append(pkt, 0, 0, 0, 0x01, 8, 8, 8, 8, 0, 53)
	pkt = append(pkt, dnsQuery...)
	if _, err := uconn.Write(pkt); err != nil {
		t.Fatalf("write udp dns query: %v", err)
	}
	_ = uconn.SetReadDeadline(time.Now().Add(3 * time.Second))
	reply := make([]byte, 2048)
	if _, err := uconn.Read(reply); err != nil {
		t.Fatalf("read udp dns reply: %v", err)
	}
}

func startMockUpstreamMixedProxy(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveMockUpstreamConn(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func serveMockUpstreamConn(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	b, err := br.Peek(1)
	if err != nil || len(b) == 0 {
		return
	}
	if b[0] == 0x05 {
		// 极简 SOCKS5 UDP ASSOCIATE 模拟（收到 DNS 查询后直接回一个 DNS A 响应）
		hdr := make([]byte, 3)
		_, _ = io.ReadFull(br, hdr)
		_, _ = c.Write([]byte{0x05, 0x00})
		req := make([]byte, 10)
		if _, err := io.ReadFull(br, req); err != nil || req[1] != 0x03 {
			return
		}
		udpLn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			return
		}
		defer udpLn.Close()
		port := udpLn.LocalAddr().(*net.UDPAddr).Port
		_, _ = c.Write(buildSOCKS5AddrReply(net.IPv4(127, 0, 0, 1), port))

		go func() {
			buf := make([]byte, 4096)
			for {
				n, from, err := udpLn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				if _, _, payload, ok := parseSOCKS5UDPPacket(buf[:n]); ok {
					if q, ok := ParseDNSPacket(payload, time.Now()); ok {
						ans := buildMockDNSResponse(q.Name, net.IPv4(1, 2, 3, 4))
						out := make([]byte, 0, 10+len(ans))
						out = append(out, 0, 0, 0, 0x01, 8, 8, 8, 8, 0, 53)
						out = append(out, ans...)
						_, _ = udpLn.WriteToUDP(out, from)
					}
				}
			}
		}()

		tmp := make([]byte, 16)
		for {
			if _, err := c.Read(tmp); err != nil {
				return
			}
		}
	}

	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method == http.MethodConnect {
		target, err := net.DialTimeout("tcp", req.Host, 5*time.Second)
		if err != nil {
			_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			return
		}
		defer target.Close()
		_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_, _ = pipeBidirectional(&bufferedConn{Conn: c, r: br}, target)
		return
	}
	// 普通 HTTP 转发
	req.RequestURI = ""
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer resp.Body.Close()
	_ = resp.Write(c)
}

func buildMockDNSQuery(name string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	for _, label := range strings.Split(name, ".") {
		b.WriteByte(byte(len(label)))
		b.WriteString(label)
	}
	b.WriteByte(0)
	b.Write([]byte{0x00, 0x01, 0x00, 0x01}) // A, IN
	return b.Bytes()
}

func buildMockDNSResponse(name string, ip net.IP) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x12, 0x34, 0x81, 0x80, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00})
	for _, label := range strings.Split(name, ".") {
		b.WriteByte(byte(len(label)))
		b.WriteString(label)
	}
	b.WriteByte(0)
	b.Write([]byte{0x00, 0x01, 0x00, 0x01}) // QTYPE A, QCLASS IN
	// Answer: pointer to offset 12 (0xc00c), TYPE A, CLASS IN, TTL 60, RDLEN 4, IPv4
	b.Write([]byte{0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04})
	b.Write(ip.To4())
	return b.Bytes()
}

func TestProxyUpdateMITMConfigDropsOnlyAffectedTunnels(t *testing.T) {
	ca := NewCAManager(t.TempDir())
	if err := ca.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	p := &Proxy{
		opts:          ProxyOptions{CA: ca},
		activeTunnels: make(map[uint64]activeTunnel),
	}

	c1a, c1b := net.Pipe()
	defer c1a.Close()
	defer c1b.Close()
	u1a, u1b := net.Pipe()
	defer u1a.Close()
	defer u1b.Close()

	c2a, c2b := net.Pipe()
	defer c2a.Close()
	defer c2b.Close()
	u2a, u2b := net.Pipe()
	defer u2a.Close()
	defer u2b.Close()

	p.registerActiveTunnel(activeTunnel{host: "x.com", isTLS: true, mitm: false, clientConn: c1a, upConn: u1a})
	p.registerActiveTunnel(activeTunnel{host: "example.org", isTLS: true, mitm: false, clientConn: c2a, upConn: u2a})

	// 仅将 *.x.com 加入白名单：应精准断开 x.com 的旧透传连接，不影响 example.org
	if dropped := p.UpdateMITMConfig(true, []string{"*.x.com"}); dropped != 1 {
		t.Fatalf("expected 1 dropped tunnel, got %d", dropped)
	}
	_ = c1b.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 1)
	if _, err := c1b.Read(buf); err == nil {
		t.Fatalf("expected x.com tunnel peer to observe close")
	}
	// example.org 仍应保持未关闭
	_ = c2b.SetReadDeadline(time.Now().Add(40 * time.Millisecond))
	_, err := c2b.Read(buf)
	if err == nil {
		t.Fatalf("unexpected data on idle example.org tunnel")
	}
	if nerr, ok := err.(net.Error); !ok || !nerr.Timeout() {
		t.Fatalf("expected example.org tunnel to stay open (timeout), got %v", err)
	}
}

func TestProxyConnectAndSOCKS5FailureRecorded(t *testing.T) {
	// 模拟上游代理：HTTP CONNECT 返回 504，SOCKS5 CONNECT 返回 0x05（连接拒绝）
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	defer upLn.Close()
	go func() {
		for {
			c, err := upLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				br := bufio.NewReader(conn)
				b, err := br.Peek(1)
				if err != nil || len(b) == 0 {
					return
				}
				if b[0] == 0x05 {
					// SOCKS5 握手成功，但 CONNECT 返回拒绝
					hdr := make([]byte, 3)
					_, _ = io.ReadFull(br, hdr)
					_, _ = conn.Write([]byte{0x05, 0x00})
					reqHdr := make([]byte, 4)
					_, _ = io.ReadFull(br, reqHdr)
					_, _, _, _ = readSOCKS5Addr(br, reqHdr[3])
					time.Sleep(15 * time.Millisecond)
					_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
					return
				}
				_, _ = http.ReadRequest(br)
				time.Sleep(15 * time.Millisecond)
				_, _ = io.WriteString(conn, "HTTP/1.1 504 Gateway Timeout\r\nContent-Length: 0\r\n\r\n")
			}(c)
		}
	}()

	store := NewStore(DefaultMaxEntries)
	proxy, err := StartProxy(ProxyOptions{
		ListenAddr:   "127.0.0.1:0",
		UpstreamAddr: upLn.Addr().String(),
		Store:        store,
	})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer proxy.Close()

	// 1. HTTP CONNECT 504
	c1, err := net.DialTimeout("tcp", proxy.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy HTTP: %v", err)
	}
	_, _ = io.WriteString(c1, "CONNECT blocked.example:443 HTTP/1.1\r\nHost: blocked.example:443\r\n\r\n")
	_, _ = io.ReadAll(c1)
	_ = c1.Close()

	// 2. SOCKS5 CONNECT 失败（对应 TUN 模式 TCP 失败）
	c2, err := net.DialTimeout("tcp", proxy.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy SOCKS5: %v", err)
	}
	_, _ = c2.Write([]byte{0x05, 0x01, 0x00})
	resp := make([]byte, 2)
	_, _ = io.ReadFull(c2, resp)
	host := "tunfail.example"
	req := append([]byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}, []byte(host)...)
	req = append(req, 0x01, 0xbb) // port 443
	_, _ = c2.Write(req)
	reply := make([]byte, 10)
	_, _ = io.ReadFull(c2, reply)
	_ = c2.Close()

	snap := store.Snapshot()
	if len(snap) < 2 {
		t.Fatalf("expected at least 2 recorded error entries, got %d: %+v", len(snap), snap)
	}
	var gotHTTP, gotSOCKS bool
	for _, e := range snap {
		if e.Host == "blocked.example" && strings.Contains(e.Error, "504") && e.DurationMs >= 10 {
			gotHTTP = true
		}
		if e.Host == "tunfail.example" && e.Kind == KindHTTPS && e.Error != "" && e.DurationMs >= 10 {
			gotSOCKS = true
		}
	}
	if !gotHTTP || !gotSOCKS {
		t.Fatalf("expected both HTTP CONNECT 504 and SOCKS5 failure recorded with duration, gotHTTP=%v gotSOCKS=%v snap=%+v", gotHTTP, gotSOCKS, snap)
	}
}
