package tun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/KevinXC5/flyshadow-bar/internal/capture"
)

const (
	tunNICID            tcpip.NICID = 1
	channelQueueSize                = 512
	tcpForwarderMaxConn             = 1024
	tcpReceiveWindow                = 2 * 1024 * 1024
	udpIdleTimeout                  = 45 * time.Second
	tcpSniffTimeout                 = 120 * time.Millisecond
)

// Manager 负责管理纯 Go TUN 虚拟网卡、gVisor 用户态协议栈以及到本机代理的 TCP/UDP 转发生命周期。
type Manager struct {
	opts Options

	mu     sync.RWMutex
	status Status
	dev    PacketDevice
	stk    *stack.Stack
	chEP   *channel.Endpoint
	cancel context.CancelFunc
	dns    *dnsCache

	closed atomic.Bool
	wg     sync.WaitGroup
}

// Start 启动虚拟网卡与纯 Go TCP/IP 协议栈。
func Start(opts Options) (*Manager, error) {
	if opts.ProxyAddr == nil {
		return nil, errors.New("TUN 缺少上游代理地址回调")
	}
	tunIP := strings.TrimSpace(opts.TunIP)
	if tunIP == "" {
		tunIP = DefaultTunIP
	}
	mtu := opts.MTU
	if mtu <= 0 {
		mtu = DefaultMTU
	}

	takeoverCIDRs, scoped := ResolveTakeoverCIDRs(opts.CustomRouteCIDRs)
	var bypassCIDRs []string
	var bypassIPs []string
	if !scoped {
		bypassCIDRs = DefaultBypassCIDRs()
		if opts.BypassHosts != nil {
			bypassIPs = ResolveBypassIPv4s(opts.BypassHosts())
		}
	}

	devCfg := DeviceConfig{
		TunIP:          tunIP,
		PrefixLen:      DefaultTunPrefix,
		MTU:            mtu,
		TakeoverCIDRs:  takeoverCIDRs,
		BypassCIDRs:    bypassCIDRs,
		BypassIPs:      bypassIPs,
		ScopedTestMode: scoped,
		AdapterName:    "FlyShadow Bar TUN",
		WinTunDLLPath:  opts.WinTunDLLPath,
		OnAuthComplete: opts.OnAuthComplete,
	}

	openFn := opts.OpenDevice
	if openFn == nil {
		openFn = openPlatformDevice
	}
	dev, err := openFn(devCfg)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		opts:   opts,
		dev:    dev,
		cancel: cancel,
		dns:    newDNSCache(2048, 10*time.Minute),
		status: Status{
			Active:         true,
			DeviceName:     dev.Name(),
			TunIP:          tunIP,
			Gateway:        dev.Gateway(),
			OutboundIface:  dev.OutboundIface(),
			RouteCount:     dev.RouteCount(),
			ScopedTestMode: scoped,
		},
	}

	if err := m.initNetstack(ctx, mtu); err != nil {
		cancel()
		_ = dev.Close()
		return nil, err
	}

	log.Printf("[tun] 虚拟网卡已就绪: dev=%s ip=%s mtu=%d out=%s gw=%s routes=%d scoped=%v",
		dev.Name(), tunIP, dev.MTU(), dev.OutboundIface(), dev.Gateway(), dev.RouteCount(), scoped)
	return m, nil
}

