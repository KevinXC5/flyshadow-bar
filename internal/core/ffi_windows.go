//go:build windows

package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
)

// NativeClient 在 Windows 上通过 x/sys/windows 零 CGO 加载并调用 flyshadow_core_lib.dll。
type NativeClient struct {
	libPath string
	dll     *windows.DLL

	mu    sync.Mutex
	rt    int64
	tc    int64
	proxy int64

	pNewRuntime            *windows.Proc
	pNewTunnelContext      *windows.Proc
	pSetClientUUID         *windows.Proc
	pSetProxyType          *windows.Proc
	pSetUDPProxyType       *windows.Proc
	pSetNativeUDP          *windows.Proc
	pSetIPv6Enable         *windows.Proc
	pSetUseBuildInDNS      *windows.Proc
	pSetDirectConnPriority *windows.Proc
	pClearDomainRule       *windows.Proc
	pSetDomainRuleObj      *windows.Proc
	pSetGeoIP              *windows.Proc
	pNewProxy              *windows.Proc
	pStartProxy            *windows.Proc
	pStartProxyIP          *windows.Proc
	pStopProxy             *windows.Proc
	pFreeProxy             *windows.Proc
	pConnectTunnel         *windows.Proc
	pCloseTunnel           *windows.Proc
	pTestTunnelDelay       *windows.Proc
	pGetTunnelUpload       *windows.Proc
	pGetTunnelDownload     *windows.Proc
	pGetTunnelPingDelay    *windows.Proc
	pGetTunnelStatus       *windows.Proc
	pGetTunnelMapperInfo   *windows.Proc
	pCloseConnect          *windows.Proc
	pGetVersion            *windows.Proc
	pFreeString            *windows.Proc
}

// Open 加载指定路径（或自动发现路径）的 FlyShadow 原生动态库。
func Open(libPath string) (Client, error) {
	if libPath == "" {
		var err error
		libPath, err = LocateCoreLib()
		if err != nil {
			return nil, err
		}
	}

	dll, err := windows.LoadDLL(libPath)
	if err != nil {
		return nil, fmt.Errorf("加载核心库失败 (%s): %w", libPath, err)
	}

	c := &NativeClient{
		libPath: libPath,
		dll:     dll,
	}

	mustFind := func(name string) (*windows.Proc, error) {
		p, err := dll.FindProc(name)
		if err != nil {
			return nil, fmt.Errorf("核心库缺少导出符号 %s: %w", name, err)
		}
		return p, nil
	}

	procs := []struct {
		name string
		dst  **windows.Proc
	}{
		{"new_runtime", &c.pNewRuntime},
		{"new_tunnel_context", &c.pNewTunnelContext},
		{"set_client_uuid", &c.pSetClientUUID},
		{"set_proxy_type", &c.pSetProxyType},
		{"set_udp_proxy_type", &c.pSetUDPProxyType},
		{"set_native_udp", &c.pSetNativeUDP},
		{"set_ipv6_enable", &c.pSetIPv6Enable},
		{"set_use_build_in_dns", &c.pSetUseBuildInDNS},
		{"set_direct_conn_priority", &c.pSetDirectConnPriority},
		{"clear_domain_rule", &c.pClearDomainRule},
		{"set_domain_rule_obj", &c.pSetDomainRuleObj},
		{"set_geoip", &c.pSetGeoIP},
		{"new_proxy", &c.pNewProxy},
		{"start_proxy", &c.pStartProxy},
		{"start_proxy_ip", &c.pStartProxyIP},
		{"stop_proxy", &c.pStopProxy},
		{"free_proxy", &c.pFreeProxy},
		{"connect_tunnel", &c.pConnectTunnel},
		{"close_tunnel", &c.pCloseTunnel},
		{"test_tunnel_delay", &c.pTestTunnelDelay},
		{"get_tunnel_upload", &c.pGetTunnelUpload},
		{"get_tunnel_download", &c.pGetTunnelDownload},
		{"get_tunnel_ping_delay", &c.pGetTunnelPingDelay},
		{"get_tunnel_status", &c.pGetTunnelStatus},
		{"get_tunnel_mapper_info_json", &c.pGetTunnelMapperInfo},
		{"close_connect", &c.pCloseConnect},
		{"get_version", &c.pGetVersion},
		{"free_string", &c.pFreeString},
	}
	for _, item := range procs {
		p, err := mustFind(item.name)
		if err != nil {
			_ = dll.Release()
			return nil, err
		}
		*item.dst = p
	}

	return c, nil
}

