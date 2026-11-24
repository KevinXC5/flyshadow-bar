package cliinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CommandName 是对外暴露的命令行入口名称。
const CommandName = "fsbar"

// Status 描述当前系统上 fsbar 命令行工具的链接与 PATH 可达状态。
type Status struct {
	Installed  bool   `json:"installed"`
	LinkPath   string `json:"linkPath,omitempty"`
	TargetPath string `json:"targetPath,omitempty"`
	InPATH     bool   `json:"inPath"`
	Changed    bool   `json:"changed"`
	Detail     string `json:"detail,omitempty"`
}

// Options 控制命令行链接安装行为。
type Options struct {
	// ExecutablePath 允许测试或调用方覆盖目标可执行文件路径；留空时使用 os.Executable()。
	ExecutablePath string
	// TargetDir 显式指定安装目录；留空时按平台策略自动选择可写的 PATH 目录。
	TargetDir string
	// CandidateDirs 供单元测试覆盖默认候选目录列表。
	CandidateDirs []string
	// EnvPATH 供单元测试覆盖 PATH 环境变量。
	EnvPATH string
	// HomeDir 供单元测试覆盖用户主目录。
	HomeDir string
}

// ShouldAutoInstall 判断当前进程可执行路径是否属于正式安装形态，
// 避免在 go run 临时构建目录、/tmp 调试目录或未拖出 macOS DMG 挂载卷（/Volumes/）时误写宿主机 PATH。
func ShouldAutoInstall(exePath string) bool {
	return shouldAutoInstallForOS(exePath, runtime.GOOS)
}

func shouldAutoInstallForOS(exePath, goos string) bool {
	p := strings.TrimSpace(exePath)
	if p == "" {
		return false
	}
	clean := filepath.Clean(p)
	lower := strings.ToLower(strings.ReplaceAll(filepath.ToSlash(clean), `\`, `/`))

	// 排除 Go 临时构建与测试二进制
	if strings.Contains(lower, "/go-build") || strings.Contains(lower, ".test") {
		return false
	}
	if tmp := strings.TrimSpace(os.TempDir()); tmp != "" {
		tmpSlash := strings.ToLower(strings.TrimRight(strings.ReplaceAll(filepath.ToSlash(filepath.Clean(tmp)), `\`, `/`), "/"))
		if tmpSlash != "" && strings.HasPrefix(lower, tmpSlash+"/") {
			return false
		}
	}
	if strings.HasPrefix(lower, "/tmp/") || strings.HasPrefix(lower, "/private/tmp/") || strings.Contains(lower, "/appdata/local/temp/") {
		return false
	}

	switch goos {
	case "darwin":
		// 仅对拖入本地的 .app 包自动链接，避免在未安装的 DMG 只读挂载卷（/Volumes/...）里创建失效链接
		if strings.HasPrefix(lower, "/volumes/") {
			return false
		}
		return strings.Contains(lower, ".app/contents/macos/")
	case "windows":
		// Windows 安装器默认安装至 %LOCALAPPDATA%\Programs\FlyShadow Bar 或用户自定义非临时目录
		return strings.HasSuffix(lower, ".exe")
	default:
		return true
	}
}

// EnsureAuto 在应用启动完成后自动确保 fsbar 链接已就绪；
// 若当前处于开发调试或未安装挂载卷运行环境，则跳过不做改动。
func EnsureAuto() (Status, error) {
	exe, err := currentExecutable()
	if err != nil {
		return Status{}, err
	}
	if !ShouldAutoInstall(exe) {
		st := Detect()
		st.Detail = "开发或临时目录运行，跳过自动链接"
		return st, nil
	}
	return Ensure(Options{ExecutablePath: exe})
}

// Ensure 安装或更新 fsbar 命令行链接到对应的 PATH 目录。
func Ensure(opts Options) (Status, error) {
	if strings.TrimSpace(opts.ExecutablePath) == "" {
		exe, err := currentExecutable()
		if err != nil {
			return Status{}, err
		}
		opts.ExecutablePath = exe
	}
	return ensurePlatform(opts)
}

// Detect 检查当前系统上 fsbar 命令行工具的安装与链接状态（只读，不做修改）。
func Detect() Status {
	exe, _ := currentExecutable()
	return detectPlatform(Options{ExecutablePath: exe})
}

func currentExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" {
		return resolved, nil
	}
	return filepath.Clean(exe), nil
}

// isOwnTargetPath 判断某个链接目标是否属于 FlyShadow Bar / fsbar 自身（避免误覆盖用户其它同名工具）。
func isOwnTargetPath(target string) bool {
	lower := strings.ToLower(filepath.ToSlash(filepath.Clean(target)))
	base := filepath.Base(lower)
	if strings.Contains(lower, "flyshadow bar.app/contents/macos/") ||
		strings.Contains(lower, "flyshadow-bar") ||
		strings.Contains(lower, "flyshadow bar") {
		return true
	}
	return base == "fsbar" || base == "fsbar.exe" || base == "flyshadow bar" || base == "flyshadow bar.exe"
}

func pathListContainsDir(pathEnv, dir string) bool {
	if strings.TrimSpace(pathEnv) == "" || strings.TrimSpace(dir) == "" {
		return false
	}
	want := normalizeDirForCompare(dir)
	for _, entry := range filepath.SplitList(pathEnv) {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if normalizeDirForCompare(entry) == want {
			return true
		}
	}
	return false
}

func normalizeDirForCompare(dir string) string {
	clean := filepath.Clean(strings.TrimSpace(dir))
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}