func (m *Manager) initNetstack(ctx context.Context, mtu int) error {
	chEP := channel.New(channelQueueSize, uint32(mtu), "")
	stk := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			ipv6.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
			udp.NewProtocol,
			icmp.NewProtocol4,
			icmp.NewProtocol6,
		},
	})

	if tcpErr := stk.CreateNIC(tunNICID, chEP); tcpErr != nil {
		chEP.Close()
		stk.Close()
		return fmt.Errorf("创建 netstack NIC 失败: %s", tcpErr.String())
	}
	if tcpErr := stk.SetPromiscuousMode(tunNICID, true); tcpErr != nil {
		chEP.Close()
		stk.Close()
		return fmt.Errorf("开启 netstack 混杂模式失败: %s", tcpErr.String())
	}
	if tcpErr := stk.SetSpoofing(tunNICID, true); tcpErr != nil {
		chEP.Close()
		stk.Close()
		return fmt.Errorf("开启 netstack Spoofing 失败: %s", tcpErr.String())
	}

	stk.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: tunNICID},
		{Destination: header.IPv6EmptySubnet, NIC: tunNICID},
	})

	// 启用 TCP SACK 提升弱网与高延迟链路吞吐
	sack := tcpip.TCPSACKEnabled(true)
	_ = stk.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)

	tcpFwd := tcp.NewForwarder(stk, tcpReceiveWindow, tcpForwarderMaxConn, func(r *tcp.ForwarderRequest) {
		m.handleTCPForwarder(ctx, r)
	})
	stk.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	udpFwd := udp.NewForwarder(stk, func(r *udp.ForwarderRequest) bool {
		return m.handleUDPForwarder(ctx, r)
	})
	stk.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	m.stk = stk
	m.chEP = chEP

	// 桥接循环 1：虚拟网卡入站 IP 包 -> gVisor netstack
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.readFromDeviceLoop(ctx)
	}()

	// 桥接循环 2：gVisor netstack 出站 IP 包 -> 虚拟网卡
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.writeToDeviceLoop(ctx)
	}()

	return nil
}

func (m *Manager) readFromDeviceLoop(ctx context.Context) {
	buf := make([]byte, max(m.dev.MTU()+64, 65536))
	for {
		n, err := m.dev.ReadPacket(buf)
		if err != nil {
			if m.closed.Load() || ctx.Err() != nil {
				return
			}
			m.mu.Lock()
			m.status.Active = false
			m.status.LastError = err.Error()
			m.mu.Unlock()
			log.Printf("[tun] 读取虚拟网卡异常断开: %v", err)
			if m.opts.OnFatal != nil {
				go m.opts.OnFatal(err)
			}
			return
		}
		if n <= 0 {
			continue
		}
		raw := buf[:n]
		ver := raw[0] >> 4
		var proto tcpip.NetworkProtocolNumber
		switch ver {
		case 4:
			proto = ipv4.ProtocolNumber
		case 6:
			proto = ipv6.ProtocolNumber
		default:
			continue
		}
		pktCopy := make([]byte, n)
		copy(pktCopy, raw)
		pktBuf := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(pktCopy),
		})
		m.chEP.InjectInbound(proto, pktBuf)
		pktBuf.DecRef()
	}
}

func (m *Manager) writeToDeviceLoop(ctx context.Context) {
	for {
		pkt := m.chEP.ReadContext(ctx)
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		data := view.AsSlice()
		if len(data) > 0 && !m.closed.Load() {
			if _, err := m.dev.WritePacket(data); err != nil && !m.closed.Load() && ctx.Err() == nil {
				log.Printf("[tun] 写回虚拟网卡失败: %v", err)
			}
		}
		view.Release()
		pkt.DecRef()
	}
}

func (m *Manager) handleTCPForwarder(ctx context.Context, r *tcp.ForwarderRequest) {
	id := r.ID()
	dstIP := tcpipAddrToString(id.LocalAddress)
	dstPort := int(id.LocalPort)
	if dstIP == "" || dstPort <= 0 {
		r.Complete(true)
		return
	}

	var wq waiter.Queue
	ep, tcpErr := r.CreateEndpoint(&wq)
	if tcpErr != nil {
		r.Complete(true)
		return
	}
	r.Complete(false)

	clientConn := gonet.NewTCPConn(&wq, ep)

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer clientConn.Close()
		m.relayTCP(ctx, clientConn, dstIP, dstPort)
	}()
}

