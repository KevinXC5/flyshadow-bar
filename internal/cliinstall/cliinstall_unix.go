//go:build !windows

package cliinstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func ensurePlatform(opts Options) (Status, error) {
	exe := filepath.Clean(opts.ExecutablePath)
	home := resolveHomeDir(opts.HomeDir)
	envPATH := resolveEffectivePATH(opts.EnvPATH, home)

	// 显式指定目录时直接在该目录安装
	if dir := expandHome(strings.TrimSpace(opts.TargetDir), home); dir != "" {
		linkPath, changed, err := ensureSymlinkInDir(dir, exe, true)
		if err != nil {
			return Status{}, err
		}
		return Status{
			Installed:  true,
			LinkPath:   linkPath,
			TargetPath: exe,
			InPATH:     pathListContainsDir(envPATH, dir),
			Changed:    changed,
			Detail:     formatUnixDetail(linkPath, pathListContainsDir(envPATH, dir)),
		}, nil
	}

	candidates := opts.CandidateDirs
	if len(candidates) == 0 {
		candidates = defaultUnixCandidateDirs(home, envPATH)
	}

	// 第一轮：若已有指向 FlyShadow Bar 的旧软链（包括断链），优先原地更新，避免多处留下重复链接
	for _, dir := range candidates {
		linkPath := filepath.Join(dir, CommandName)
		if !isExistingOwnSymlink(linkPath, exe) {
			continue
		}
		if !isDirWritable(dir) {
			continue
		}
		updatedPath, changed, err := ensureSymlinkInDir(dir, exe, false)
		if err == nil {
			inPath := pathListContainsDir(envPATH, dir)
			return Status{
				Installed:  true,
				LinkPath:   updatedPath,
				TargetPath: exe,
				InPATH:     inPath,
				Changed:    changed,
				Detail:     formatUnixDetail(updatedPath, inPath),
			}, nil
		}
	}

	// 第二轮：选择第一个已存在且当前用户可写、且不存在非己方冲突文件的标准 PATH 目录
	for _, dir := range candidates {
		if !isExistingDir(dir) || !isDirWritable(dir) {
			continue
		}
		linkPath, changed, err := ensureSymlinkInDir(dir, exe, false)
		if err == nil {
			inPath := pathListContainsDir(envPATH, dir)
			return Status{
				Installed:  true,
				LinkPath:   linkPath,
				TargetPath: exe,
				InPATH:     inPath,
				Changed:    changed,
				Detail:     formatUnixDetail(linkPath, inPath),
			}, nil
		}
	}

	// 第三轮兜底：若系统标准目录均不可写，自动创建用户级 ~/.local/bin 并建立软链
	fallbackDir := ""
	if len(opts.CandidateDirs) > 0 {
		fallbackDir = opts.CandidateDirs[len(opts.CandidateDirs)-1]
	} else if home != "" {
		fallbackDir = filepath.Join(home, ".local", "bin")
	}
	if fallbackDir == "" {
		return Status{}, errors.New("未找到可写的 PATH 目录以安装 fsbar 命令行链接")
	}
	linkPath, changed, err := ensureSymlinkInDir(fallbackDir, exe, true)
	if err != nil {
		return Status{}, err
	}
	inPath := pathListContainsDir(envPATH, fallbackDir)
	return Status{
		Installed:  true,
		LinkPath:   linkPath,
		TargetPath: exe,
		InPATH:     inPath,
		Changed:    changed,
		Detail:     formatUnixDetail(linkPath, inPath),
	}, nil
}

func detectPlatform(opts Options) Status {
	exe := filepath.Clean(opts.ExecutablePath)
	home := resolveHomeDir(opts.HomeDir)
	envPATH := resolveEffectivePATH(opts.EnvPATH, home)
	candidates := opts.CandidateDirs
	if len(candidates) == 0 {
		candidates = defaultUnixCandidateDirs(home, envPATH)
	}

	for _, dir := range candidates {
		linkPath := filepath.Join(dir, CommandName)
		target, err := os.Readlink(linkPath)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(target) {
			target = filepath.Clean(filepath.Join(dir, target))
		}
		if exe != "" && filepath.Clean(target) == exe {
			inPath := pathListContainsDir(envPATH, dir)
			return Status{
				Installed:  true,
				LinkPath:   linkPath,
				TargetPath: target,
				InPATH:     inPath,
				Detail:     formatUnixDetail(linkPath, inPath),
			}
		}
		if isOwnTargetPath(target) {
			inPath := pathListContainsDir(envPATH, dir)
			return Status{
				Installed:  true,
				LinkPath:   linkPath,
				TargetPath: target,
				InPATH:     inPath,
				Detail:     formatUnixDetail(linkPath, inPath),
			}
		}
	}
	return Status{
		Installed: false,
		Detail:    "尚未链接到 PATH 目录",
	}
}

