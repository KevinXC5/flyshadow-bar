//go:build !darwin && !windows

package capture

import "fmt"

func platformInstallPromptHint() string {
	return "当前平台请手动将证书文件导入系统或浏览器受信任根证书存储。"
}

func checkSystemCATrust(_, _, _ string) (bool, bool, string) {
	return false, false, "当前平台暂不支持自动检测系统根证书信任状态，请手动导入证书"
}

func installSystemCATrust(certPath string) error {
	return fmt.Errorf("当前平台不支持一键安装根证书，请手动导入: %s", certPath)
}

func uninstallSystemCATrust(_, _, _ string) error {
	return fmt.Errorf("当前平台不支持自动卸载根证书，请在系统证书管理中手动移除")
}