func (m *Manager) relayTCP(ctx context.Context, clientConn net.Conn, dstIP string, dstPort int) {
	proxyAddr := strings.TrimSpace(m.opts.ProxyAddr())
	if proxyAddr == "" {
		return
	}
	var user, pass string
	if m.opts.ProxyAuth != nil {
		user, pass = m.opts.ProxyAuth()
	}

	// 短时非阻塞嗅探首包：在不阻塞 server-first 协议（SSH/SMTP 等）的前提下提取 TLS SNI 或 HTTP Host，
	// 从而让核心库域名规则与抓包白名单 HTTPS MITM 在 TUN 模式下同样精准命中真实域名。
	var peeked []byte
	peekBuf := make([]byte, 4096)
	_ = clientConn.SetReadDeadline(time.Now().Add(tcpSniffTimeout))
	n, readErr := clientConn.Read(peekBuf)
	_ = clientConn.SetReadDeadline(time.Time{})
	if n > 0 {
		peeked = peekBuf[:n]
	} else if readErr != nil && !isTimeoutErr(readErr) {
		return
	}

	targetHost := m.resolveTargetHost(dstIP, peeked)
	upstream, err := dialSOCKS5TCP(proxyAddr, user, pass, targetHost, dstPort, 10*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()

	// 上下文取消时主动打断长连接，确保关闭 TUN 时毫秒级退出
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = clientConn.Close()
			_ = upstream.Close()
		case <-done:
		}
	}()

	if len(peeked) > 0 {
		if _, err := upstream.Write(peeked); err != nil {
			return
		}
	}

	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(upstream, clientConn)
		closeWrite(upstream)
	}()
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(clientConn, upstream)
		closeWrite(clientConn)
	}()
	copyWG.Wait()
}

func (m *Manager) handleUDPForwarder(ctx context.Context, r *udp.ForwarderRequest) bool {
	id := r.ID()
	dstIP := tcpipAddrToString(id.LocalAddress)
	dstPort := int(id.LocalPort)
	if dstIP == "" || dstPort <= 0 {
		return false
	}

	var wq waiter.Queue
	ep, tcpErr := r.CreateEndpoint(&wq)
	if tcpErr != nil {
		return false
	}
	clientConn := gonet.NewUDPConn(&wq, ep)

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer clientConn.Close()
		m.relayUDP(ctx, clientConn, dstIP, dstPort)
	}()
	return true
}

func (m *Manager) relayUDP(ctx context.Context, clientConn *gonet.UDPConn, dstIP string, dstPort int) {
	proxyAddr := strings.TrimSpace(m.opts.ProxyAddr())
	if proxyAddr == "" {
		return
	}
	var user, pass string
	if m.opts.ProxyAuth != nil {
		user, pass = m.opts.ProxyAuth()
	}

	sess, err := openSOCKS5UDP(proxyAddr, user, pass, 8*time.Second)
	if err != nil {
		return
	}
	defer sess.Close()

	targetHost := dstIP
	if cached := m.dns.Lookup(dstIP); cached != "" {
		targetHost = cached
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = clientConn.Close()
			_ = sess.Close()
		case <-done:
		}
	}()

	// 上行：TUN -> SOCKS5 UDP Relay
	go func() {
		buf := make([]byte, 65535)
		for {
			_ = clientConn.SetReadDeadline(time.Now().Add(udpIdleTimeout))
			n, err := clientConn.Read(buf)
			if err != nil {
				_ = sess.Close()
				return
			}
			if n == 0 {
				continue
			}
			payload := buf[:n]
			// 针对 QUIC Initial 尝试提取 SNI 辅助域名透传
			sendHost := targetHost
			if sendHost == dstIP && dstPort == 443 {
				if qm, ok := capture.ParseQUICInitial(payload); ok && qm.SNI != "" {
					sendHost = qm.SNI
					m.dns.Put(dstIP, qm.SNI)
				}
			}
			if err := sess.Send(sendHost, dstPort, payload); err != nil {
				_ = clientConn.Close()
				return
			}
		}
	}()

	// 下行：SOCKS5 UDP Relay -> TUN
	respBuf := make([]byte, 65535)
	for {
		_ = sess.udpConn.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		_, _, payload, err := sess.Recv(respBuf)
		if err != nil {
			return
		}
		if len(payload) == 0 {
			continue
		}
		if dstPort == 53 {
			m.learnDNSResponse(payload)
		}
		if _, err := clientConn.Write(payload); err != nil {
			return
		}
	}
}

