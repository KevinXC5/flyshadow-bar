package sysproxy

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Manager 定义系统代理的统一管理接口。
type Manager interface {
	Enable(host string, port int) error
	Disable() error
	IsEnabled(host string, port int) (bool, error)
}

// validateProxyTarget 校验代理主机和端口参数。
func validateProxyTarget(host string, port int) (string, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return "", fmt.Errorf("代理主机地址不能为空")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("代理端口超出有效范围: %d", port)
	}
	return trimmed, nil
}

// webProxyInfo 表示从 macOS networksetup -getwebproxy 输出解析出的代理配置。
type webProxyInfo struct {
	Enabled bool
	Server  string
	Port    int
}

// parseNetworkServices 解析 networksetup -listallnetworkservices 的输出。
// 忽略首行说明文字、空行以及以 "*" 开头的已禁用网络服务。
func parseNetworkServices(output string) []string {
	lines := strings.Split(output, "\n")
	if len(lines) <= 1 {
		return nil
	}

	services := make([]string, 0, len(lines)-1)
	for _, raw := range lines[1:] {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "*") {
			continue
		}
		services = append(services, line)
	}
	return services
}

// parseWebProxyOutput 解析 networksetup -getwebproxy <service> 的输出。
func parseWebProxyOutput(output string) webProxyInfo {
	var info webProxyInfo
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		switch key {
		case "Enabled":
			info.Enabled = strings.EqualFold(val, "Yes")
		case "Server":
			info.Server = val
		case "Port":
			if p, err := strconv.Atoi(val); err == nil {
				info.Port = p
			}
		}
	}
	return info
}

// matchWebProxy 判断解析出的 macOS Web 代理配置是否与期望主机和端口匹配。
// 当 port <= 0 时仅判断是否处于启用状态。
func matchWebProxy(info webProxyInfo, host string, port int) bool {
	if !info.Enabled {
		return false
	}
	if port <= 0 {
		return true
	}
	if info.Port != port {
		return false
	}
	expectedHost := strings.TrimSpace(host)
	if expectedHost == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(info.Server), expectedHost)
}

// scutilProxyInfo 表示从 macOS scutil --proxy 输出解析出的当前生效代理配置。
type scutilProxyInfo struct {
	HTTP  webProxyInfo
	HTTPS webProxyInfo
	SOCKS webProxyInfo
}

// parseScutilProxyOutput 解析 macOS `scutil --proxy` 输出的顶层字典键值。
func parseScutilProxyOutput(output string) scutilProxyInfo {
	var info scutilProxyInfo
	depth := 0
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")
		if depth == 1 && opens == 0 {
			if key, val, ok := strings.Cut(line, ":"); ok {
				key = strings.TrimSpace(key)
				val = strings.TrimSpace(val)
				switch key {
				case "HTTPEnable":
					info.HTTP.Enabled = val == "1"
				case "HTTPProxy":
					info.HTTP.Server = val
				case "HTTPPort":
					if p, err := strconv.Atoi(val); err == nil {
						info.HTTP.Port = p
					}
				case "HTTPSEnable":
					info.HTTPS.Enabled = val == "1"
				case "HTTPSProxy":
					info.HTTPS.Server = val
				case "HTTPSPort":
					if p, err := strconv.Atoi(val); err == nil {
						info.HTTPS.Port = p
					}
				case "SOCKSEnable":
					info.SOCKS.Enabled = val == "1"
				case "SOCKSProxy":
					info.SOCKS.Server = val
				case "SOCKSPort":
					if p, err := strconv.Atoi(val); err == nil {
						info.SOCKS.Port = p
					}
				}
			}
		}
		depth += opens - closes
		if depth < 0 {
			depth = 0
		}
	}
	return info
}

// matchScutilProxy 判断当前生效的 macOS 系统代理（HTTP 或 HTTPS）是否指向期望主机和端口。
func matchScutilProxy(info scutilProxyInfo, host string, port int) bool {
	return matchWebProxy(info.HTTP, host, port) || matchWebProxy(info.HTTPS, host, port)
}

// matchWindowsProxyServer 判断 Windows 注册表中的 ProxyServer 字段是否匹配目标主机和端口。
// 兼容 "host:port"、"http://host:port" 以及 "http=host:port;https=host:port" 等格式。
func matchWindowsProxyServer(proxyServer, host string, port int) bool {
	if port <= 0 {
		return true
	}

	expectedHost := strings.TrimSpace(host)
	portStr := strconv.Itoa(port)
	expectedAddr := ""
	if expectedHost != "" {
		expectedAddr = net.JoinHostPort(expectedHost, portStr)
	}

	entries := strings.Split(proxyServer, ";")
	for _, rawEntry := range entries {
		entry := strings.TrimSpace(rawEntry)
		if entry == "" {
			continue
		}
		// 兼容 "http=127.0.0.1:7890" 形式
		if scheme, addr, ok := strings.Cut(entry, "="); ok {
			scheme = strings.ToLower(strings.TrimSpace(scheme))
			if scheme != "http" && scheme != "https" && scheme != "socks" {
				continue
			}
			entry = strings.TrimSpace(addr)
		}
		// 兼容携带协议前缀的写法
		if idx := strings.Index(entry, "://"); idx >= 0 {
			entry = entry[idx+3:]
		}
		entry = strings.TrimSuffix(entry, "/")

		if expectedAddr != "" && strings.EqualFold(entry, expectedAddr) {
			return true
		}

		h, p, err := net.SplitHostPort(entry)
		if err != nil {
			continue
		}
		if p != portStr {
			continue
		}
		if expectedHost == "" || strings.EqualFold(h, expectedHost) {
			return true
		}
	}
	return false
}
