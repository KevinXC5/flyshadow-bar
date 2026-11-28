package capture

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProxyOptions 配置 Go 前置透明抓包混合代理。
type ProxyOptions struct {
	ListenAddr        string // 如 127.0.0.1:7890 或 0.0.0.0:7890
	UpstreamAddr      string // 底层核心库内部回环端口，如 127.0.0.1:54321
	Username          string
	Password          string
	Store             *Store
	CA                *CAManager
	MITMEnabled       bool
	MITMHosts         []string
	MaxBodyBytes      int
	ResolveProcess    func(clientAddr string) string
	UpstreamTLSConfig *tls.Config // 可选，供测试注入上游 TLS 信任池
}

// Proxy 是运行在用户本地代理端口上的混合协议抓包前置代理（HTTP + HTTPS + SOCKS5 TCP/UDP）。
type Proxy struct {
	opts ProxyOptions

	mu          sync.RWMutex
	mitmEnabled bool
	mitmHosts   []string

	tunnelMu      sync.Mutex
	nextTunnelID  uint64
	activeTunnels map[uint64]activeTunnel

	ln     net.Listener
	closed atomic.Bool
	wg     sync.WaitGroup
}

type activeTunnel struct {
	host       string
	isTLS      bool
	mitm       bool
	clientConn net.Conn
	upConn     net.Conn
}

// StartProxy 启动前置抓包代理监听。
func StartProxy(opts ProxyOptions) (*Proxy, error) {
	if opts.Store == nil {
		opts.Store = NewStore(DefaultMaxEntries)
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if opts.ListenAddr == "" {
		return nil, errors.New("抓包代理监听地址不能为空")
	}
	if opts.UpstreamAddr == "" {
		return nil, errors.New("上游核心代理地址不能为空")
	}

	ln, err := net.Listen("tcp", opts.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("监听抓包代理端口失败 (%s): %w", opts.ListenAddr, err)
	}

	p := &Proxy{
		opts:          opts,
		mitmEnabled:   opts.MITMEnabled,
		mitmHosts:     NormalizeHostPatterns(opts.MITMHosts),
		activeTunnels: make(map[uint64]activeTunnel),
		ln:            ln,
	}
	p.wg.Add(1)
	go p.acceptLoop()
	return p, nil
}

// Addr 返回实际监听地址（测试时可用于获取随机分配端口）。
func (p *Proxy) Addr() string {
	if p == nil || p.ln == nil {
		return ""
	}
	return p.ln.Addr().String()
}

// UpdateMITMConfig 热更新白名单 HTTPS 解密开关与域名列表，并主动断开受影响域名的既有长连接，
// 让新加入或移出白名单的域名在下一条请求立即按最新策略重建 TLS 握手，不影响无关域名连接。
func (p *Proxy) UpdateMITMConfig(enabled bool, hosts []string) int {
	if p == nil {
		return 0
	}
	normHosts := NormalizeHostPatterns(hosts)
	p.mu.Lock()
	p.mitmEnabled = enabled
	p.mitmHosts = normHosts
	p.mu.Unlock()

	return p.dropMismatchedTLSTunnels(enabled, normHosts)
}

func (p *Proxy) registerActiveTunnel(t activeTunnel) uint64 {
	if p == nil {
		return 0
	}
	p.tunnelMu.Lock()
	p.nextTunnelID++
	id := p.nextTunnelID
	if p.activeTunnels == nil {
		p.activeTunnels = make(map[uint64]activeTunnel)
	}
	p.activeTunnels[id] = t
	p.tunnelMu.Unlock()
	return id
}

func (p *Proxy) unregisterActiveTunnel(id uint64) {
	if p == nil || id == 0 {
		return
	}
	p.tunnelMu.Lock()
	delete(p.activeTunnels, id)
	p.tunnelMu.Unlock()
}

func (p *Proxy) dropMismatchedTLSTunnels(enabled bool, hosts []string) int {
	if p == nil {
		return 0
	}
	var toClose []activeTunnel
	p.tunnelMu.Lock()
	for id, t := range p.activeTunnels {
		if !t.isTLS {
			continue
		}
		wantMITM := p.opts.CA != nil && enabled && len(hosts) > 0 && MatchMITMHost(t.host, hosts)
		if wantMITM != t.mitm {
			toClose = append(toClose, t)
			delete(p.activeTunnels, id)
		}
	}
	p.tunnelMu.Unlock()

	for _, t := range toClose {
		if t.clientConn != nil {
			_ = t.clientConn.Close()
		}
		if t.upConn != nil {
			_ = t.upConn.Close()
		}
	}
	return len(toClose)
}

func (p *Proxy) shouldMITM(host string) bool {
	if p == nil || p.opts.CA == nil {
		return false
	}
	p.mu.RLock()
	enabled := p.mitmEnabled
	hosts := p.mitmHosts
	p.mu.RUnlock()
	if !enabled || len(hosts) == 0 {
		return false
	}
	return MatchMITMHost(host, hosts)
}

// Close 停止接受新连接并关闭监听套接字。
func (p *Proxy) Close() error {
	if p == nil {
		return nil
	}
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	err := p.ln.Close()
	return err
}

func (p *Proxy) acceptLoop() {
	defer p.wg.Done()
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			if p.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		p.wg.Add(1)
		go func(c net.Conn) {
			defer p.wg.Done()
			p.handleClient(c)
		}(conn)
	}
}

func (p *Proxy) resolveProc(remoteAddr net.Addr) string {
	if p.opts.ResolveProcess == nil || remoteAddr == nil {
		return ""
	}
	return p.opts.ResolveProcess(remoteAddr.String())
}

func (p *Proxy) handleClient(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, 16*1024)
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	first, err := br.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || len(first) == 0 {
		return
	}

	if first[0] == 0x05 {
		p.handleSOCKS5(conn, br)
		return
	}
	p.handleHTTPProxy(conn, br)
}