func (m *Manager) learnDNSResponse(payload []byte) {
	rec, ok := capture.ParseDNSPacket(payload, time.Now())
	if !ok || rec.Direction != "answer" || rec.Name == "" {
		return
	}
	for _, ans := range rec.Answers {
		if ip := net.ParseIP(strings.TrimSpace(ans)); ip != nil {
			m.dns.Put(ip.String(), rec.Name)
		}
	}
}

func (m *Manager) resolveTargetHost(dstIP string, peeked []byte) string {
	if len(peeked) > 0 {
		if sni, _, _, ok := capture.ParseTLSClientHello(peeked); ok && sni != "" {
			m.dns.Put(dstIP, sni)
			return sni
		}
		if host := extractHTTPHostHeader(peeked); host != "" {
			if net.ParseIP(host) == nil {
				m.dns.Put(dstIP, host)
			}
			return host
		}
	}
	if cached := m.dns.Lookup(dstIP); cached != "" {
		return cached
	}
	return dstIP
}

// extractHTTPHostHeader 从明文 HTTP/1.x 首包提取 Host 请求头（去除端口号）。
func extractHTTPHostHeader(data []byte) string {
	if len(data) < 12 {
		return ""
	}
	// 快速校验是否常见 HTTP 方法开头
	methods := []string{"GET ", "POST ", "PUT ", "DELETE ", "HEAD ", "OPTIONS ", "PATCH ", "CONNECT "}
	matched := false
	for _, m := range methods {
		if bytes.HasPrefix(data, []byte(m)) {
			matched = true
			break
		}
	}
	if !matched {
		return ""
	}
	lines := bytes.Split(data, []byte("\r\n"))
	for i := 1; i < len(lines) && i < 32; i++ {
		if len(lines[i]) == 0 {
			break
		}
		line := string(lines[i])
		if len(line) > 5 && strings.EqualFold(line[:5], "host:") {
			val := strings.TrimSpace(line[5:])
			if h, _, err := net.SplitHostPort(val); err == nil {
				return strings.Trim(h, "[]")
			}
			return strings.Trim(val, "[]")
		}
	}
	return ""
}

// AddBypassHosts 在切换代理节点时动态追加新节点 IP 的 /32 物理网关豁免路由，避免新隧道回环。
func (m *Manager) AddBypassHosts(hosts []string) {
	if m == nil || m.closed.Load() {
		return
	}
	m.mu.RLock()
	scoped := m.status.ScopedTestMode
	dev := m.dev
	m.mu.RUnlock()
	if scoped || dev == nil {
		return
	}
	for _, ip := range ResolveBypassIPv4s(hosts) {
		if err := dev.AddBypassIP(ip); err != nil {
			log.Printf("[tun] 动态追加节点豁免路由失败 (%s): %v", ip, err)
		}
	}
}

// Status 返回当前 TUN 运行状态只读快照。
func (m *Manager) Status() Status {
	if m == nil {
		return Status{}
	}
	m.mu.RLock()
	st := m.status
	if m.dev != nil {
		st.RouteCount = m.dev.RouteCount()
	}
	m.mu.RUnlock()
	return st
}

// Close 停止所有转发协程、释放 gVisor 协议栈、还原系统路由并销毁虚拟网卡。
func (m *Manager) Close() error {
	if m == nil || !m.closed.CompareAndSwap(false, true) {
		return nil
	}
	if m.cancel != nil {
		m.cancel()
	}
	if m.chEP != nil {
		m.chEP.Close()
	}
	var closeErr error
	if m.dev != nil {
		closeErr = m.dev.Close()
	}
	if m.stk != nil {
		m.stk.Close()
	}
	m.wg.Wait()

	m.mu.Lock()
	m.status.Active = false
	m.mu.Unlock()
	log.Printf("[tun] 虚拟网卡已关闭并释放路由")
	return closeErr
}

func tcpipAddrToString(addr tcpip.Address) string {
	b := addr.AsSlice()
	if len(b) == 4 {
		ip, ok := netip.AddrFromSlice(b)
		if ok {
			return ip.String()
		}
	}
	if len(b) == 16 {
		ip, ok := netip.AddrFromSlice(b)
		if ok {
			return ip.String()
		}
	}
	return ""
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout")
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
