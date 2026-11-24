//go:build darwin

package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// darwinRelaunchWatchdogScript 在脱离旧 App 会话的独立 /bin/sh 中运行：
// 等待旧进程彻底退出且 mygo 换包临时目录清理完毕后，若新版本未由 mygo 自动拉起，
// 则使用 `/usr/bin/open -n` 重试拉起（最多 3 次），彻底避免重启更新后偶发未启动。
const darwinRelaunchWatchdogScript = `pid=$1
bundle=$2
shift 2
trap '' HUP TERM INT

# 1. 等待旧进程完全退出（最多 15 秒）
i=0
while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 300 ]; do
	sleep 0.05
	i=$((i + 1))
done

# 2. 等待 mygo 换包临时目录（.<bundle>.update-*）替换完成（最多 5 秒）
parent=$(dirname "$bundle")
base=$(basename "$bundle")
i=0
while [ "$i" -lt 100 ]; do
	found=0
	for d in "$parent/.$base.update-"*; do
		if [ -e "$d" ]; then
			found=1
			break
		fi
	done
	[ "$found" -eq 0 ] && break
	sleep 0.05
	i=$((i + 1))
done

# 3. 优先留出窗口让 mygo 自身的 open 生效，若新进程已在运行则直接退出
sleep 0.6
exe_prefix="$bundle/Contents/MacOS/"
if pgrep -f "$exe_prefix" >/dev/null 2>&1; then
	exit 0
fi

# 4. 兜底重试拉起（最多 3 次）
for try in 1 2 3; do
	if [ "$#" -gt 0 ]; then
		/usr/bin/open -n "$bundle" --args "$@" >/dev/null 2>&1 || true
	else
		/usr/bin/open -n "$bundle" >/dev/null 2>&1 || true
	fi
	sleep 0.8
	if pgrep -f "$exe_prefix" >/dev/null 2>&1; then
		exit 0
	fi
done
`

// appBundlePathFromExe 从可执行文件路径提取所属的 .app 包路径；非 .app 形态返回空字符串。
func appBundlePathFromExe(exe string) string {
	clean := filepath.Clean(strings.TrimSpace(exe))
	if clean == "" || clean == "." {
		return ""
	}
	// 标准 macOS 包路径：<App>.app/Contents/MacOS/<Binary>
	macosDir := filepath.Dir(clean)
	contentsDir := filepath.Dir(macosDir)
	bundleDir := filepath.Dir(contentsDir)
	if filepath.Base(macosDir) != "MacOS" || filepath.Base(contentsDir) != "Contents" {
		return ""
	}
	if !strings.HasSuffix(bundleDir, ".app") {
		return ""
	}
	return bundleDir
}

// armRelaunchWatchdog 在 macOS .app 正式安装形态下启动脱离当前进程组的重启兜底看门狗。
func armRelaunchWatchdog(args ...string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" {
		exe = resolved
	}
	bundle := appBundlePathFromExe(exe)
	if bundle == "" {
		return
	}
	startDarwinRelaunchWatchdog(os.Getpid(), bundle, args...)
}

func startDarwinRelaunchWatchdog(pid int, bundle string, args ...string) {
	argv := make([]string, 0, 5+len(args))
	argv = append(argv, "-c", darwinRelaunchWatchdogScript, "fsbar-relaunch", strconv.Itoa(pid), bundle)
	argv = append(argv, args...)

	cmd := exec.Command("/bin/sh", argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err == nil && cmd.Process != nil {
		_ = cmd.Process.Release()
	}
}