// ---------- HTTP / HTTPS 代理处理 ----------

func (p *Proxy) handleHTTPProxy(clientConn net.Conn, br *bufio.Reader) {
	procName := p.resolveProc(clientConn.RemoteAddr())
	clientAddr := ""
	if clientConn.RemoteAddr() != nil {
		clientAddr = clientConn.RemoteAddr().String()
	}

	for {
		_ = clientConn.SetReadDeadline(time.Now().Add(90 * time.Second))
		req, err := http.ReadRequest(br)
		_ = clientConn.SetReadDeadline(time.Time{})
		if err != nil {
			return
		}

		// 代理鉴权校验
		if !p.checkHTTPProxyAuth(req) {
			_, _ = io.WriteString(clientConn, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"FlyShadow Bar\"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			_ = req.Body.Close()
			return
		}

		if req.Method == http.MethodConnect {
			_ = req.Body.Close()
			p.handleHTTPConnect(clientConn, br, req.Host, clientAddr, procName)
			return
		}

		// 明文 HTTP 代理请求：通过上游代理转发并完整采集请求/响应详情
		keepAlive := p.forwardPlainHTTPRequest(clientConn, req, clientAddr, procName)
		if !keepAlive {
			return
		}
	}
}

func (p *Proxy) checkHTTPProxyAuth(req *http.Request) bool {
	if p.opts.Username == "" || p.opts.Password == "" {
		return true
	}
	raw := strings.TrimSpace(req.Header.Get("Proxy-Authorization"))
	if !strings.HasPrefix(strings.ToLower(raw), "basic ") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw[6:]))
	if err != nil {
		return false
	}
	want := p.opts.Username + ":" + p.opts.Password
	return string(decoded) == want
}

func (p *Proxy) forwardPlainHTTPRequest(clientConn net.Conn, req *http.Request, clientAddr, procName string) bool {
	defer req.Body.Close()
	started := time.Now()

	host, port := splitHostPortDefault(req.URL.Host, 80)
	if host == "" {
		host, port = splitHostPortDefault(req.Host, 80)
	}
	fullURL := req.URL.String()
	if !strings.HasPrefix(fullURL, "http://") && !strings.HasPrefix(fullURL, "https://") && host != "" {
		fullURL = fmt.Sprintf("http://%s%s", net.JoinHostPort(host, strconv.Itoa(port)), req.URL.RequestURI())
	}

	reqBodyBytes, reqBodySize, reqTrunc, readErr := readBodyLimited(req.Body, p.opts.MaxBodyBytes)
	if readErr != nil {
		return false
	}

	entry := Entry{
		Kind:             KindHTTP,
		Protocol:         req.Proto,
		StartedAt:        started,
		Active:           true,
		ClientAddr:       clientAddr,
		ProcessName:      procName,
		Host:             host,
		Port:             port,
		Method:           req.Method,
		URL:              fullURL,
		Path:             req.URL.Path,
		QueryParams:      ExtractQueryParams(req.URL.RawQuery),
		ReqHeaders:       FlattenHeaders(req.Header),
		ReqBody:          decodeBodyForDisplay(reqBodyBytes, req.Header.Get("Content-Encoding")),
		ReqBodySize:      reqBodySize,
		ReqBodyTruncated: reqTrunc,
		ReqContentType:   req.Header.Get("Content-Type"),
		BytesSent:        reqBodySize,
	}
	if entry.Path == "" {
		entry.Path = "/"
	}
	id := p.opts.Store.Add(entry)

	// 向上游核心代理发 HTTP 代理请求（保留核心库规则分流）
	upConn, err := net.DialTimeout("tcp", p.opts.UpstreamAddr, 15*time.Second)
	if err != nil {
		p.finishEntryWithError(id, started, fmt.Errorf("连接上游核心代理失败: %w", err))
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return false
	}
	defer upConn.Close()

	// 重新组装写给上游代理的请求（去除 Proxy-Authorization）
	outReq := req.Clone(req.Context())
	outReq.Header.Del("Proxy-Authorization")
	outReq.Header.Del("Proxy-Connection")
	if outReq.URL.Scheme == "" {
		outReq.URL.Scheme = "http"
	}
	if outReq.URL.Host == "" {
		outReq.URL.Host = req.Host
	}
	outReq.Body = io.NopCloser(bytes.NewReader(reqBodyBytes))
	if reqTrunc {
		// 若请求体超过捕获上限，无法完整缓存重放，已截断部分仍尽力发出
		outReq.ContentLength = int64(len(reqBodyBytes))
	} else {
		outReq.ContentLength = int64(len(reqBodyBytes))
	}

	if err := outReq.WriteProxy(upConn); err != nil {
		p.finishEntryWithError(id, started, err)
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return false
	}

	upReader := bufio.NewReaderSize(upConn, 16*1024)
	resp, err := http.ReadResponse(upReader, outReq)
	if err != nil {
		p.finishEntryWithError(id, started, err)
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return false
	}
	defer resp.Body.Close()

	// 流式转发响应回客户端，同时受限捕获响应 Body
	respCap := &limitedBuffer{limit: p.opts.MaxBodyBytes}
	var respBytes int64
	if err := writeHTTPResponseStreaming(clientConn, resp, respCap, &respBytes); err != nil {
		p.finishEntryWithError(id, started, err)
		return false
	}

	displayBody := decodeBodyForDisplay(respCap.Bytes(), resp.Header.Get("Content-Encoding"))
	p.opts.Store.Update(id, func(e *Entry) {
		e.Active = false
		e.DurationMs = time.Since(started).Milliseconds()
		e.StatusCode = resp.StatusCode
		e.StatusText = resp.Status
		e.RespHeaders = FlattenHeaders(resp.Header)
		e.RespBody = displayBody
		e.RespBodySize = respBytes
		e.RespBodyTruncated = respCap.truncated
		e.RespContentType = resp.Header.Get("Content-Type")
		e.BytesRecv = respBytes
	})

	return !req.Close && !resp.Close
}

