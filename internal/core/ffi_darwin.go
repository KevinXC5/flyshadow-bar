//go:build darwin

package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
)

// NativeClient 在 macOS 上通过 purego 零 CGO 加载并调用 libflyshadow_core_lib.dylib。
type NativeClient struct {
	libPath string
	handle  uintptr

	mu    sync.Mutex
	rt    int64
	tc    int64
	proxy int64

	fnNewRuntime            func() int64
	fnNewTunnelContext      func(rt int64) int64
	fnSetClientUUID         func(rt, tc int64, uuid string)
	fnSetProxyType          func(rt, tc int64, proxyType int32)
	fnSetUDPProxyType       func(rt, tc int64, proxyType int32)
	fnSetNativeUDP          func(rt, tc int64, enable bool)
	fnSetIPv6Enable         func(rt, tc int64, enable bool)
	fnSetUseBuildInDNS      func(rt, tc int64, enable bool)
	fnSetDirectConnPriority func(rt, tc int64, enable bool, timeoutMs int64)
	fnClearDomainRule       func(rt, tc int64)
	fnSetDomainRuleObj      func(rt, tc int64, domain string, matching, proxyType, directConnPriority int32)
	fnSetGeoIP              func(rt, tc int64, country string, data uintptr, length int32)
	fnNewProxy              func(tc int64) int64
	fnStartProxy            func(rt, proxy int64, port int32, user, pass string) *byte
	fnStartProxyIP          func(rt, proxy int64, ip string, port int32, user, pass string) *byte
	fnStopProxy             func(rt, proxy int64)
	fnFreeProxy             func(proxy int64)
	fnConnectTunnel         func(rt, tc int64, host string, port int32, password string, enableQuic bool)
	fnCloseTunnel           func(rt, tc int64) int32
	fnTestTunnelDelay       func(rt int64, nodeJSON string, enableQuic bool) int64
	fnGetTunnelUpload       func(rt, tc int64) int64
	fnGetTunnelDownload     func(rt, tc int64) int64
	fnGetTunnelPingDelay    func(rt, tc int64) int32
	fnGetTunnelStatus       func(rt, tc int64) int32
	fnGetTunnelMapperInfo   func(rt, tc int64) *byte
	fnCloseConnect          func(rt, tc int64, key string)
	fnGetVersion            func() *byte
	fnFreeString            func(ptr *byte)
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

	h, err := purego.Dlopen(libPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("加载核心库失败 (%s): %w", libPath, err)
	}

	c := &NativeClient{
		libPath: libPath,
		handle:  h,
	}

	purego.RegisterLibFunc(&c.fnNewRuntime, h, "new_runtime")
	purego.RegisterLibFunc(&c.fnNewTunnelContext, h, "new_tunnel_context")
	purego.RegisterLibFunc(&c.fnSetClientUUID, h, "set_client_uuid")
	purego.RegisterLibFunc(&c.fnSetProxyType, h, "set_proxy_type")
	purego.RegisterLibFunc(&c.fnSetUDPProxyType, h, "set_udp_proxy_type")
	purego.RegisterLibFunc(&c.fnSetNativeUDP, h, "set_native_udp")
	purego.RegisterLibFunc(&c.fnSetIPv6Enable, h, "set_ipv6_enable")
	purego.RegisterLibFunc(&c.fnSetUseBuildInDNS, h, "set_use_build_in_dns")
	purego.RegisterLibFunc(&c.fnSetDirectConnPriority, h, "set_direct_conn_priority")
	purego.RegisterLibFunc(&c.fnClearDomainRule, h, "clear_domain_rule")
	purego.RegisterLibFunc(&c.fnSetDomainRuleObj, h, "set_domain_rule_obj")
	purego.RegisterLibFunc(&c.fnSetGeoIP, h, "set_geoip")
	purego.RegisterLibFunc(&c.fnNewProxy, h, "new_proxy")
	purego.RegisterLibFunc(&c.fnStartProxy, h, "start_proxy")
	purego.RegisterLibFunc(&c.fnStartProxyIP, h, "start_proxy_ip")
	purego.RegisterLibFunc(&c.fnStopProxy, h, "stop_proxy")
	purego.RegisterLibFunc(&c.fnFreeProxy, h, "free_proxy")
	purego.RegisterLibFunc(&c.fnConnectTunnel, h, "connect_tunnel")
	purego.RegisterLibFunc(&c.fnCloseTunnel, h, "close_tunnel")
	purego.RegisterLibFunc(&c.fnTestTunnelDelay, h, "test_tunnel_delay")
	purego.RegisterLibFunc(&c.fnGetTunnelUpload, h, "get_tunnel_upload")
	purego.RegisterLibFunc(&c.fnGetTunnelDownload, h, "get_tunnel_download")
	purego.RegisterLibFunc(&c.fnGetTunnelPingDelay, h, "get_tunnel_ping_delay")
	purego.RegisterLibFunc(&c.fnGetTunnelStatus, h, "get_tunnel_status")
	purego.RegisterLibFunc(&c.fnGetTunnelMapperInfo, h, "get_tunnel_mapper_info_json")
	purego.RegisterLibFunc(&c.fnCloseConnect, h, "close_connect")
	purego.RegisterLibFunc(&c.fnGetVersion, h, "get_version")
	purego.RegisterLibFunc(&c.fnFreeString, h, "free_string")

	return c, nil
}

