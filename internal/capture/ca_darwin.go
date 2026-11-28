//go:build darwin

package capture

import (
	"fmt"
	"os/exec"
	"strings"
)

func platformInstallPromptHint() string {
	return "点击安装后会弹出 macOS 系统原生授权窗口（支持 Touch ID 或登录密码），确认后将自动把本机根证书导入系统钥匙串并设为“始终信任”，无需手动修改钥匙串设置。"
}

func checkSystemCATrust(certPath, sha256FP, _ string) (installed bool, trusted bool, detail string) {
	// 1. 先检查系统钥匙串与登录钥匙串中是否存在指纹匹配的当前 CA
	out, err := exec.Command("security", "find-certificate", "-a", "-Z", "-c", caCommonName).CombinedOutput()
	if err == nil && sha256FP != "" && strings.Contains(strings.ToUpper(string(out)), sha256FP) {
		installed = true
	}

	// 2. 通过 security verify-cert 验证该根证书是否已被系统信任
	if verifyOut, verifyErr := exec.Command("security", "verify-cert", "-c", certPath).CombinedOutput(); verifyErr == nil {
		installed = true
		trusted = true
		return true, true, "已安装并受 macOS 系统信任，白名单域名可直接解密 HTTPS"
	} else if installed {
		msg := strings.TrimSpace(string(verifyOut))
		if msg == "" {
			msg = "证书已导入钥匙串但尚未设为“始终信任”"
		}
		return true, false, msg
	}

	return false, false, "证书尚未安装到 macOS 系统钥匙串，白名单 HTTPS 解密将无法通过浏览器校验"
}

func installSystemCATrust(certPath string) error {
	// 使用 macOS 原生 osascript 提权弹窗（支持 Touch ID / 密码），一步导入系统钥匙串并标记为 trustRoot
	shellCmd := fmt.Sprintf("security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s", shellQuote(certPath))
	appleScript := fmt.Sprintf(`do shell script %s with administrator privileges`, appleScriptQuote(shellCmd))
	out, err := exec.Command("osascript", "-e", appleScript).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "-128") || strings.Contains(strings.ToLower(msg), "canceled") {
			return fmt.Errorf("已取消系统授权，证书未安装")
		}
		if msg != "" {
			return fmt.Errorf("安装并信任 macOS 根证书失败: %s", msg)
		}
		return fmt.Errorf("安装并信任 macOS 根证书失败: %w", err)
	}
	return nil
}

func uninstallSystemCATrust(certPath, sha256FP, _ string) error {
	var cmds []string
	cmds = append(cmds, fmt.Sprintf("security remove-trusted-cert -d %s || true", shellQuote(certPath)))
	if sha256FP != "" {
		cmds = append(cmds, fmt.Sprintf("security delete-certificate -Z %s /Library/Keychains/System.keychain || true", shellQuote(sha256FP)))
	}
	shellCmd := strings.Join(cmds, "; ")
	appleScript := fmt.Sprintf(`do shell script %s with administrator privileges`, appleScriptQuote(shellCmd))
	out, err := exec.Command("osascript", "-e", appleScript).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "-128") || strings.Contains(strings.ToLower(msg), "canceled") {
			return fmt.Errorf("已取消系统授权，证书未卸载")
		}
		if msg != "" {
			return fmt.Errorf("卸载 macOS 根证书失败: %s", msg)
		}
		return fmt.Errorf("卸载 macOS 根证书失败: %w", err)
	}
	return nil
}

func appleScriptQuote(s string) string {
	esc := strings.ReplaceAll(s, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	return `"` + esc + `"`
}