func (p *Proxy) handleHTTPConnect(clientConn net.Conn, br *bufio.Reader, targetHostPort, clientAddr, procName string) {
	started := time.Now()
	host, port := splitHostPortDefault(targetHostPort, 443)
	if host == "" {
		_, _ = io.WriteString(clientConn, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))

	// 先建立到上游核心代理的 CONNECT 隧道，确保核心库规则分流生效
	upConn, err := p.dialUpstreamConnect(target)
	if err != nil {
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		p.opts.Store.Add(Entry{
			Kind:        KindHTTPS,
			Protocol:    "HTTPS",
			StartedAt:   started,
			DurationMs:  time.Since(started).Milliseconds(),
			ClientAddr:  clientAddr,
			ProcessName: procName,
			Host:        host,
			Port:        port,
			Error:       err.Error(),
		})
		return
	}
	defer upConn.Close()

	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	wrappedClient := &bufferedConn{Conn: clientConn, r: br}
	p.handleTunneledStream(wrappedClient, upConn, host, port, clientAddr, procName)
}

func (p *Proxy) dialUpstreamConnect(target string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", p.opts.UpstreamAddr, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接上游核心代理失败: %w", err)
	}
	reqLine := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := io.WriteString(conn, reqLine); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(conn, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("上游 CONNECT 握手失败: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("上游 CONNECT 返回 %s", resp.Status)
	}
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// handleTunneledStream 处理已建立的上游 TCP 隧道：
// - 若首包是 TLS 且命中白名单，走本地 TLS MITM 解密并逐条解析 HTTPS 请求；
// - 否则无侵入嗅探 TLS ClientHello 元信息（SNI / 版本 / ALPN）并双向透传。
func (p *Proxy) handleTunneledStream(clientConn, upConn net.Conn, host string, port int, clientAddr, procName string) {
	started := time.Now()

	// 先读取首包片段以判断是否为 TLS 握手或明文 HTTP（15 秒超时以兼容浏览器预连接池）
	firstBuf := make([]byte, 4096)
	_ = clientConn.SetReadDeadline(time.Now().Add(15 * time.Second))
	n, readErr := clientConn.Read(firstBuf)
	_ = clientConn.SetReadDeadline(time.Time{})
	if n == 0 && readErr != nil {
		return
	}
	firstData := firstBuf[:n]
	replayClient := &prefixConn{Conn: clientConn, prefix: firstData}

	sni, tlsVer, alpn, isTLS := ParseTLSClientHello(firstData)
	effectiveHost := host
	if sni != "" {
		effectiveHost = sni
	}

	// 1. 命中白名单的 TLS 流量 -> 执行本机 TLS MITM 解密
	if isTLS && p.shouldMITM(effectiveHost) {
		p.serveMITMTLS(replayClient, upConn, effectiveHost, port, clientAddr, procName, tlsVer, sni, alpn)
		return
	}

	// 2. 明文 HTTP 走 SOCKS5/CONNECT 隧道时，也直接解析请求详情
	if !isTLS && looksLikeHTTPRequest(firstData) {
		p.serveDirectHTTPStream(replayClient, upConn, host, port, clientAddr, procName, false, "", "", "")
		return
	}

	// 3. 非白名单 HTTPS 或普通二进制 TCP -> 透明双向转发，记录连接与 TLS 元信息
	kind := KindTCP
	proto := "TCP"
	if isTLS {
		kind = KindHTTPS
		proto = "TLS"
		if tlsVer != "" {
			proto = tlsVer
		}
	}
	id := p.opts.Store.Add(Entry{
		Kind:          kind,
		Protocol:      proto,
		StartedAt:     started,
		Active:        true,
		ClientAddr:    clientAddr,
		ProcessName:   procName,
		Host:          effectiveHost,
		Port:          port,
		MITMDecrypted: false,
		TLSVersion:    tlsVer,
		TLSSNI:        sni,
		TLSALPN:       alpn,
	})

	tunnelID := p.registerActiveTunnel(activeTunnel{
		host:       effectiveHost,
		isTLS:      isTLS,
		mitm:       false,
		clientConn: clientConn,
		upConn:     upConn,
	})
	defer p.unregisterActiveTunnel(tunnelID)

	sent, recv := pipeBidirectional(replayClient, upConn)
	p.opts.Store.Update(id, func(e *Entry) {
		e.Active = false
		e.DurationMs = time.Since(started).Milliseconds()
		e.BytesSent = sent
		e.BytesRecv = recv
	})
}

func (p *Proxy) serveMITMTLS(clientConn, upConn net.Conn, host string, port int, clientAddr, procName, clientTLSVer, clientSNI, clientALPN string) {
	started := time.Now()
	leafCert, err := p.opts.CA.CertForHost(host)
	if err != nil {
		log.Printf("[capture] 为 %s 签发 MITM 证书失败: %v", host, err)
		p.opts.Store.Add(Entry{
			Kind:          KindHTTPS,
			Protocol:      "HTTPS",
			StartedAt:     started,
			DurationMs:    time.Since(started).Milliseconds(),
			ClientAddr:    clientAddr,
			ProcessName:   procName,
			Host:          host,
			Port:          port,
			MITMDecrypted: false,
			TLSVersion:    clientTLSVer,
			TLSSNI:        clientSNI,
			TLSALPN:       clientALPN,
			Error:         fmt.Sprintf("签发本地 MITM 证书失败: %v", err),
		})
		return
	}

	tlsClient := tls.Server(clientConn, &tls.Config{
		Certificates: []tls.Certificate{*leafCert},
		NextProtos:   []string{"http/1.1"},
	})
	_ = tlsClient.SetDeadline(time.Now().Add(15 * time.Second))
	if err := tlsClient.Handshake(); err != nil {
		_ = tlsClient.SetDeadline(time.Time{})
		p.opts.Store.Add(Entry{
			Kind:          KindHTTPS,
			Protocol:      "HTTPS",
			StartedAt:     started,
			DurationMs:    time.Since(started).Milliseconds(),
			ClientAddr:    clientAddr,
			ProcessName:   procName,
			Host:          host,
			Port:          port,
			MITMDecrypted: false,
			TLSVersion:    clientTLSVer,
			TLSSNI:        clientSNI,
			TLSALPN:       clientALPN,
			Error:         fmt.Sprintf("客户端 TLS 握手失败（可能未信任本机 CA 或应用开启了证书锁定）: %v", err),
		})
		return
	}
	_ = tlsClient.SetDeadline(time.Time{})
	defer tlsClient.Close()

	var upTLSConf *tls.Config
	if p.opts.UpstreamTLSConfig != nil {
		upTLSConf = p.opts.UpstreamTLSConfig.Clone()
	} else {
		upTLSConf = &tls.Config{}
	}
	if upTLSConf.ServerName == "" && net.ParseIP(host) == nil {
		upTLSConf.ServerName = host
	}
	upTLSConf.NextProtos = []string{"http/1.1"}

	tlsUp := tls.Client(upConn, upTLSConf)
	_ = tlsUp.SetDeadline(time.Now().Add(15 * time.Second))
	if err := tlsUp.Handshake(); err != nil {
		_ = tlsUp.SetDeadline(time.Time{})
		p.opts.Store.Add(Entry{
			Kind:          KindHTTPS,
			Protocol:      "HTTPS",
			StartedAt:     started,
			DurationMs:    time.Since(started).Milliseconds(),
			ClientAddr:    clientAddr,
			ProcessName:   procName,
			Host:          host,
			Port:          port,
			MITMDecrypted: true,
			TLSVersion:    clientTLSVer,
			TLSSNI:        clientSNI,
			TLSALPN:       clientALPN,
			Error:         fmt.Sprintf("上游 TLS 握手失败: %v", err),
		})
		return
	}
	_ = tlsUp.SetDeadline(time.Time{})
	defer tlsUp.Close()

	tunnelID := p.registerActiveTunnel(activeTunnel{
		host:       host,
		isTLS:      true,
		mitm:       true,
		clientConn: tlsClient,
		upConn:     tlsUp,
	})
	defer p.unregisterActiveTunnel(tunnelID)

	cs := tlsUp.ConnectionState()
	ver := formatTLSVersion(cs.Version)
	alpn := cs.NegotiatedProtocol
	if alpn == "" {
		alpn = clientALPN
	}
	p.serveDirectHTTPStream(tlsClient, tlsUp, host, port, clientAddr, procName, true, ver, host, alpn)
}

// serveDirectHTTPStream 在已建立的明文或 MITM 解密流上逐条读取并转发 HTTP/1.1 请求与响应。
func (p *Proxy) serveDirectHTTPStream(clientConn, upConn net.Conn, host string, port int, clientAddr, procName string, isHTTPS bool, tlsVer, tlsSNI, tlsALPN string) {
	clientBR := bufio.NewReaderSize(clientConn, 16*1024)
	upBR := bufio.NewReaderSize(upConn, 16*1024)

	scheme := "http"
	kind := KindHTTP
	if isHTTPS {
		scheme = "https"
		kind = KindHTTPS
	}

	handledCount := 0
	for {
		_ = clientConn.SetReadDeadline(time.Now().Add(90 * time.Second))
		peek, peekErr := clientBR.Peek(1)
		_ = clientConn.SetReadDeadline(time.Time{})
		if peekErr != nil || len(peek) == 0 {
			return
		}
		if peek[0] < 'A' || peek[0] > 'Z' {
			// 非 HTTP 协议载荷，降级双向透传并补记连接会话
			streamStarted := time.Now()
			id := p.opts.Store.Add(Entry{
				Kind:          kind,
				Protocol:      strings.ToUpper(scheme),
				StartedAt:     streamStarted,
				Active:        true,
				ClientAddr:    clientAddr,
				ProcessName:   procName,
				Host:          host,
				Port:          port,
				MITMDecrypted: isHTTPS,
				TLSVersion:    tlsVer,
				TLSSNI:        tlsSNI,
				TLSALPN:       tlsALPN,
			})
			sent, recv := pipeBidirectional(&bufferedConn{Conn: clientConn, r: clientBR}, &bufferedConn{Conn: upConn, r: upBR})
			p.opts.Store.Update(id, func(e *Entry) {
				e.Active = false
				e.DurationMs = time.Since(streamStarted).Milliseconds()
				e.BytesSent = sent
				e.BytesRecv = recv
			})
			return
		}

		req, err := http.ReadRequest(clientBR)
		if err != nil {
			if handledCount == 0 && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				p.opts.Store.Add(Entry{
					Kind:          kind,
					Protocol:      strings.ToUpper(scheme),
					StartedAt:     time.Now(),
					ClientAddr:    clientAddr,
					ProcessName:   procName,
					Host:          host,
					Port:          port,
					MITMDecrypted: isHTTPS,
					TLSVersion:    tlsVer,
					TLSSNI:        tlsSNI,
					TLSALPN:       tlsALPN,
					Error:         fmt.Sprintf("解析 HTTP 请求失败: %v", err),
				})
			}
			return
		}
		handledCount++
		started := time.Now()

		reqBodyBytes, reqBodySize, reqTrunc, bodyErr := readBodyLimited(req.Body, p.opts.MaxBodyBytes)
		_ = req.Body.Close()
		if bodyErr != nil {
			return
		}

		reqHost := req.Host
		if reqHost == "" {
			reqHost = host
			if (isHTTPS && port != 443) || (!isHTTPS && port != 80) {
				reqHost = net.JoinHostPort(host, strconv.Itoa(port))
			}
		}
		fullURL := fmt.Sprintf("%s://%s%s", scheme, reqHost, req.URL.RequestURI())
		path := req.URL.Path
		if path == "" {
			path = "/"
		}

		id := p.opts.Store.Add(Entry{
			Kind:             kind,
			Protocol:         strings.ToUpper(scheme),
			StartedAt:        started,
			Active:           true,
			ClientAddr:       clientAddr,
			ProcessName:      procName,
			Host:             host,
			Port:             port,
			Method:           req.Method,
			URL:              fullURL,
			Path:             path,
			QueryParams:      ExtractQueryParams(req.URL.RawQuery),
			MITMDecrypted:    isHTTPS,
			TLSVersion:       tlsVer,
			TLSSNI:           tlsSNI,
			TLSALPN:          tlsALPN,
			ReqHeaders:       FlattenHeaders(req.Header),
			ReqBody:          decodeBodyForDisplay(reqBodyBytes, req.Header.Get("Content-Encoding")),
			ReqBodySize:      reqBodySize,
			ReqBodyTruncated: reqTrunc,
			ReqContentType:   req.Header.Get("Content-Type"),
			BytesSent:        reqBodySize,
		})

		req.Body = io.NopCloser(bytes.NewReader(reqBodyBytes))
		req.ContentLength = int64(len(reqBodyBytes))
		if err := req.Write(upConn); err != nil {
			p.finishEntryWithError(id, started, err)
			return
		}

		resp, err := http.ReadResponse(upBR, req)
		if err != nil {
			p.finishEntryWithError(id, started, err)
			return
		}

		// 若触发 WebSocket / 协议升级（101 Switching Protocols），写回响应头后切为双向透传
		if resp.StatusCode == http.StatusSwitchingProtocols {
			_ = resp.Write(clientConn)
			_ = resp.Body.Close()
			p.opts.Store.Update(id, func(e *Entry) {
				e.StatusCode = resp.StatusCode
				e.StatusText = resp.Status
				e.RespHeaders = FlattenHeaders(resp.Header)
				e.Protocol = "WS"
			})
			sent, recv := pipeBidirectional(&bufferedConn{Conn: clientConn, r: clientBR}, &bufferedConn{Conn: upConn, r: upBR})
			p.opts.Store.Update(id, func(e *Entry) {
				e.Active = false
				e.DurationMs = time.Since(started).Milliseconds()
				e.BytesSent += sent
				e.BytesRecv += recv
			})
			return
		}

		respCap := &limitedBuffer{limit: p.opts.MaxBodyBytes}
		var respBytes int64
		writeErr := writeHTTPResponseStreaming(clientConn, resp, respCap, &respBytes)
		_ = resp.Body.Close()
		if writeErr != nil {
			p.finishEntryWithError(id, started, writeErr)
			return
		}

		displayBody := decodeBodyForDisplay(respCap.Bytes(), resp.Header.Get("Content-Encoding"))
		p.opts.Store.Update(id, func(e *Entry) {
			e.Active = false
			e.DurationMs = time.Since(started).Milliseconds()
			e.StatusCode = resp.StatusCode
			e.StatusText = resp.Status
			e.RespHeaders = FlattenHeaders(resp.Header)
			e.RespBody = displayBody
			e.RespBodySize = respBytes
			e.RespBodyTruncated = respCap.truncated
			e.RespContentType = resp.Header.Get("Content-Type")
			e.BytesRecv = respBytes
		})

		if req.Close || resp.Close {
			return
		}
	}
}

