package tun

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"

	"github.com/KevinXC5/flyshadow-bar/internal/capture"
)

func TestRoutePlanSafetyAndParsing(t *testing.T) {
	takeover := DefaultTakeoverCIDRs()
	if len(takeover) != 8 {
		t.Fatalf("expected 8 non-destructive takeover CIDRs, got %d", len(takeover))
	}
	for _, c := range takeover {
		if c == "0.0.0.0/0" {
			t.Fatalf("takeover routes must not overwrite default 0.0.0.0/0")
		}
	}

	bypass := DefaultBypassCIDRs()
	hasCGNAT := false
	for _, c := range bypass {
		if c == "100.64.0.0/10" {
			hasCGNAT = true
		}
	}
	if !hasCGNAT {
		t.Fatalf("bypass routes must protect 100.64.0.0/10 (Tailscale/CGNAT), got %v", bypass)
	}

	parsed := ParseCIDRList("198.18.0.0/16, 1.1.1.1, invalid, 198.18.0.0/16")
	if len(parsed) != 2 || parsed[0] != "198.18.0.0/16" || parsed[1] != "1.1.1.1/32" {
		t.Fatalf("unexpected parsed CIDRs: %v", parsed)
	}

	netAddr, mask, prefix, err := CIDRToNetworkMask("100.64.0.0/10")
	if err != nil || netAddr != "100.64.0.0" || mask != "255.192.0.0" || prefix != 10 {
		t.Fatalf("CIDRToNetworkMask mismatch: %s %s %d %v", netAddr, mask, prefix, err)
	}

	ips := ResolveBypassIPv4s([]string{"127.0.0.1", "192.168.1.1", "8.8.8.8:443", "1.1.1.1"})
	if len(ips) != 2 || ips[0] != "1.1.1.1" || ips[1] != "8.8.8.8" {
		t.Fatalf("ResolveBypassIPv4s mismatch: %v", ips)
	}
}

func TestHelperFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writeHelperJSON(&buf, helperFrameReady, helperReadyMsg{
		DeviceName:    "utun9",
		MTU:           1500,
		OutboundIface: "en1",
		Gateway:       "192.168.4.1",
		RouteCount:    12,
	}); err != nil {
		t.Fatalf("writeHelperJSON failed: %v", err)
	}
	payloadIn := []byte{0x45, 0x00, 0x00, 0x1c, 1, 2, 3, 4}
	if err := writeHelperFrame(&buf, helperFramePacket, payloadIn); err != nil {
		t.Fatalf("writeHelperFrame failed: %v", err)
	}

	typ, p1, err := readHelperFrame(&buf, nil)
	if err != nil || typ != helperFrameReady || !strings.Contains(string(p1), "utun9") {
		t.Fatalf("unexpected ready frame: typ=%d payload=%q err=%v", typ, string(p1), err)
	}
	typ2, p2, err := readHelperFrame(&buf, make([]byte, 64))
	if err != nil || typ2 != helperFramePacket || !bytes.Equal(p2, payloadIn) {
		t.Fatalf("unexpected packet frame: typ=%d payload=%x err=%v", typ2, p2, err)
	}
}

func TestExtractHTTPHostAndDNSCache(t *testing.T) {
	req := []byte("GET /v1/ping HTTP/1.1\r\nHost: api.example.test:8080\r\nUser-Agent: test\r\n\r\n")
	if got := extractHTTPHostHeader(req); got != "api.example.test" {
		t.Fatalf("expected api.example.test, got %q", got)
	}

	cache := newDNSCache(16, time.Minute)
	cache.Put("203.0.113.10", "Service.Example.Test.")
	if got := cache.Lookup("203.0.113.10"); got != "service.example.test" {
		t.Fatalf("dns cache lookup mismatch: %q", got)
	}
}

// memPacketDevice 构造纯内存虚拟 L3 网卡，用于跨平台端到端验证 gVisor netstack -> capture.Proxy -> 目标服务完整链路。
type memPacketDevice struct {
	name   string
	mtu    int
	inCh   chan []byte
	outCh  chan []byte
	closed chan struct{}
	once   sync.Once
}

func newMemPacketDevice() *memPacketDevice {
	return &memPacketDevice{
		name:   "utun-mem0",
		mtu:    1500,
		inCh:   make(chan []byte, 256),
		outCh:  make(chan []byte, 256),
		closed: make(chan struct{}),
	}
}

func (d *memPacketDevice) Name() string          { return d.name }
func (d *memPacketDevice) MTU() int              { return d.mtu }
func (d *memPacketDevice) OutboundIface() string { return "en0" }
func (d *memPacketDevice) Gateway() string       { return "192.168.1.1" }
func (d *memPacketDevice) RouteCount() int       { return 8 }
func (d *memPacketDevice) AddBypassIP(string) error {
	return nil
}

func (d *memPacketDevice) ReadPacket(buf []byte) (int, error) {
	select {
	case pkt := <-d.inCh:
		return copy(buf, pkt), nil
	case <-d.closed:
		return 0, io.EOF
	}
}

func (d *memPacketDevice) WritePacket(pkt []byte) (int, error) {
	cp := make([]byte, len(pkt))
	copy(cp, pkt)
	select {
	case d.outCh <- cp:
		return len(pkt), nil
	case <-d.closed:
		return 0, io.ErrClosedPipe
	}
}

func (d *memPacketDevice) Close() error {
	d.once.Do(func() { close(d.closed) })
	return nil
}

