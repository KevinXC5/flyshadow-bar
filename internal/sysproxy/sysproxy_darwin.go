//go:build darwin

package sysproxy

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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
	run commandRunner
}

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

// Enable 为所有可用网络服务开启 HTTP、HTTPS 与 SOCKS 系统代理，并配置本地绕过域名。
func (m *darwinManager) Enable(host string, port int) error {
	cleanHost, err := validateProxyTarget(host, port)
	if err != nil {
		return err
	}

	services, err := m.listServices()
	if err != nil {
		return err
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

// Disable 关闭所有可用网络服务的 HTTP、HTTPS 与 SOCKS 系统代理。
func (m *darwinManager) Disable() error {
	services, err := m.listServices()
	if err != nil {
		return err
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
// 若 scutil 不可用则回退至 networksetup 逐服务查询。
func (m *darwinManager) IsEnabled(host string, port int) (bool, error) {
	if out, err := m.run("scutil", "--proxy"); err == nil {
		return matchScutilProxy(parseScutilProxyOutput(out), host, port), nil
	}

	services, err := m.listServices()
	if err != nil {
		return false, err
	}

	queried := 0
	var errs []string
	for _, svc := range services {
		out, err := m.run("networksetup", "-getwebproxy", svc)
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

	if queried == 0 {
		return false, fmt.Errorf("查询系统代理状态失败: %s", strings.Join(errs, "; "))
	}
	return false, nil
}
