//go:build windows

package core

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// discoverWindowsRegistryInstallDirs 从 Windows 卸载注册表项读取官方 FlyShadow 的实际安装目录，
// 以兼容自定义盘符与非默认安装路径。
func discoverWindowsRegistryInstallDirs() []string {
	type regRoot struct {
		root registry.Key
		path string
	}
	roots := []regRoot{
		{registry.LOCAL_MACHINE, `Software\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.LOCAL_MACHINE, `Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall`},
	}

	var dirs []string
	for _, item := range roots {
		k, err := registry.OpenKey(item.root, item.path, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		subKeys, err := k.ReadSubKeyNames(-1)
		_ = k.Close()
		if err != nil {
			continue
		}
		for _, sub := range subKeys {
			sk, err := registry.OpenKey(item.root, item.path+`\`+sub, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			displayName, _, _ := sk.GetStringValue("DisplayName")
			if !isOfficialFlyShadowDisplayName(displayName) {
				_ = sk.Close()
				continue
			}
			installLoc, _, _ := sk.GetStringValue("InstallLocation")
			displayIcon, _, _ := sk.GetStringValue("DisplayIcon")
			_ = sk.Close()

			if loc := cleanInstallDir(installLoc); loc != "" {
				dirs = append(dirs, loc)
			}
			if iconDir := dirFromDisplayIcon(displayIcon); iconDir != "" {
				dirs = append(dirs, iconDir)
			}
		}
	}
	return dirs
}

func isOfficialFlyShadowDisplayName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || strings.Contains(lower, "bar") {
		return false
	}
	return strings.Contains(lower, "flyshadow") || strings.Contains(name, "飞影")
}

func cleanInstallDir(raw string) string {
	s := strings.TrimSpace(strings.Trim(raw, `"`))
	if s == "" {
		return ""
	}
	return filepath.Clean(s)
}

func dirFromDisplayIcon(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// DisplayIcon 常见格式："C:\Program Files\FlyShadow\FlyShadow.exe,0"
	if idx := strings.LastIndex(s, ","); idx > 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(strings.Trim(s, `"`))
	if s == "" {
		return ""
	}
	return filepath.Clean(filepath.Dir(s))
}
