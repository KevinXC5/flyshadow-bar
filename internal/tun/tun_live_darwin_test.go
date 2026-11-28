//go:build darwin

package tun

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/capture"
)

// TestLiveDarwinNativeUTUNWithHTTPSMITM 在 macOS 宿主机上安全实测真实原生 utun 虚拟网卡：
//   - 仅当显式设置 FLYSHADOW_LIVE_TUN_TEST=1 且以 root 运行时执行；
//   - 严格使用隔离保留网段 198.18.10.0/24（ScopedTestMode=true），绝不修改系统默认路由、公网路由或 100.64.0.0/10 远程隧道；
//   - 端到端验证不走系统代理的裸 TCP/HTTP、白名单 HTTPS MITM 明文解密、非白名单 HTTPS 透传、UDP DNS 以及关闭后路由 100% 清理。
func TestLiveDarwinNativeUTUNWithHTTPSMITM(t *testing.T) {
	if os.Getenv("FLYSHADOW_LIVE_TUN_TEST") != "1" {
		t.Skip("设置 FLYSHADOW_LIVE_TUN_TEST=1 且以 root 运行以执行 macOS 原生 utun 安全隔离实机测试")
	}
	if !IsAdmin() {
		t.Skip("需要 root 权限以创建原生 utun 设备")
	}

	// 1. 生成临时测试服务端自签证书（覆盖白名单域名 tun-mitm.example.test 与透传域名 tun-pass.example.test）
	srvCert, srvPool := generateTestServerCert(t, []string{"tun-mitm.example.test", "tun-pass.example.test"})

	// 2. 启动本地测试 HTTP 与 HTTPS 后端
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"scheme": "http",
			"path":   r.URL.RequestURI(),
			"host":   r.Host,
		})
	}))
	defer httpSrv.Close()
	httpBackendAddr := strings.TrimPrefix(httpSrv.URL, "http://")

	httpsSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"scheme": "https",
			"path":   r.URL.RequestURI(),
			"host":   r.Host,
			"secret": "decrypted-via-real-utun",
		})
	}))
	httpsSrv.TLS = &tls.Config{Certificates: []tls.Certificate{srvCert}}
	httpsSrv.StartTLS()
	defer httpsSrv.Close()
	httpsBackendAddr := strings.TrimPrefix(httpsSrv.URL, "https://")

	// 3. 启动本地测试上游 SOCKS5（支持 TCP 按端口分流到 HTTP/HTTPS 后端 + UDP DNS 应答）
	upstreamAddr, stopUpstream := startLiveTestSOCKS5(t, httpBackendAddr, httpsBackendAddr)
	defer stopUpstream()

	// 4. 准备临时本机 MITM CA 与真实 capture.Proxy（白名单仅加入 tun-mitm.example.test）
	caDir := filepath.Join(t.TempDir(), "mitm")
	caMgr := capture.NewCAManager(caDir)
	if err := caMgr.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	caPEM, err := os.ReadFile(caMgr.CertPath())
	if err != nil {
		t.Fatalf("read CA cert: %v", err)
	}
	mitmClientPool := x509.NewCertPool()
	if !mitmClientPool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("append MITM CA PEM failed")
	}

	store := capture.NewStore(128)
	frontProxy, err := capture.StartProxy(capture.ProxyOptions{
		ListenAddr:        "127.0.0.1:0",
		UpstreamAddr:      upstreamAddr,
		Store:             store,
		CA:                caMgr,
		MITMEnabled:       true,
		MITMHosts:         []string{"tun-mitm.example.test"},
		UpstreamTLSConfig: &tls.Config{RootCAs: srvPool},
	})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer frontProxy.Close()

	// 5. 启动真实 macOS 原生 utun（仅路由隔离测试网段 198.18.10.0/24，绝不触碰默认路由和远程连接）
	mgr, err := Start(Options{
		ProxyAddr:        func() string { return frontProxy.Addr() },
		CustomRouteCIDRs: []string{"198.18.10.0/24"},
	})
	if err != nil {
		t.Fatalf("tun.Start native utun failed: %v", err)
	}
	st := mgr.Status()
	t.Logf("真机原生 utun 已创建: dev=%s ip=%s routes=%d scoped=%v", st.DeviceName, st.TunIP, st.RouteCount, st.ScopedTestMode)
	if !st.Active || !strings.HasPrefix(st.DeviceName, "utun") || !st.ScopedTestMode {
		_ = mgr.Close()
		t.Fatalf("unexpected live utun status: %+v", st)
	}

	const testTargetIP = "198.18.10.88"

	// --- 验证 A：不配代理的裸 HTTP 请求经内核路由 -> 真 utun -> gVisor -> capture.Proxy ---
	httpClient := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy: nil, // 显式禁用任何代理，100% 走操作系统内核路由表进 utun
		},
	}
	reqHTTP, _ := http.NewRequest(http.MethodGet, "http://"+testTargetIP+"/tun-http?case=1", nil)
	reqHTTP.Host = "plain-tun.example.test"
	respHTTP, err := httpClient.Do(reqHTTP)
	if err != nil {
		_ = mgr.Close()
		t.Fatalf("裸 HTTP 经真 utun 请求失败: %v", err)
	}
	bodyHTTP, _ := io.ReadAll(respHTTP.Body)
	_ = respHTTP.Body.Close()
	if respHTTP.StatusCode != 200 || !strings.Contains(string(bodyHTTP), "/tun-http?case=1") {
		_ = mgr.Close()
		t.Fatalf("裸 HTTP 响应异常: status=%d body=%s", respHTTP.StatusCode, string(bodyHTTP))
	}
	t.Logf("[PASS] 裸 HTTP 经真 %s 成功: %s", st.DeviceName, strings.TrimSpace(string(bodyHTTP)))

	// --- 验证 B：不配代理的裸 HTTPS（白名单域名 tun-mitm.example.test）触发真 MITM 明文解密 ---
	mitmClient := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			TLSClientConfig: &tls.Config{
				RootCAs:    mitmClientPool, // 信任本机临时自签 CA
				ServerName: "tun-mitm.example.test",
			},
		},
	}
	reqMITM, _ := http.NewRequest(http.MethodGet, "https://"+testTargetIP+"/tun-secret?mode=mitm", nil)
	reqMITM.Host = "tun-mitm.example.test"
	respMITM, err := mitmClient.Do(reqMITM)
	if err != nil {
		_ = mgr.Close()
		t.Fatalf("裸 HTTPS (白名单 MITM) 经真 utun 请求失败: %v", err)
	}
	bodyMITM, _ := io.ReadAll(respMITM.Body)
	_ = respMITM.Body.Close()
	if respMITM.StatusCode != 200 || !strings.Contains(string(bodyMITM), "decrypted-via-real-utun") {
		_ = mgr.Close()
		t.Fatalf("裸 HTTPS MITM 响应异常: status=%d body=%s", respMITM.StatusCode, string(bodyMITM))
	}
	t.Logf("[PASS] 裸 HTTPS (白名单 MITM) 经真 %s 解密成功: %s", st.DeviceName, strings.TrimSpace(string(bodyMITM)))

	// --- 验证 C：不配代理的裸 HTTPS（非白名单域名 tun-pass.example.test）自动无侵入透传 ---
	passClient := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			TLSClientConfig: &tls.Config{
				RootCAs:    srvPool, // 仅信任源站证书，若被错误 MITM 握手会直接失败
				ServerName: "tun-pass.example.test",
			},
		},
	}
	reqPass, _ := http.NewRequest(http.MethodGet, "https://"+testTargetIP+"/tun-pass", nil)
	reqPass.Host = "tun-pass.example.test"
	respPass, err := passClient.Do(reqPass)
	if err != nil {
		_ = mgr.Close()
		t.Fatalf("裸 HTTPS (非白名单透传) 经真 utun 请求失败: %v", err)
	}
	bodyPass, _ := io.ReadAll(respPass.Body)
	_ = respPass.Body.Close()
	if respPass.StatusCode != 200 || !strings.Contains(string(bodyPass), "/tun-pass") {
		_ = mgr.Close()
		t.Fatalf("裸 HTTPS 透传响应异常: status=%d body=%s", respPass.StatusCode, string(bodyPass))
	}
	t.Logf("[PASS] 裸 HTTPS (非白名单透传) 经真 %s 透传成功: %s", st.DeviceName, strings.TrimSpace(string(bodyPass)))

	// --- 验证 D：不配代理的裸 UDP DNS 查询经真 utun -> gVisor -> SOCKS5 UDP -> capture.Proxy ---
	udpConn, err := net.DialTimeout("udp4", testTargetIP+":53", 5*time.Second)
	if err != nil {
		_ = mgr.Close()
		t.Fatalf("拨号 UDP DNS 失败: %v", err)
	}
	_ = udpConn.SetDeadline(time.Now().Add(5 * time.Second))
	dnsQuery := buildTestDNSQuery(0x4242, "dns-via-tun.example.test")
	if _, err := udpConn.Write(dnsQuery); err != nil {
		_ = udpConn.Close()
		_ = mgr.Close()
		t.Fatalf("发送 UDP DNS 查询失败: %v", err)
	}
	dnsResp := make([]byte, 512)
	dn, err := udpConn.Read(dnsResp)
	_ = udpConn.Close()
	if err != nil || dn < 12 {
		_ = mgr.Close()
		t.Fatalf("读取 UDP DNS 响应失败: n=%d err=%v", dn, err)
	}
	rec, ok := capture.ParseDNSPacket(dnsResp[:dn], time.Now())
	if !ok || rec.Direction != "answer" || len(rec.Answers) == 0 {
		_ = mgr.Close()
		t.Fatalf("UDP DNS 响应解析异常: ok=%v rec=%+v", ok, rec)
	}
	t.Logf("[PASS] 裸 UDP DNS 经真 %s 应答成功: %s -> %v", st.DeviceName, rec.Name, rec.Answers)

	// --- 验证 E：核查 capture.Store 全部 4 类记录是否完整捕获并正确解密 ---
	var sawHTTP, sawMITM, sawPass, sawDNS bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range store.Snapshot() {
			if e.Kind == capture.KindHTTP && strings.Contains(e.Host, "plain-tun.example.test") && strings.Contains(string(e.RespBody), "/tun-http?case=1") {
				sawHTTP = true
			}
			if e.Kind == capture.KindHTTPS && e.MITMDecrypted && strings.Contains(e.Host, "tun-mitm.example.test") &&
				strings.Contains(e.URL, "/tun-secret?mode=mitm") && strings.Contains(string(e.RespBody), "decrypted-via-real-utun") {
				sawMITM = true
			}
			if e.Kind == capture.KindHTTPS && !e.MITMDecrypted && strings.Contains(e.TLSSNI, "tun-pass.example.test") {
				sawPass = true
			}
			if e.Kind == capture.KindUDP && e.UDPProtocol == "DNS" && len(e.DNSRecords) > 0 {
				sawDNS = true
			}
		}
		if sawHTTP && sawMITM && sawPass && sawDNS {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawHTTP || !sawMITM || !sawPass || !sawDNS {
		_ = mgr.Close()
		t.Fatalf("抓包仓库校验不完整: http=%v mitm=%v pass=%v dns=%v entries=%+v",
			sawHTTP, sawMITM, sawPass, sawDNS, store.Snapshot())
	}
	t.Logf("[PASS] capture.Store 完整记录: HTTP明文=%v HTTPS白名单解密=%v HTTPS透传=%v UDP_DNS=%v",
		sawHTTP, sawMITM, sawPass, sawDNS)

	// --- 验证 F：关闭 TUN 后路由表与 utun 100% 干净还原 ---
	if err := mgr.Close(); err != nil {
		t.Fatalf("mgr.Close failed: %v", err)
	}
	routeOut, _ := exec.Command("/usr/sbin/netstat", "-rn", "-f", "inet").CombinedOutput()
	if strings.Contains(string(routeOut), "198.18.10") || strings.Contains(string(routeOut), st.DeviceName) {
		t.Fatalf("关闭 TUN 后仍残留测试路由或设备 %s:\n%s", st.DeviceName, string(routeOut))
	}
	t.Logf("[PASS] 关闭 TUN 后路由表已完全还原，未残留 198.18.10/24 或 %s", st.DeviceName)
}

