package tun

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
)

// DefaultTakeoverCIDRs 返回非破坏性 IPv4 分片接管网段（不覆盖或删除原 default 路由，
// 且长度均大于 0/0，从而优先命中虚拟网卡；同时不与系统 0.0.0.0/8 冲突）。
func DefaultTakeoverCIDRs() []string {
	return []string{
		"1.0.0.0/8",
		"2.0.0.0/7",
		"4.0.0.0/6",
		"8.0.0.0/5",
		"16.0.0.0/4",
		"32.0.0.0/3",
		"64.0.0.0/2",
		"128.0.0.0/1",
	}
}

// DefaultBypassCIDRs 返回必须绕过 TUN 虚拟网卡的保留与局域网网段：
// 包含 RFC1918 私有网段、链路本地、组播以及 100.64.0.0/10（CGNAT / Tailscale 等远程隧道网段），
// 防止开启 TUN 后中断局域网访问或远程管理通道。
func DefaultBypassCIDRs() []string {
	return []string{
		"10.0.0.0/8",
		"100.64.0.0/10",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"224.0.0.0/4",
		"255.255.255.255/32",
	}
}

// ParseCIDRList 解析逗号或空白分隔的 IPv4 CIDR 列表（自动支持裸 IP 转为 /32）。
func ParseCIDRList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.Contains(f, "/") {
			if ip := net.ParseIP(f).To4(); ip != nil {
				f = ip.String() + "/32"
			} else {
				continue
			}
		}
		ip, ipNet, err := net.ParseCIDR(f)
		if err != nil || ip.To4() == nil {
			continue
		}
		norm := ipNet.String()
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}

// ResolveTakeoverCIDRs 根据显式参数或测试环境变量决定本次 TUN 接管的网段集合。
// 当设置自定义网段（如安全测试）时返回 scoped=true，仅接管目标网段而绝不修改全局默认路由。
func ResolveTakeoverCIDRs(custom []string) (cidrs []string, scoped bool) {
	if len(custom) > 0 {
		parsed := ParseCIDRList(strings.Join(custom, ","))
		if len(parsed) > 0 {
			return parsed, true
		}
	}
	if env := strings.TrimSpace(os.Getenv(TestCIDRsEnv)); env != "" {
		parsed := ParseCIDRList(env)
		if len(parsed) > 0 {
			return parsed, true
		}
	}
	return DefaultTakeoverCIDRs(), false
}

// ResolveBypassIPv4s 将节点域名或 IP 列表解析为需要直走物理网关的公网 IPv4 地址集合。
func ResolveBypassIPv4s(hosts []string) []string {
	seen := make(map[string]struct{})
	addIP := func(ip net.IP) {
		v4 := ip.To4()
		if v4 == nil {
			return
		}
		if v4.IsLoopback() || v4.IsPrivate() || v4.IsLinkLocalUnicast() || v4.IsMulticast() || v4.IsUnspecified() {
			return
		}
		seen[v4.String()] = struct{}{}
	}

	for _, raw := range hosts {
		h := strings.TrimSpace(raw)
		if h == "" {
			continue
		}
		if hostOnly, _, err := net.SplitHostPort(h); err == nil {
			h = hostOnly
		}
		h = strings.Trim(h, "[]")
		if ip := net.ParseIP(h); ip != nil {
			addIP(ip)
			continue
		}
		ips, err := net.LookupIP(h)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			addIP(ip)
		}
	}

	out := make([]string, 0, len(seen))
	for ip := range seen {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

// CIDRToNetworkMask 将 IPv4 CIDR 解析为网络地址、点分十进制掩码与前缀长度（用于 Windows route 命令）。
func CIDRToNetworkMask(cidr string) (network string, mask string, prefix int, err error) {
	ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return "", "", 0, err
	}
	v4 := ip.To4()
	if v4 == nil || len(ipNet.Mask) != 4 {
		return "", "", 0, fmt.Errorf("仅支持 IPv4 CIDR: %s", cidr)
	}
	ones, _ := ipNet.Mask.Size()
	m := net.IP(ipNet.Mask).String()
	return ipNet.IP.To4().String(), m, ones, nil
}
