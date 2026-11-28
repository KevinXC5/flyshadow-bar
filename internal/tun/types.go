package tun

import (
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTunIP 是虚拟网卡本端 IPv4 地址（RFC 2544 基准保留网段，避免与常见局域网冲突）。
	DefaultTunIP = "198.18.0.1"
	// DefaultTunPrefix 是虚拟网卡掩码前缀长度。
	DefaultTunPrefix = 16
	// DefaultMTU 是 TUN 虚拟网卡默认 MTU。
	DefaultMTU = 1500
	// TestCIDRsEnv 允许在安全实测时仅将指定测试网段路由到 TUN，绝不接管全局默认流量。
	TestCIDRsEnv = "FLYSHADOW_TUN_TEST_CIDRS"
)

var (
	// ErrAdminRequired 表示当前操作系统需要管理员权限（如 Windows 非提权进程或 macOS 取消授权）才能开启 TUN。
	ErrAdminRequired = errors.New("开启 TUN 模式需要管理员权限")
	// ErrUnsupported 表示当前操作系统暂不支持原生 TUN 驱动。
	ErrUnsupported = errors.New("当前平台暂不支持原生 TUN 模式")
)

// Status 描述 TUN 虚拟网卡的当前运行状态。
type Status struct {
	Active         bool   `json:"active"`
	DeviceName     string `json:"deviceName,omitempty"`
	TunIP          string `json:"tunIp,omitempty"`
	Gateway        string `json:"gateway,omitempty"`
	OutboundIface  string `json:"outboundIface,omitempty"`
	RouteCount     int    `json:"routeCount,omitempty"`
	ScopedTestMode bool   `json:"scopedTestMode,omitempty"`
	LastError      string `json:"lastError,omitempty"`
}

// DeviceConfig 定义底层平台虚拟网卡与路由的启动参数。
type DeviceConfig struct {
	TunIP          string
	PrefixLen      int
	MTU            int
	TakeoverCIDRs  []string
	BypassCIDRs    []string
	BypassIPs      []string
	ScopedTestMode bool
	AdapterName    string
	WinTunDLLPath  string
	// OnAuthComplete 在系统权限授权弹窗结束（确认或取消）后第一时间触发，便于桌面 UI 立即恢复窗口前台聚焦。
	OnAuthComplete func()
}

// PacketDevice 抽象跨平台 L3 IP 虚拟网卡设备（macOS utun / Windows Wintun / 单测内存设备）。
type PacketDevice interface {
	Name() string
	MTU() int
	OutboundIface() string
	Gateway() string
	RouteCount() int
	ReadPacket(buf []byte) (int, error)
	WritePacket(pkt []byte) (int, error)
	AddBypassIP(ip string) error
	Close() error
}

// Options 配置纯 Go TUN 引擎。
type Options struct {
	TunIP            string
	MTU              int
	ProxyAddr        func() string
	ProxyAuth        func() (username, password string)
	BypassHosts      func() []string
	CustomRouteCIDRs []string
	WinTunDLLPath    string
	// OpenDevice 仅用于单元测试或自定义虚拟设备注入；为空时走平台原生驱动。
	OpenDevice func(cfg DeviceConfig) (PacketDevice, error)
	// OnAuthComplete 在系统管理员授权框关闭后第一时间回调（早于虚拟网卡与路由配置完成）。
	OnAuthComplete func()
	// OnFatal 在底层虚拟网卡或提权辅助进程意外断开时异步回调。
	OnFatal func(err error)
}

// dnsCache 记录经 TUN 观察到的 DNS A/AAAA 响应映射（IP -> 域名），辅助非 TLS/HTTP 流量还原域名。
type dnsCache struct {
	mu      sync.RWMutex
	entries map[string]dnsCacheItem
	maxSize int
	ttl     time.Duration
}

type dnsCacheItem struct {
	domain    string
	expiresAt time.Time
}

func newDNSCache(maxSize int, ttl time.Duration) *dnsCache {
	if maxSize <= 0 {
		maxSize = 2048
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &dnsCache{
		entries: make(map[string]dnsCacheItem, 256),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

func (c *dnsCache) Put(ipStr, domain string) {
	if c == nil {
		return
	}
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if ip == nil || domain == "" {
		return
	}
	key := ip.String()
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxSize {
		for k, v := range c.entries {
			if now.After(v.expiresAt) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= c.maxSize {
			// 简单淘汰部分旧条目避免内存膨胀
			evict := c.maxSize / 4
			for k := range c.entries {
				delete(c.entries, k)
				evict--
				if evict <= 0 {
					break
				}
			}
		}
	}
	c.entries[key] = dnsCacheItem{
		domain:    domain,
		expiresAt: now.Add(c.ttl),
	}
}

func (c *dnsCache) Lookup(ipStr string) string {
	if c == nil {
		return ""
	}
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return ""
	}
	now := time.Now()
	c.mu.RLock()
	item, ok := c.entries[ip.String()]
	c.mu.RUnlock()
	if !ok || now.After(item.expiresAt) {
		return ""
	}
	return item.domain
}
