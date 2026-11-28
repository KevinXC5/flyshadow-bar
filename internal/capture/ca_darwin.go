//go:build darwin

package capture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func loginKeychainPath() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		db := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
		if _, statErr := os.Stat(db); statErr == nil {
			return db
		}
		legacy := filepath.Join(home, "Library", "Keychains", "login.keychain")
		if _, statErr := os.Stat(legacy); statErr == nil {
			return legacy
		}
		return db
	}
	return "login.keychain-db"
}

func platformInstallPromptHint() string {
	return "点击安装后会弹出 macOS 系统原生授权窗口（支持 Touch ID 或登录密码），确认后将自动把本机根证书导入当前用户登录钥匙串并设为“始终信任”，无需手动修改钥匙串设置。"
}

func checkSystemCATrust(certPath, sha256FP, _ string) (installed bool, trusted bool, detail string) {
	// 1. 先检查登录钥匙串与系统钥匙串中是否存在指纹匹配的当前 CA
	out, err := exec.Command("security", "find-certificate", "-a", "-Z", "-c", caCommonName).CombinedOutput()
	if err == nil && sha256FP != "" && strings.Contains(strings.ToUpper(string(out)), sha256FP) {
		installed = true
	}

	// 2. 通过 security verify-cert 验证该根证书是否已被系统信任（同时检查用户域与系统域信任设置）
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

	return false, false, "证书尚未安装到 macOS 钥匙串，白名单 HTTPS 解密将无法通过浏览器校验"
}

func installSystemCATrust(certPath string) error {
	// macOS 11+ 在 osascript 非交互 root 会话中调用 `security add-trusted-cert -d` 会触发
	// `SecTrustSettingsSetTrustSettings: The authorization was denied since no user interaction was possible`。
	// 直接在当前 GUI 登录会话中写入用户登录钥匙串（不加 -d），系统会自动弹出原生 Touch ID / 密码确认框，
	// 确认后即刻对 Chrome / Safari / Edge / curl / Go 全链路生效且仅影响当前用户。
	kc := loginKeychainPath()
	out, err := exec.Command("security", "add-trusted-cert", "-r", "trustRoot", "-k", kc, certPath).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		lower := strings.ToLower(msg)
		if strings.Contains(msg, "-128") || strings.Contains(lower, "canceled") || strings.Contains(lower, "cancelled") {
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
	kc := loginKeychainPath()

	// 1. 先清理当前用户登录钥匙串与用户域信任设置（无需 root）
	_, _ = exec.Command("security", "remove-trusted-cert", certPath).CombinedOutput()
	if sha256FP != "" {
		_, _ = exec.Command("security", "delete-certificate", "-Z", sha256FP, kc).CombinedOutput()
	}

	// 2. 兼容清理旧版本曾导入至 System.keychain 的证书（仅当确实存在于系统钥匙串时才弹一次提权）
	sysOut, sysErr := exec.Command("security", "find-certificate", "-a", "-Z", "-c", caCommonName, "/Library/Keychains/System.keychain").CombinedOutput()
	if sysErr == nil && sha256FP != "" && strings.Contains(strings.ToUpper(string(sysOut)), sha256FP) {
		var cmds []string
		cmds = append(cmds, fmt.Sprintf("security remove-trusted-cert -d %s || true", shellQuote(certPath)))
		cmds = append(cmds, fmt.Sprintf("security delete-certificate -Z %s /Library/Keychains/System.keychain || true", shellQuote(sha256FP)))
		shellCmd := strings.Join(cmds, "; ")
		appleScript := fmt.Sprintf(`do shell script %s with administrator privileges`, appleScriptQuote(shellCmd))
		out, err := exec.Command("osascript", "-e", appleScript).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if strings.Contains(msg, "-128") || strings.Contains(strings.ToLower(msg), "canceled") {
				return fmt.Errorf("已取消系统授权，系统钥匙串中的旧证书未卸载")
			}
			if msg != "" {
				return fmt.Errorf("卸载 macOS 系统钥匙串中的旧根证书失败: %s", msg)
			}
			return fmt.Errorf("卸载 macOS 根证书失败: %w", err)
		}
	}
	return nil
}

func appleScriptQuote(s string) string {
	esc := strings.ReplaceAll(s, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	return `"` + esc + `"`
}