func (c *NativeClient) LibPath() string { return c.libPath }

func (c *NativeClient) Version() string {
	r, _, _ := c.pGetVersion.Call()
	return c.consumeCString(r)
}

func (c *NativeClient) Init() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rt == 0 {
		r, _, _ := c.pNewRuntime.Call()
		c.rt = int64(r)
		if c.rt == 0 {
			return errors.New("new_runtime 返回空句柄")
		}
	}
	if c.tc == 0 {
		r, _, _ := c.pNewTunnelContext.Call(uintptr(c.rt))
		c.tc = int64(r)
		if c.tc == 0 {
			return errors.New("new_tunnel_context 返回空句柄")
		}
	}
	return nil
}

func (c *NativeClient) handles() (int64, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rt, c.tc
}

func (c *NativeClient) SetClientUUID(uuid string) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 || uuid == "" {
		return
	}
	b := cBytes(uuid)
	c.pSetClientUUID.Call(uintptr(rt), uintptr(tc), ptrOf(b))
	runtime.KeepAlive(b)
}

func (c *NativeClient) SetProxyType(mode ProxyMode) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.pSetProxyType.Call(uintptr(rt), uintptr(tc), uintptr(int32(mode)))
}

func (c *NativeClient) SetUDPProxyType(mode ProxyMode) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.pSetUDPProxyType.Call(uintptr(rt), uintptr(tc), uintptr(int32(mode)))
}

func (c *NativeClient) SetNativeUDP(enabled bool) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.pSetNativeUDP.Call(uintptr(rt), uintptr(tc), boolToUintptr(enabled))
}

func (c *NativeClient) SetIPv6Enable(enabled bool) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.pSetIPv6Enable.Call(uintptr(rt), uintptr(tc), boolToUintptr(enabled))
}

func (c *NativeClient) SetUseBuildInDNS(enabled bool) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.pSetUseBuildInDNS.Call(uintptr(rt), uintptr(tc), boolToUintptr(enabled))
}

func (c *NativeClient) SetDirectConnPriority(enabled bool, timeoutMs int64) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.pSetDirectConnPriority.Call(uintptr(rt), uintptr(tc), boolToUintptr(enabled), uintptr(timeoutMs))
}

func (c *NativeClient) ClearDomainRules() {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.pClearDomainRule.Call(uintptr(rt), uintptr(tc))
}

func (c *NativeClient) SetDomainRule(domain string, matching, proxyType, directConnPriority int32) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 || domain == "" {
		return
	}
	b := cBytes(domain)
	c.pSetDomainRuleObj.Call(uintptr(rt), uintptr(tc), ptrOf(b), uintptr(matching), uintptr(proxyType), uintptr(directConnPriority))
	runtime.KeepAlive(b)
}

func (c *NativeClient) SetGeoIP(country string, data []byte) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 || len(data) == 0 {
		return
	}
	cb := cBytes(country)
	c.pSetGeoIP.Call(uintptr(rt), uintptr(tc), ptrOf(cb), uintptr(unsafe.Pointer(&data[0])), uintptr(int32(len(data))))
	runtime.KeepAlive(cb)
	runtime.KeepAlive(data)
}

func (c *NativeClient) StartProxy(port int, allowLAN bool, username, password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rt == 0 || c.tc == 0 {
		return errors.New("核心运行时未初始化")
	}
	c.stopProxyLocked()

	user, pass := "", ""
	if username != "" && password != "" {
		user, pass = username, password
	}

	p, _, _ := c.pNewProxy.Call(uintptr(c.tc))
	if p == 0 {
		return errors.New("new_proxy 返回空句柄")
	}
	c.proxy = int64(p)

	ub := cBytes(user)
	pb := cBytes(pass)
	var errPtr uintptr
	if allowLAN {
		errPtr, _, _ = c.pStartProxy.Call(uintptr(c.rt), p, uintptr(int32(port)), ptrOf(ub), ptrOf(pb))
	} else {
		ipb := cBytes("127.0.0.1")
		errPtr, _, _ = c.pStartProxyIP.Call(uintptr(c.rt), p, ptrOf(ipb), uintptr(int32(port)), ptrOf(ub), ptrOf(pb))
		runtime.KeepAlive(ipb)
	}
	runtime.KeepAlive(ub)
	runtime.KeepAlive(pb)

	if errMsg := c.consumeCString(errPtr); errMsg != "" {
		c.stopProxyLocked()
		return fmt.Errorf("启动本地代理失败 (port=%d): %s", port, errMsg)
	}
	return nil
}