func (c *NativeClient) LibPath() string { return c.libPath }

func (c *NativeClient) Version() string {
	ptr := c.fnGetVersion()
	return c.consumeCString(ptr)
}

func (c *NativeClient) Init() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rt == 0 {
		c.rt = c.fnNewRuntime()
		if c.rt == 0 {
			return errors.New("new_runtime 返回空句柄")
		}
	}
	if c.tc == 0 {
		c.tc = c.fnNewTunnelContext(c.rt)
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
	c.fnSetClientUUID(rt, tc, uuid)
}

func (c *NativeClient) SetProxyType(mode ProxyMode) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.fnSetProxyType(rt, tc, int32(mode))
}

func (c *NativeClient) SetUDPProxyType(mode ProxyMode) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.fnSetUDPProxyType(rt, tc, int32(mode))
}

func (c *NativeClient) SetNativeUDP(enabled bool) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.fnSetNativeUDP(rt, tc, enabled)
}

func (c *NativeClient) SetIPv6Enable(enabled bool) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.fnSetIPv6Enable(rt, tc, enabled)
}

func (c *NativeClient) SetUseBuildInDNS(enabled bool) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.fnSetUseBuildInDNS(rt, tc, enabled)
}

func (c *NativeClient) SetDirectConnPriority(enabled bool, timeoutMs int64) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.fnSetDirectConnPriority(rt, tc, enabled, timeoutMs)
}

func (c *NativeClient) ClearDomainRules() {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return
	}
	c.fnClearDomainRule(rt, tc)
}

func (c *NativeClient) SetDomainRule(domain string, matching, proxyType, directConnPriority int32) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 || domain == "" {
		return
	}
	c.fnSetDomainRuleObj(rt, tc, domain, matching, proxyType, directConnPriority)
}

func (c *NativeClient) SetGeoIP(country string, data []byte) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 || len(data) == 0 {
		return
	}
	c.fnSetGeoIP(rt, tc, country, uintptr(unsafe.Pointer(&data[0])), int32(len(data)))
	runtime.KeepAlive(data)
}

func (c *NativeClient) StartProxy(port int, allowLAN bool, username, password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rt == 0 || c.tc == 0 {
		return errors.New("核心运行时未初始化")
	}
	c.stopProxyLocked()

	// 只有同时配置了用户名和密码时才开启本地代理认证（与官方实现一致）
	user, pass := "", ""
	if username != "" && password != "" {
		user, pass = username, password
	}

	p := c.fnNewProxy(c.tc)
	if p == 0 {
		return errors.New("new_proxy 返回空句柄")
	}
	c.proxy = p

	var errPtr *byte
	if allowLAN {
		errPtr = c.fnStartProxy(c.rt, p, int32(port), user, pass)
	} else {
		errPtr = c.fnStartProxyIP(c.rt, p, "127.0.0.1", int32(port), user, pass)
	}
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
		c.fnStopProxy(c.rt, c.proxy)
		c.fnFreeProxy(c.proxy)
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
	c.fnConnectTunnel(rt, tc, host, int32(port), password, enableQuic)
	return nil
}

func (c *NativeClient) CloseTunnel() int32 {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return 0
	}
	return c.fnCloseTunnel(rt, tc)
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
	return c.fnTestTunnelDelay(rt, payload, enableQuic)
}

func (c *NativeClient) TunnelUploadBytes() int64 {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return 0
	}
	return c.fnGetTunnelUpload(rt, tc)
}

func (c *NativeClient) TunnelDownloadBytes() int64 {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return 0
	}
	return c.fnGetTunnelDownload(rt, tc)
}

func (c *NativeClient) TunnelPingDelay() int32 {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return -1
	}
	return c.fnGetTunnelPingDelay(rt, tc)
}

func (c *NativeClient) TunnelStatus() int32 {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return -1
	}
	return c.fnGetTunnelStatus(rt, tc)
}

func (c *NativeClient) TunnelConnections() ([]ConnectionInfo, error) {
	rt, tc := c.handles()
	if rt == 0 || tc == 0 {
		return nil, nil
	}
	raw := c.consumeCString(c.fnGetTunnelMapperInfo(rt, tc))
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
	c.fnCloseConnect(rt, tc, key)
}

func (c *NativeClient) Close() {
	c.StopProxy()
	_ = c.CloseTunnel()
}

func (c *NativeClient) consumeCString(ptr *byte) string {
	if ptr == nil {
		return ""
	}
	defer c.fnFreeString(ptr)
	return copyCString(ptr)
}

func copyCString(ptr *byte) string {
	if ptr == nil {
		return ""
	}
	const maxLen = 1 << 20
	var n int
	for n < maxLen {
		if *(*byte)(unsafe.Add(unsafe.Pointer(ptr), n)) == 0 {
			break
		}
		n++
	}
	if n == 0 {
		return ""
	}
	buf := make([]byte, n)
	copy(buf, unsafe.Slice(ptr, n))
	return string(buf)
}