// ---------- SOCKS5（TCP CONNECT + UDP ASSOCIATE）处理 ----------

func (p *Proxy) handleSOCKS5(clientConn net.Conn, br *bufio.Reader) {
	procName := p.resolveProc(clientConn.RemoteAddr())
	clientAddr := ""
	if clientConn.RemoteAddr() != nil {
		clientAddr = clientConn.RemoteAddr().String()
	}

	// 1. 协商认证方法
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil || hdr[0] != 0x05 {
		return
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}

	needAuth := p.opts.Username != "" && p.opts.Password != ""
	if needAuth {
		if !bytes.Contains(methods, []byte{0x02}) {
			_, _ = clientConn.Write([]byte{0x05, 0xff})
			return
		}
		if _, err := clientConn.Write([]byte{0x05, 0x02}); err != nil {
			return
		}
		if !p.verifySOCKS5UserPass(clientConn, br) {
			return
		}
	} else {
		if _, err := clientConn.Write([]byte{0x05, 0x00}); err != nil {
			return
		}
	}

	// 2. 读取命令请求头：VER(1) CMD(1) RSV(1) ATYP(1)
	reqHdr := make([]byte, 4)
	if _, err := io.ReadFull(br, reqHdr); err != nil || reqHdr[0] != 0x05 {
		return
	}
	cmd := reqHdr[1]
	dstHost, dstPort, rawAddr, err := readSOCKS5Addr(br, reqHdr[3])
	if err != nil {
		_, _ = clientConn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	switch cmd {
	case 0x01: // CONNECT
		started := time.Now()
		upConn, err := p.dialUpstreamSOCKS5Connect(rawAddr)
		if err != nil {
			_, _ = clientConn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			kind, proto := KindTCP, "TCP"
			switch dstPort {
			case 443:
				kind, proto = KindHTTPS, "HTTPS"
			case 80:
				kind, proto = KindHTTP, "HTTP"
			}
			p.opts.Store.Add(Entry{
				Kind:        kind,
				Protocol:    proto,
				StartedAt:   started,
				DurationMs:  time.Since(started).Milliseconds(),
				ClientAddr:  clientAddr,
				ProcessName: procName,
				Host:        dstHost,
				Port:        dstPort,
				Error:       err.Error(),
			})
			return
		}
		defer upConn.Close()

		// 返回 SOCKS5 成功响应
		if _, err := clientConn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0}); err != nil {
			return
		}
		wrappedClient := &bufferedConn{Conn: clientConn, r: br}
		p.handleTunneledStream(wrappedClient, upConn, dstHost, dstPort, clientAddr, procName)

	case 0x03: // UDP ASSOCIATE
		p.handleSOCKS5UDPAssociate(clientConn, br, clientAddr, procName)

	default:
		_, _ = clientConn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	}
}

