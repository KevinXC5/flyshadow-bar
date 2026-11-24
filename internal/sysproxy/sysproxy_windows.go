//go:build windows

package sysproxy

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	internetSettingsKeyPath       = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	windowsProxyOverride          = "localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*;<local>"
	internetOptionSettingsChanged = 39
	internetOptionRefresh         = 37
)

var (
	wininetDLL         = windows.NewLazySystemDLL("wininet.dll")
	internetSetOptionW = wininetDLL.NewProc("InternetSetOptionW")
)

type windowsManager struct{}

// New 返回 Windows 平台的系统代理管理器实现。
func New() Manager {
	return &windowsManager{}
}

// Enable 写入注册表并刷新 WinINet 缓存以启用系统代理。
func (m *windowsManager) Enable(host string, port int) error {
	cleanHost, err := validateProxyTarget(host, port)
	if err != nil {
		return err
	}

	key, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开系统代理注册表项失败: %w", err)
	}
	defer key.Close()

	proxyServer := net.JoinHostPort(cleanHost, strconv.Itoa(port))
	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("写入 ProxyEnable 失败: %w", err)
	}
	if err := key.SetStringValue("ProxyServer", proxyServer); err != nil {
		return fmt.Errorf("写入 ProxyServer 失败: %w", err)
	}
	if err := key.SetStringValue("ProxyOverride", windowsProxyOverride); err != nil {
		return fmt.Errorf("写入 ProxyOverride 失败: %w", err)
	}

	if err := notifyInternetSettingsChanged(); err != nil {
		return fmt.Errorf("刷新 Windows 系统代理缓存失败: %w", err)
	}
	return nil
}

// Disable 关闭注册表中的系统代理开关并刷新 WinINet 缓存。
func (m *windowsManager) Disable() error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开系统代理注册表项失败: %w", err)
	}
	defer key.Close()

	if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
		return fmt.Errorf("关闭 ProxyEnable 失败: %w", err)
	}

	if err := notifyInternetSettingsChanged(); err != nil {
		return fmt.Errorf("刷新 Windows 系统代理缓存失败: %w", err)
	}
	return nil
}

// IsEnabled 读取注册表判断当前系统代理是否开启且匹配目标地址。
func (m *windowsManager) IsEnabled(host string, port int) (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKeyPath, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("读取系统代理注册表项失败: %w", err)
	}
	defer key.Close()

	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("读取 ProxyEnable 失败: %w", err)
	}
	if enabled != 1 {
		return false, nil
	}
	if port <= 0 {
		return true, nil
	}

	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("读取 ProxyServer 失败: %w", err)
	}

	return matchWindowsProxyServer(server, host, port), nil
}

// notifyInternetSettingsChanged 通知 WinINet 代理配置已变更并重新加载缓存。
func notifyInternetSettingsChanged() error {
	if err := wininetDLL.Load(); err != nil {
		return fmt.Errorf("加载 wininet.dll 失败: %w", err)
	}
	if r1, _, callErr := internetSetOptionW.Call(0, internetOptionSettingsChanged, 0, 0); r1 == 0 {
		return fmt.Errorf("调用 InternetSetOptionW(SETTINGS_CHANGED) 失败: %w", callErr)
	}
	if r1, _, callErr := internetSetOptionW.Call(0, internetOptionRefresh, 0, 0); r1 == 0 {
		return fmt.Errorf("调用 InternetSetOptionW(REFRESH) 失败: %w", callErr)
	}
	return nil
}
