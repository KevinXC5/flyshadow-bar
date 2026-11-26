//go:build darwin

package sysproxy

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

var darwinBypassDomains = []string{
	"127.0.0.1",
	"localhost",
	"*.local",
	"169.254/16",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

type commandRunner func(name string, args ...string) (string, error)

type darwinManager struct {
	run          commandRunner
	mu           sync.RWMutex
	excluded     map[string]struct{}
	excludedList []string
}

var _ ServiceFilterManager = (*darwinManager)(nil)

// New 返回 macOS 平台的系统代理管理器实现。
func New() Manager {
	return &darwinManager{run: runSystemCommand}
}

func runSystemCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return string(out), fmt.Errorf("执行命令 %s %s 失败: %s: %w", name, strings.Join(args, " "), msg, err)
		}
		return string(out), fmt.Errorf("执行命令 %s %s 失败: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *darwinManager) listServices() ([]string, error) {
	out, err := m.run("networksetup", "-listallnetworkservices")
	if err != nil {
		return nil, fmt.Errorf("获取网络服务列表失败: %w", err)
	}
	services := parseNetworkServices(out)
	if len(services) == 0 {
		return nil, fmt.Errorf("未找到可用的网络服务")
	}
	return services, nil
}

// ListServices 返回 macOS 当前所有可用（未禁用）的网络服务名称列表。
func (m *darwinManager) ListServices() ([]string, error) {
	return m.listServices()
}

// SetExcludedServices 设置系统代理排除的网络服务列表。
func (m *darwinManager) SetExcludedServices(excluded []string) {
	clean := normalizeServiceNames(excluded)
	set := make(map[string]struct{}, len(clean))
	for _, svc := range clean {
		set[svc] = struct{}{}
	}
	m.mu.Lock()
	m.excluded = set
	m.excludedList = clean
	m.mu.Unlock()
}

// ExcludedServices 返回当前已排除的网络服务列表副本。
func (m *darwinManager) ExcludedServices() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.excludedList) == 0 {
		return nil
	}
	out := make([]string, len(m.excludedList))
	copy(out, m.excludedList)
	return out
}

func (m *darwinManager) hasExcluded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.excluded) > 0
}

func (m *darwinManager) filterServices(services []string) []string {
	m.mu.RLock()
	excluded := m.excluded
	m.mu.RUnlock()
	if len(excluded) == 0 {
		return services
	}
	out := make([]string, 0, len(services))
	for _, svc := range services {
		if _, skip := excluded[svc]; skip {
			continue
		}
		out = append(out, svc)
	}
	return out
}

// Enable 为所有未排除的可用网络服务开启 HTTP、HTTPS 与 SOCKS 系统代理，并配置本地绕过域名。
func (m *darwinManager) Enable(host string, port int) error {
	cleanHost, err := validateProxyTarget(host, port)
	if err != nil {
		return err
	}

	allServices, err := m.listServices()
	if err != nil {
		return err
	}
	services := m.filterServices(allServices)
	if len(services) == 0 {
		return fmt.Errorf("所有可用网络服务均已排除，请在设置中至少保留一个网卡")
	}

	portStr := strconv.Itoa(port)
	succeeded := 0
	var errs []string

	for _, svc := range services {
		if err := m.enableService(svc, cleanHost, portStr); err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", svc, err))
			continue
		}
		succeeded++
	}

	if succeeded == 0 {
		return fmt.Errorf("所有网络服务开启系统代理均失败: %s", strings.Join(errs, "; "))
	}
	return nil
}

// EnableService 仅对指定网络服务开启 HTTP、HTTPS 与 SOCKS 系统代理。
func (m *darwinManager) EnableService(service, host string, port int) error {
	svc := strings.TrimSpace(service)
	if svc == "" {
		return fmt.Errorf("网络服务名称不能为空")
	}
	cleanHost, err := validateProxyTarget(host, port)
	if err != nil {
		return err
	}
	return m.enableService(svc, cleanHost, strconv.Itoa(port))
}

func (m *darwinManager) enableService(service, host, portStr string) error {
	cmds := [][]string{
		{"-setwebproxy", service, host, portStr},
		{"-setwebproxystate", service, "on"},
		{"-setsecurewebproxy", service, host, portStr},
		{"-setsecurewebproxystate", service, "on"},
		{"-setsocksfirewallproxy", service, host, portStr},
		{"-setsocksfirewallproxystate", service, "on"},
	}
	bypassArgs := make([]string, 0, 2+len(darwinBypassDomains))
	bypassArgs = append(bypassArgs, "-setproxybypassdomains", service)
	bypassArgs = append(bypassArgs, darwinBypassDomains...)
	cmds = append(cmds, bypassArgs)

	for _, args := range cmds {
		if _, err := m.run("networksetup", args...); err != nil {
			return err
		}
	}
	return nil
}

