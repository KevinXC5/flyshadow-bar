//go:build windows

package capture

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

func platformInstallPromptHint() string {
	return "点击安装后会弹出 Windows 原生安全警告窗口（询问“是否确实要安装此证书？”），这是系统对新增受信任根证书的正常保护。点击“是”即刻对当前用户生效，无需管理员权限。"
}

func checkSystemCATrust(_, sha256FP, sha1FP string) (installed bool, trusted bool, detail string) {
	// 优先用 SHA-1 指纹查询 CurrentUser\Root（Windows 证书存储以 SHA-1 Thumbprint 为主索引）
	query := sha1FP
	if query == "" {
		query = caCommonName
	}
	cmd := exec.Command("certutil", "-user", "-verifystore", "Root", query)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err == nil && matchWindowsCertutilFingerprint(string(out), sha256FP, sha1FP) {
		return true, true, "已安装到当前用户“受信任的根证书颁发机构”，白名单域名可直接解密 HTTPS"
	}
	return false, false, "证书尚未安装到 Windows 受信任根证书存储，白名单 HTTPS 解密将无法通过浏览器校验"
}

func installSystemCATrust(certPath string) error {
	// certutil -user -addstore Root 会弹出 Windows 原生安全确认窗，用户点击“是”即刻生效，免 UAC 管理员提权
	cmd := exec.Command("certutil", "-user", "-addstore", "Root", certPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("安装 Windows 根证书失败（若点击了“否”可重试）: %s", msg)
		}
		return fmt.Errorf("安装 Windows 根证书失败: %w", err)
	}
	return nil
}

func uninstallSystemCATrust(_, _, sha1FP string) error {
	target := sha1FP
	if target == "" {
		target = caCommonName
	}
	cmd := exec.Command("certutil", "-user", "-delstore", "Root", target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("卸载 Windows 根证书失败: %s", msg)
		}
		return fmt.Errorf("卸载 Windows 根证书失败: %w", err)
	}
	return nil
}