func (p *Proxy) verifySOCKS5UserPass(conn net.Conn, br *bufio.Reader) bool {
	ver, err := br.ReadByte()
	if err != nil || ver != 0x01 {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return false
	}
	ulen, err := br.ReadByte()
	if err != nil {
		return false
	}
	u := make([]byte, int(ulen))
	if _, err := io.ReadFull(br, u); err != nil {
		return false
	}
	plen, err := br.ReadByte()
	if err != nil {
		return false
	}
	pw := make([]byte, int(plen))
	if _, err := io.ReadFull(br, pw); err != nil {
		return false
	}
	if string(u) != p.opts.Username || string(pw) != p.opts.Password {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return false
	}
	_, err = conn.Write([]byte{0x01, 0x00})
	return err == nil
}

func (p *Proxy) dialUpstreamSOCKS5Connect(rawAddr []byte) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", p.opts.UpstreamAddr, 15*time.Second)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil || resp[0] != 0x05 || resp[1] != 0x00 {
		conn.Close()
		return nil, errors.New("上游 SOCKS5 握手失败")
	}
	req := make([]byte, 0, 3+len(rawAddr))
	req = append(req, 0x05, 0x01, 0x00)
	req = append(req, rawAddr...)
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(conn, 4096)
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil || hdr[1] != 0x00 {
		conn.Close()
		return nil, errors.New("上游 SOCKS5 CONNECT 失败")
	}
	if _, _, _, err := readSOCKS5Addr(br, hdr[3]); err != nil {
		conn.Close()
		return nil, err
	}
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