// Disable 关闭所有未排除的可用网络服务的 HTTP、HTTPS 与 SOCKS 系统代理（不触碰用户已排除的网卡）。
func (m *darwinManager) Disable() error {
	allServices, err := m.listServices()
	if err != nil {
		return err
	}
	services := m.filterServices(allServices)
	if len(services) == 0 {
		return nil
	}

	succeeded := 0
	var errs []string

	for _, svc := range services {
		if err := m.disableService(svc); err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", svc, err))
			continue
		}
		succeeded++
	}

	if succeeded == 0 {
		return fmt.Errorf("所有网络服务关闭系统代理均失败: %s", strings.Join(errs, "; "))
	}
	return nil
}

// DisableServiceIfMatches 仅当指定网络服务的 HTTP/HTTPS/SOCKS 代理指向目标 host:port 时关闭对应项。
func (m *darwinManager) DisableServiceIfMatches(service, host string, port int) (bool, error) {
	svc := strings.TrimSpace(service)
	if svc == "" {
		return false, fmt.Errorf("网络服务名称不能为空")
	}
	cleanHost, err := validateProxyTarget(host, port)
	if err != nil {
		return false, err
	}

	checks := []struct {
		getFlag string
		setFlag string
	}{
		{"-getwebproxy", "-setwebproxystate"},
		{"-getsecurewebproxy", "-setsecurewebproxystate"},
		{"-getsocksfirewallproxy", "-setsocksfirewallproxystate"},
	}

	changed := false
	var errs []string
	for _, chk := range checks {
		out, err := m.run("networksetup", chk.getFlag, svc)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		info := parseWebProxyOutput(out)
		if !matchWebProxy(info, cleanHost, port) {
			continue
		}
		if _, err := m.run("networksetup", chk.setFlag, svc, "off"); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		changed = true
	}

	if len(errs) > 0 && !changed {
		return false, fmt.Errorf("清理网络服务 [%s] 代理失败: %s", svc, strings.Join(errs, "; "))
	}
	return changed, nil
}

func (m *darwinManager) disableService(service string) error {
	cmds := [][]string{
		{"-setwebproxystate", service, "off"},
		{"-setsecurewebproxystate", service, "off"},
		{"-setsocksfirewallproxystate", service, "off"},
	}
	for _, args := range cmds {
		if _, err := m.run("networksetup", args...); err != nil {
			return err
		}
	}
	return nil
}

// IsEnabled 检查当前生效的系统代理是否已开启并指向目标主机与端口。
// 优先使用单次 `scutil --proxy` 读取活跃网络接口的实际生效状态，避免离线接口残留配置造成误判；
// 若已配置排除网卡且主接口恰好被排除导致 scutil 返回未开启，则补充核对未排除网卡，避免状态回弹或退出漏清理；
// 若 scutil 不可用则回退至 networksetup 逐服务查询。
func (m *darwinManager) IsEnabled(host string, port int) (bool, error) {
	if out, err := m.run("scutil", "--proxy"); err == nil {
		if matchScutilProxy(parseScutilProxyOutput(out), host, port) {
			return true, nil
		}
		if !m.hasExcluded() {
			return false, nil
		}
		if enabled, svcErr := m.isAnyTargetServiceEnabled(host, port); svcErr == nil {
			return enabled, nil
		}
		return false, nil
	}

	return m.isAnyTargetServiceEnabled(host, port)
}

func (m *darwinManager) isAnyTargetServiceEnabled(host string, port int) (bool, error) {
	allServices, err := m.listServices()
	if err != nil {
		return false, err
	}
	services := m.filterServices(allServices)
	if len(services) == 0 {
		return false, nil
	}

	queried := 0
	var errs []string
	for _, svc := range services {
		for _, flag := range []string{"-getwebproxy", "-getsecurewebproxy"} {
			out, err := m.run("networksetup", flag, svc)
			if err != nil {
				errs = append(errs, fmt.Sprintf("[%s] %v", svc, err))
				continue
			}
			queried++
			info := parseWebProxyOutput(out)
			if matchWebProxy(info, host, port) {
				return true, nil
			}
		}
	}

	if queried == 0 {
		return false, fmt.Errorf("查询系统代理状态失败: %s", strings.Join(errs, "; "))
	}
	return false, nil
}