// defaultUnixCandidateDirs 按通用性与免提权原则返回候选 PATH 目录列表。
func defaultUnixCandidateDirs(home, envPATH string) []string {
	var raw []string
	if runtime.GOOS == "darwin" {
		// /usr/local/bin 位于 macOS /etc/paths 系统默认 PATH；
		// /opt/homebrew/bin 为 Apple Silicon Homebrew 默认用户可写目录；
		// ~/.local/bin 与 ~/bin 为常见用户级 PATH 目录。
		raw = append(raw, "/usr/local/bin", "/opt/homebrew/bin")
	}
	if home != "" {
		raw = append(raw, filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"))
	}
	if runtime.GOOS != "darwin" {
		raw = append(raw, "/usr/local/bin")
	}

	// 去重并保持顺序
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, d := range raw {
		clean := filepath.Clean(d)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}

	// 若用户当前 PATH 中已存在其它用户级目录（位于 home 下），补充为备选
	if home != "" && envPATH != "" {
		homePrefix := filepath.Clean(home) + string(os.PathSeparator)
		for _, entry := range filepath.SplitList(envPATH) {
			clean := filepath.Clean(strings.TrimSpace(entry))
			if !strings.HasPrefix(clean, homePrefix) {
				continue
			}
			if _, ok := seen[clean]; ok {
				continue
			}
			seen[clean] = struct{}{}
			out = append(out, clean)
		}
	}
	return out
}

// resolveEffectivePATH 汇总当前进程 PATH 与 macOS /etc/paths，
// 避免从 Finder / LaunchServices 启动时丢失 /usr/local/bin 或 /opt/homebrew/bin 判断。
func resolveEffectivePATH(override, home string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	parts := filepath.SplitList(os.Getenv("PATH"))
	if runtime.GOOS == "darwin" {
		if data, err := os.ReadFile("/etc/paths"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if trimmed := strings.TrimSpace(line); trimmed != "" {
					parts = append(parts, trimmed)
				}
			}
		}
		if entries, err := os.ReadDir("/etc/paths.d"); err == nil {
			for _, ent := range entries {
				if ent.IsDir() {
					continue
				}
				if data, err := os.ReadFile(filepath.Join("/etc/paths.d", ent.Name())); err == nil {
					for _, line := range strings.Split(string(data), "\n") {
						if trimmed := strings.TrimSpace(line); trimmed != "" {
							parts = append(parts, trimmed)
						}
					}
				}
			}
		}
		// Homebrew 目录在 Apple Silicon 上通常在 shell rc 中加入 PATH
		if isExistingDir("/opt/homebrew/bin") {
			parts = append(parts, "/opt/homebrew/bin")
		}
	}
	if home != "" {
		// 检查常见 shell 配置文件是否已配置 ~/.local/bin
		localBin := filepath.Join(home, ".local", "bin")
		if shellConfigMentions(home, ".local/bin") {
			parts = append(parts, localBin)
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

func shellConfigMentions(home, needle string) bool {
	for _, name := range []string{".zshrc", ".zprofile", ".bashrc", ".bash_profile", ".profile"} {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err == nil && strings.Contains(string(data), needle) {
			return true
		}
	}
	return false
}

func resolveHomeDir(override string) string {
	if strings.TrimSpace(override) != "" {
		return filepath.Clean(override)
	}
	home, _ := os.UserHomeDir()
	return home
}

func expandHome(p, home string) string {
	if p == "" || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func isExistingDir(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

func isDirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".fsbar-write-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func isExistingOwnSymlink(linkPath, currentExe string) bool {
	info, err := os.Lstat(linkPath)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Clean(filepath.Join(filepath.Dir(linkPath), target))
	}
	if currentExe != "" && filepath.Clean(target) == filepath.Clean(currentExe) {
		return true
	}
	return isOwnTargetPath(target)
}

// ensureSymlinkInDir 在 dir 下原子创建或更新 fsbar 软链，绝不覆盖非 FlyShadow Bar 的已有文件。
func ensureSymlinkInDir(dir, targetExe string, createDir bool) (string, bool, error) {
	if createDir {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", false, fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	linkPath := filepath.Join(dir, CommandName)

	if info, err := os.Lstat(linkPath); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return "", false, fmt.Errorf("%s 已存在且不是软链接，为避免冲突未覆盖", linkPath)
		}
		existing, rerr := os.Readlink(linkPath)
		if rerr != nil {
			return "", false, fmt.Errorf("读取已有软链 %s 失败: %w", linkPath, rerr)
		}
		if !filepath.IsAbs(existing) {
			existing = filepath.Clean(filepath.Join(dir, existing))
		}
		if filepath.Clean(existing) == filepath.Clean(targetExe) {
			return linkPath, false, nil
		}
		if !isOwnTargetPath(existing) {
			return "", false, fmt.Errorf("%s 指向其他程序 (%s)，为避免冲突未覆盖", linkPath, existing)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}

	tmpLink := filepath.Join(dir, fmt.Sprintf(".%s.tmp.%d", CommandName, os.Getpid()))
	_ = os.Remove(tmpLink)
	if err := os.Symlink(targetExe, tmpLink); err != nil {
		return "", false, fmt.Errorf("创建软链失败: %w", err)
	}
	if err := os.Rename(tmpLink, linkPath); err != nil {
		_ = os.Remove(tmpLink)
		return "", false, fmt.Errorf("更新软链 %s 失败: %w", linkPath, err)
	}
	return linkPath, true, nil
}

func formatUnixDetail(linkPath string, inPath bool) string {
	if inPath {
		return linkPath
	}
	return fmt.Sprintf("%s（请确认 %s 已加入 PATH）", linkPath, filepath.Dir(linkPath))
}