func (c *NativeClient) StopProxy() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopProxyLocked()
}

func (c *NativeClient) stopProxyLocked() {
	if c.proxy != 0 && c.rt != 0 {
		c.pStopProxy.Call(uintptr(c.rt), uintptr(c.proxy))
		c.pFreeProxy.Call(uintptr(c.proxy))
		c.proxy = 0
	}
}

func (c *NativeClient) ConnectTunnel(host string, port int, password string, enableQuic bool) error {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return errors.New("核心运行时未初始化")
	}
	if host == "" || port <= 0 {
		return errors.New("节点地址或端口无效")
	}
	hb := cBytes(host)
	pb := cBytes(password)
	c.pConnectTunnel.Call(uintptr(rt), uintptr(tc), ptrOf(hb), uintptr(int32(port)), ptrOf(pb), boolToUintptr(enableQuic))
	runtime.KeepAlive(hb)
	runtime.KeepAlive(pb)
	return nil
}

func (c *NativeClient) CloseTunnel() int32 {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return 0
	}
	r, _, _ := c.pCloseTunnel.Call(uintptr(rt), uintptr(tc))
	return int32(r)
}

func (c *NativeClient) TestTunnelDelay(node config.Node, enableQuic bool) int64 {
	rt, _ := c.handles()
	if rt == 0 {
		return -1
	}
	payload, err := marshalDelayPayload(node)
	if err != nil {
		return -1
	}
	jb := cBytes(payload)
	r, _, _ := c.pTestTunnelDelay.Call(uintptr(rt), ptrOf(jb), boolToUintptr(enableQuic))
	runtime.KeepAlive(jb)
	return int64(r)
}

func (c *NativeClient) TunnelUploadBytes() int64 {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return 0
	}
	r, _, _ := c.pGetTunnelUpload.Call(uintptr(rt), uintptr(tc))
	return int64(r)
}

func (c *NativeClient) TunnelDownloadBytes() int64 {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return 0
	}
	r, _, _ := c.pGetTunnelDownload.Call(uintptr(rt), uintptr(tc))
	return int64(r)
}

func (c *NativeClient) TunnelPingDelay() int32 {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return -1
	}
	r, _, _ := c.pGetTunnelPingDelay.Call(uintptr(rt), uintptr(tc))
	return int32(r)
}

func (c *NativeClient) TunnelStatus() int32 {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return -1
	}
	r, _, _ := c.pGetTunnelStatus.Call(uintptr(rt), uintptr(tc))
	return int32(r)
}

func (c *NativeClient) TunnelConnections() ([]ConnectionInfo, error) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return nil, nil
	}
	r, _, _ := c.pGetTunnelMapperInfo.Call(uintptr(rt), uintptr(tc))
	raw := c.consumeCString(r)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var list []ConnectionInfo
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("解析连接监控 JSON 失败: %w", err)
	}
	return list, nil
}

func (c *NativeClient) CloseConnection(key string) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 || key == "" {
		return
	}
	kb := cBytes(key)
	c.pCloseConnect.Call(uintptr(rt), uintptr(tc), ptrOf(kb))
	runtime.KeepAlive(kb)
}

func (c *NativeClient) Close() {
	c.StopProxy()
	_ = c.CloseTunnel()
	if c.dll != nil {
		_ = c.dll.Release()
		c.dll = nil
	}
}

func (c *NativeClient) consumeCString(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	defer c.pFreeString.Call(ptr)
	return copyCString(ptr)
}

func copyCString(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	base := *(**byte)(unsafe.Pointer(&ptr))
	const maxLen = 1 << 20
	var n int
	for n < maxLen {
		if *(*byte)(unsafe.Add(unsafe.Pointer(base), n)) == 0 {
			break
		}
		n++
	}
	if n == 0 {
		return ""
	}
	buf := make([]byte, n)
	copy(buf, unsafe.Slice(base, n))
	return string(buf)
}

func cBytes(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

func ptrOf(b []byte) uintptr {
	return uintptr(unsafe.Pointer(&b[0]))
}

func boolToUintptr(v bool) uintptr {
	if v {
		return 1
	}
	return 0
}