func (p *Proxy) dialUpstreamSOCKS5UDPAssociate() (net.Conn, *net.UDPAddr, error) {
	conn, err := net.DialTimeout("tcp", p.opts.UpstreamAddr, 15*time.Second)
	if err != nil {
		return nil, nil, err
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, nil, err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil || resp[0] != 0x05 || resp[1] != 0x00 {
		conn.Close()
		return nil, nil, errors.New("上游 SOCKS5 UDP 握手失败")
	}
	// CMD=0x03, ATYP=IPv4, 0.0.0.0:0
	if _, err := conn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReaderSize(conn, 1024)
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil || hdr[1] != 0x00 {
		conn.Close()
		return nil, nil, errors.New("上游 SOCKS5 UDP ASSOCIATE 失败")
	}
	host, port, _, err := readSOCKS5Addr(br, hdr[3])
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		upHost, _, _ := net.SplitHostPort(p.opts.UpstreamAddr)
		if upHost == "" {
			upHost = "127.0.0.1"
		}
		host = upHost
	}
	udpAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, udpAddr, nil
}

func (p *Proxy) handleSOCKS5UDPAssociate(clientTCP net.Conn, _ *bufio.Reader, clientAddr, procName string) {
	upTCP, upUDPAddr, err := p.dialUpstreamSOCKS5UDPAssociate()
	if err != nil {
		_, _ = clientTCP.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upTCP.Close()

	// 本地绑定 UDP Relay 套接字接收客户端发来的 SOCKS5 UDP 封装报文
	bindHost := "127.0.0.1"
	if localTCP, ok := clientTCP.LocalAddr().(*net.TCPAddr); ok && localTCP.IP != nil && !localTCP.IP.IsUnspecified() {
		bindHost = localTCP.IP.String()
	}
	clientUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(bindHost), Port: 0})
	if err != nil {
		_, _ = clientTCP.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer clientUDP.Close()

	upUDP, err := net.DialUDP("udp", nil, upUDPAddr)
	if err != nil {
		_, _ = clientTCP.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upUDP.Close()

	// 回复客户端本地 UDP 转发地址与端口
	localAddr := clientUDP.LocalAddr().(*net.UDPAddr)
	reply := buildSOCKS5AddrReply(localAddr.IP, localAddr.Port)
	if _, err := clientTCP.Write(reply); err != nil {
		return
	}

	var peerMu sync.RWMutex
	var peerAddr *net.UDPAddr

	var sessMu sync.Mutex
	sessions := make(map[string]uint64)
	sessionStarts := make(map[string]time.Time)

	recordUDPPacket := func(targetHost string, targetPort int, dir string, payload []byte) {
		now := time.Now()
		key := net.JoinHostPort(targetHost, strconv.Itoa(targetPort))
		proto, summary, dnsRec, quicMeta := InspectUDPPacket(targetPort, payload, dir, now)

		sessMu.Lock()
		id, exists := sessions[key]
		if !exists {
			e := Entry{
				Kind:        KindUDP,
				Protocol:    proto,
				UDPProtocol: proto,
				StartedAt:   now,
				Active:      true,
				ClientAddr:  clientAddr,
				ProcessName: procName,
				Host:        targetHost,
				Port:        targetPort,
			}
			id = p.opts.Store.Add(e)
			sessions[key] = id
			sessionStarts[key] = now
		}
		started := sessionStarts[key]
		sessMu.Unlock()

		if id == 0 {
			return
		}
		previewLen := min(len(payload), maxUDPSamplePreviewBytes)
		preview := append([]byte(nil), payload[:previewLen]...)
		sample := UDPPacketSample{
			Timestamp: now,
			Direction: dir,
			Target:    key,
			Size:      len(payload),
			Summary:   summary,
			Preview:   preview,
		}
		p.opts.Store.Update(id, func(e *Entry) {
			e.DurationMs = now.Sub(started).Milliseconds()
			if dir == "out" {
				e.PacketsSent++
				e.BytesSent += int64(len(payload))
			} else {
				e.PacketsRecv++
				e.BytesRecv += int64(len(payload))
			}
			if proto != "UDP" && (e.UDPProtocol == "" || e.UDPProtocol == "UDP") {
				e.UDPProtocol = proto
				e.Protocol = proto
			}
			if dnsRec != nil {
				if len(e.DNSRecords) < maxDNSRecordsPerEntry {
					e.DNSRecords = append(e.DNSRecords, *dnsRec)
				} else {
					copy(e.DNSRecords, e.DNSRecords[1:])
					e.DNSRecords[len(e.DNSRecords)-1] = *dnsRec
				}
			}
			if quicMeta != nil {
				if e.QUICInfo == nil {
					q := *quicMeta
					e.QUICInfo = &q
				} else {
					if quicMeta.SNI != "" {
						e.QUICInfo.SNI = quicMeta.SNI
					}
					if quicMeta.ALPN != "" {
						e.QUICInfo.ALPN = quicMeta.ALPN
					}
				}
			}
			if len(e.UDPSamples) < maxUDPSamplesPerEntry {
				e.UDPSamples = append(e.UDPSamples, sample)
			} else {
				copy(e.UDPSamples, e.UDPSamples[1:])
				e.UDPSamples[len(e.UDPSamples)-1] = sample
			}
		})
	}

	done := make(chan struct{})
	var closeOnce sync.Once
	stop := func() {
		closeOnce.Do(func() {
			close(done)
			_ = clientUDP.Close()
			_ = upUDP.Close()
		})
	}

	// 客户端 -> 上游
	go func() {
		defer stop()
		buf := make([]byte, 65535)
		for {
			n, from, err := clientUDP.ReadFromUDP(buf)
			if err != nil {
				return
			}
			peerMu.Lock()
			peerAddr = from
			peerMu.Unlock()

			pkt := buf[:n]
			if dstHost, dstPort, payload, ok := parseSOCKS5UDPPacket(pkt); ok {
				recordUDPPacket(dstHost, dstPort, "out", payload)
			}
			if _, err := upUDP.Write(pkt); err != nil {
				return
			}
		}
	}()

	// 上游 -> 客户端
	go func() {
		defer stop()
		buf := make([]byte, 65535)
		for {
			n, err := upUDP.Read(buf)
			if err != nil {
				return
			}
			peerMu.RLock()
			dst := peerAddr
			peerMu.RUnlock()
			if dst == nil {
				continue
			}
			pkt := buf[:n]
			if srcHost, srcPort, payload, ok := parseSOCKS5UDPPacket(pkt); ok {
				recordUDPPacket(srcHost, srcPort, "in", payload)
			}
			if _, err := clientUDP.WriteToUDP(pkt, dst); err != nil {
				return
			}
		}
	}()

	// 等待控制 TCP 连接关闭即结束本次 UDP ASSOCIATE 会话
	tmp := make([]byte, 64)
	for {
		if _, err := clientTCP.Read(tmp); err != nil {
			break
		}
	}
	stop()

	sessMu.Lock()
	for k, id := range sessions {
		started := sessionStarts[k]
		p.opts.Store.Update(id, func(e *Entry) {
			e.Active = false
			e.DurationMs = time.Since(started).Milliseconds()
		})
	}
	sessMu.Unlock()
}

// ---------- 辅助方法与工具类型 ----------

func (p *Proxy) finishEntryWithError(id uint64, started time.Time, err error) {
	if id == 0 {
		return
	}
	p.opts.Store.Update(id, func(e *Entry) {
		e.Active = false
		e.DurationMs = time.Since(started).Milliseconds()
		if err != nil {
			e.Error = err.Error()
		}
	})
}

func readSOCKS5Addr(br *bufio.Reader, atyp byte) (host string, port int, raw []byte, err error) {
	switch atyp {
	case 0x01: // IPv4
		buf := make([]byte, 6)
		if _, err = io.ReadFull(br, buf); err != nil {
			return "", 0, nil, err
		}
		host = net.IP(buf[:4]).String()
		port = int(binary.BigEndian.Uint16(buf[4:6]))
		raw = append([]byte{0x01}, buf...)
		return host, port, raw, nil
	case 0x03: // Domain
		l, err := br.ReadByte()
		if err != nil {
			return "", 0, nil, err
		}
		buf := make([]byte, int(l)+2)
		if _, err = io.ReadFull(br, buf); err != nil {
			return "", 0, nil, err
		}
		host = string(buf[:l])
		port = int(binary.BigEndian.Uint16(buf[l : l+2]))
		raw = append([]byte{0x03, l}, buf...)
		return host, port, raw, nil
	case 0x04: // IPv6
		buf := make([]byte, 18)
		if _, err = io.ReadFull(br, buf); err != nil {
			return "", 0, nil, err
		}
		host = net.IP(buf[:16]).String()
		port = int(binary.BigEndian.Uint16(buf[16:18]))
		raw = append([]byte{0x04}, buf...)
		return host, port, raw, nil
	default:
		return "", 0, nil, fmt.Errorf("不支持的 SOCKS5 地址类型: %d", atyp)
	}
}

func buildSOCKS5AddrReply(ip net.IP, port int) []byte {
	if v4 := ip.To4(); v4 != nil {
		out := make([]byte, 10)
		out[0], out[1], out[2], out[3] = 0x05, 0x00, 0x00, 0x01
		copy(out[4:8], v4)
		binary.BigEndian.PutUint16(out[8:10], uint16(port))
		return out
	}
	v6 := ip.To16()
	if v6 == nil {
		v6 = net.IPv6loopback
	}
	out := make([]byte, 22)
	out[0], out[1], out[2], out[3] = 0x05, 0x00, 0x00, 0x04
	copy(out[4:20], v6)
	binary.BigEndian.PutUint16(out[20:22], uint16(port))
	return out
}

func parseSOCKS5UDPPacket(pkt []byte) (host string, port int, payload []byte, ok bool) {
	if len(pkt) < 10 || pkt[2] != 0 {
		return "", 0, nil, false
	}
	atyp := pkt[3]
	switch atyp {
	case 0x01:
		if len(pkt) < 10 {
			return "", 0, nil, false
		}
		return net.IP(pkt[4:8]).String(), int(binary.BigEndian.Uint16(pkt[8:10])), pkt[10:], true
	case 0x03:
		if len(pkt) < 7 {
			return "", 0, nil, false
		}
		l := int(pkt[4])
		if len(pkt) < 5+l+2 {
			return "", 0, nil, false
		}
		return string(pkt[5 : 5+l]), int(binary.BigEndian.Uint16(pkt[5+l : 7+l])), pkt[7+l:], true
	case 0x04:
		if len(pkt) < 22 {
			return "", 0, nil, false
		}
		return net.IP(pkt[4:20]).String(), int(binary.BigEndian.Uint16(pkt[20:22])), pkt[22:], true
	default:
		return "", 0, nil, false
	}
}

func splitHostPortDefault(hostPort string, defPort int) (string, int) {
	s := strings.TrimSpace(hostPort)
	if s == "" {
		return "", defPort
	}
	if h, pStr, err := net.SplitHostPort(s); err == nil {
		if p, pErr := strconv.Atoi(pStr); pErr == nil && p > 0 {
			return strings.Trim(h, "[]"), p
		}
		return strings.Trim(h, "[]"), defPort
	}
	return strings.Trim(s, "[]"), defPort
}

func looksLikeHTTPRequest(b []byte) bool {
	for _, prefix := range []string{"GET ", "POST ", "PUT ", "DELETE ", "PATCH ", "HEAD ", "OPTIONS "} {
		if bytes.HasPrefix(b, []byte(prefix)) {
			return true
		}
	}
	return false
}

func readBodyLimited(r io.Reader, limit int) (data []byte, total int64, truncated bool, err error) {
	if r == nil {
		return nil, 0, false, nil
	}
	var buf bytes.Buffer
	tmp := make([]byte, 8192)
	for {
		n, rErr := r.Read(tmp)
		if n > 0 {
			total += int64(n)
			if buf.Len() < limit {
				remain := limit - buf.Len()
				if n <= remain {
					buf.Write(tmp[:n])
				} else {
					buf.Write(tmp[:remain])
					truncated = true
				}
			} else {
				truncated = true
			}
		}
		if rErr == io.EOF {
			return buf.Bytes(), total, truncated, nil
		}
		if rErr != nil {
			return buf.Bytes(), total, truncated, rErr
		}
	}
}

func writeHTTPResponseStreaming(dst io.Writer, resp *http.Response, capBuf *limitedBuffer, totalBody *int64) error {
	origBody := resp.Body
	counting := &teeCaptureReader{
		r:     origBody,
		cap:   capBuf,
		total: totalBody,
	}
	resp.Body = io.NopCloser(counting)
	err := resp.Write(dst)
	resp.Body = origBody
	return err
}

func decodeBodyForDisplay(raw []byte, contentEncoding string) []byte {
	if len(raw) == 0 {
		return nil
	}
	enc := strings.ToLower(strings.TrimSpace(contentEncoding))
	switch enc {
	case "gzip":
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return raw
		}
		defer gr.Close()
		out, err := io.ReadAll(io.LimitReader(gr, int64(DefaultMaxBodyBytes)*2))
		if err != nil || len(out) == 0 {
			return raw
		}
		return out
	case "deflate":
		fr := flate.NewReader(bytes.NewReader(raw))
		defer fr.Close()
		out, err := io.ReadAll(io.LimitReader(fr, int64(DefaultMaxBodyBytes)*2))
		if err != nil || len(out) == 0 {
			return raw
		}
		return out
	default:
		return raw
	}
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.limit <= 0 {
		return len(p), nil
	}
	remain := l.limit - l.buf.Len()
	if remain <= 0 {
		l.truncated = true
		return len(p), nil
	}
	if len(p) <= remain {
		l.buf.Write(p)
	} else {
		l.buf.Write(p[:remain])
		l.truncated = true
	}
	return len(p), nil
}

func (l *limitedBuffer) Bytes() []byte {
	return append([]byte(nil), l.buf.Bytes()...)
}

type teeCaptureReader struct {
	r     io.Reader
	cap   *limitedBuffer
	total *int64
}

func (t *teeCaptureReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		if t.total != nil {
			*t.total += int64(n)
		}
		if t.cap != nil {
			_, _ = t.cap.Write(p[:n])
		}
	}
	return n, err
}

func pipeBidirectional(a, b net.Conn) (aToB int64, bToA int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(b, a)
		aToB = n
		closeWrite(b)
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(a, b)
		bToA = n
		closeWrite(a)
	}()
	wg.Wait()
	return aToB, bToA
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.SetDeadline(time.Now())
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.r.Read(p)
}

type prefixConn struct {
	net.Conn
	prefix []byte
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}