func generateTestServerCert(t *testing.T, dnsNames []string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1001),
		Subject:               pkix.Name{CommonName: dnsNames[0]},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        parsed,
	}, pool
}

func startLiveTestSOCKS5(t *testing.T, httpTarget, httpsTarget string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				var hdr [2]byte
				if _, err := io.ReadFull(conn, hdr[:]); err != nil || hdr[0] != 0x05 {
					return
				}
				methods := make([]byte, int(hdr[1]))
				if _, err := io.ReadFull(conn, methods); err != nil {
					return
				}
				_, _ = conn.Write([]byte{0x05, 0x00})

				var reqHdr [4]byte
				if _, err := io.ReadFull(conn, reqHdr[:]); err != nil {
					return
				}
				switch reqHdr[3] {
				case 0x01:
					var b [4]byte
					_, _ = io.ReadFull(conn, b[:])
				case 0x03:
					var l [1]byte
					_, _ = io.ReadFull(conn, l[:])
					d := make([]byte, int(l[0]))
					_, _ = io.ReadFull(conn, d)
				case 0x04:
					var b [16]byte
					_, _ = io.ReadFull(conn, b[:])
				default:
					return
				}
				var p [2]byte
				if _, err := io.ReadFull(conn, p[:]); err != nil {
					return
				}
				dstPort := int(binary.BigEndian.Uint16(p[:]))

				switch reqHdr[1] {
				case 0x01: // TCP CONNECT
					target := httpTarget
					if dstPort == 443 {
						target = httpsTarget
					}
					up, err := net.DialTimeout("tcp", target, 5*time.Second)
					if err != nil {
						_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
						return
					}
					defer up.Close()
					_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0})
					done := make(chan struct{}, 2)
					go func() { _, _ = io.Copy(up, conn); done <- struct{}{} }()
					go func() { _, _ = io.Copy(conn, up); done <- struct{}{} }()
					<-done

				case 0x03: // UDP ASSOCIATE（返回模拟 DNS A 应答）
					udpSrv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
					if err != nil {
						return
					}
					defer udpSrv.Close()
					uPort := udpSrv.LocalAddr().(*net.UDPAddr).Port
					reply := []byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, byte(uPort >> 8), byte(uPort)}
					_, _ = conn.Write(reply)

					go func() {
						buf := make([]byte, 4096)
						for {
							n, raddr, uerr := udpSrv.ReadFromUDP(buf)
							if uerr != nil {
								return
							}
							host, port, payload, derr := decodeSOCKS5UDPDatagram(buf[:n])
							if derr != nil || len(payload) < 12 {
								continue
							}
							respDNS := buildTestDNSAnswer(payload, net.IPv4(198, 18, 10, 88).To4())
							pkt := encodeSOCKS5UDPDatagram(host, port, respDNS)
							_, _ = udpSrv.WriteToUDP(pkt, raddr)
						}
					}()
					_, _ = io.Copy(io.Discard, conn)
				}
			}(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func buildTestDNSQuery(txID uint16, domain string) []byte {
	var buf []byte
	buf = binary.BigEndian.AppendUint16(buf, txID)
	buf = binary.BigEndian.AppendUint16(buf, 0x0100) // RD=1
	buf = binary.BigEndian.AppendUint16(buf, 1)      // QDCOUNT=1
	buf = binary.BigEndian.AppendUint16(buf, 0)
	buf = binary.BigEndian.AppendUint16(buf, 0)
	buf = binary.BigEndian.AppendUint16(buf, 0)
	for _, lbl := range strings.Split(strings.TrimSuffix(domain, "."), ".") {
		buf = append(buf, byte(len(lbl)))
		buf = append(buf, lbl...)
	}
	buf = append(buf, 0x00)
	buf = binary.BigEndian.AppendUint16(buf, 1) // QTYPE=A
	buf = binary.BigEndian.AppendUint16(buf, 1) // QCLASS=IN
	return buf
}

func buildTestDNSAnswer(query []byte, answerIP net.IP) []byte {
	resp := make([]byte, 0, len(query)+16)
	resp = append(resp, query...)
	binary.BigEndian.PutUint16(resp[2:4], 0x8180) // QR=1, RD=1, RA=1
	binary.BigEndian.PutUint16(resp[6:8], 1)      // ANCOUNT=1
	// Name pointer 0xC00C, TYPE=A(1), CLASS=IN(1), TTL=60, RDLENGTH=4, RDATA=IPv4
	resp = append(resp, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04)
	resp = append(resp, answerIP.To4()...)
	return resp
}

var _ = fmt.Sprintf