// TestTunManagerEndToEndHTTPAndCapture 验证纯 Go TUN 协议栈将原始 IPv4 TCP 流还原后，
// 通过 SOCKS5 送入 capture.Proxy 抓包并成功转发到目标 HTTP 服务，同时提取 Host 域名。
func TestTunManagerEndToEndHTTPAndCapture(t *testing.T) {
	// 1. 启动模拟目标 HTTP 服务
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"via":"tun-e2e"}`))
	}))
	defer backend.Close()
	backendAddr := strings.TrimPrefix(backend.URL, "http://")

	// 2. 启动极简测试上游 SOCKS5 服务（模拟 FlyShadow 核心库回环端口）
	upstreamAddr, stopUpstream := startTestSOCKS5Server(t, backendAddr)
	defer stopUpstream()

	// 3. 启动 capture.Proxy 前置抓包代理（模拟用户开启抓包录制）
	store := capture.NewStore(64)
	frontProxy, err := capture.StartProxy(capture.ProxyOptions{
		ListenAddr:   "127.0.0.1:0",
		UpstreamAddr: upstreamAddr,
		Store:        store,
	})
	if err != nil {
		t.Fatalf("StartProxy failed: %v", err)
	}
	defer frontProxy.Close()

	// 4. 启动 TUN Manager（使用内存虚拟网卡，不触碰宿主机路由）
	memDev := newMemPacketDevice()
	mgr, err := Start(Options{
		ProxyAddr: func() string { return frontProxy.Addr() },
		OpenDevice: func(DeviceConfig) (PacketDevice, error) {
			return memDev, nil
		},
	})
	if err != nil {
		t.Fatalf("tun.Start failed: %v", err)
	}
	defer mgr.Close()

	// 5. 构造客户端侧 gVisor 协议栈，把 IPv4 包注入 memDev，经由 TUN Manager 发起标准 HTTP 请求
	clientStack := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	defer clientStack.Close()

	clientEP := channel.New(256, 1500, "")
	if tcpErr := clientStack.CreateNIC(1, clientEP); tcpErr != nil {
		t.Fatalf("client CreateNIC failed: %s", tcpErr)
	}
	clientAddr := tcpip.AddrFromSlice(net.IPv4(198, 18, 0, 2).To4())
	if tcpErr := clientStack.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: clientAddr.WithPrefix(),
	}, stack.AddressProperties{}); tcpErr != nil {
		t.Fatalf("client AddProtocolAddress failed: %s", tcpErr)
	}
	clientStack.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 客户端出站包 -> memDev.inCh（进入 TUN Manager）
	go func() {
		for {
			pkt := clientEP.ReadContext(ctx)
			if pkt == nil {
				return
			}
			view := pkt.ToView()
			cp := append([]byte(nil), view.AsSlice()...)
			view.Release()
			pkt.DecRef()
			select {
			case memDev.inCh <- cp:
			case <-ctx.Done():
				return
			}
		}
	}()
	// TUN Manager 回包 -> clientEP.InjectInbound
	go func() {
		for {
			select {
			case raw := <-memDev.outCh:
				pktBuf := stack.NewPacketBuffer(stack.PacketBufferOptions{
					Payload: buffer.MakeWithData(raw),
				})
				clientEP.InjectInbound(ipv4.ProtocolNumber, pktBuf)
				pktBuf.DecRef()
			case <-ctx.Done():
				return
			}
		}
	}()

	// 客户端向虚构公网 IP 203.0.113.55:80 发起明文 HTTP 请求，携带真实 Host 头
	dstIP := tcpip.AddrFromSlice(net.IPv4(203, 0, 113, 55).To4())
	conn, err := gonet.DialContextTCP(ctx, clientStack, tcpip.FullAddress{
		NIC:  1,
		Addr: dstIP,
		Port: 80,
	}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("gonet.DialContextTCP via TUN failed: %v", err)
	}
	defer conn.Close()

	httpReq := "GET /hello?from=tun HTTP/1.1\r\nHost: tun-demo.example.test\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(httpReq)); err != nil {
		t.Fatalf("write HTTP request failed: %v", err)
	}
	respBytes, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read HTTP response failed: %v", err)
	}
	if !strings.Contains(string(respBytes), `"via":"tun-e2e"`) {
		t.Fatalf("unexpected HTTP response via TUN: %s", string(respBytes))
	}

	// 核验抓包仓库已自动记录该流量且准确提取出了真实域名（而非仅显示 203.0.113.55）
	deadline := time.Now().Add(2 * time.Second)
	var matched bool
	for time.Now().Before(deadline) {
		for _, e := range store.Snapshot() {
			if strings.Contains(e.Host, "tun-demo.example.test") {
				matched = true
				break
			}
		}
		if matched {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !matched {
		t.Fatalf("expected capture store to record tun-demo.example.test via TUN, got %+v", store.Snapshot())
	}
}

// startTestSOCKS5Server 启动最小化测试 SOCKS5 服务端，将所有 CONNECT 重定向至 redirectTarget。
func startTestSOCKS5Server(t *testing.T, redirectTarget string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen test socks5 failed: %v", err)
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
				if _, err := io.ReadFull(conn, reqHdr[:]); err != nil || reqHdr[1] != 0x01 {
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
				_ = binary.BigEndian.Uint16(p[:])

				up, err := net.DialTimeout("tcp", redirectTarget, 3*time.Second)
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
			}(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestResolveTakeoverCIDRsEnv(t *testing.T) {
	t.Setenv(TestCIDRsEnv, "198.18.0.0/16,1.1.1.1")
	cidrs, scoped := ResolveTakeoverCIDRs(nil)
	if !scoped || len(cidrs) != 2 {
		t.Fatalf("expected scoped test CIDRs, got scoped=%v cidrs=%v", scoped, cidrs)
	}
	if errors.Is(ErrAdminRequired, ErrUnsupported) {
		t.Fatalf("sentinel errors must be distinct")
	}
}
