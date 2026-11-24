package core

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
)

const (
	DarwinLibName       = "libflyshadow_core_lib.dylib"
	WindowsLibName      = "flyshadow_core_lib.dll"
	GeoIPFileName       = "Country-only-cn-private.mmdb"
	GeoIPJarEntry       = "geoip/Country-only-cn-private.mmdb"
	DarwinJarEntry      = "darwin-aarch64/libflyshadow_core_lib.dylib"
	DarwinAMD64JarEntry = "darwin-x86-64/libflyshadow_core_lib.dylib"
	WindowsJarEntry     = "win32-x86-64/flyshadow_core_lib.dll"
)

func darwinJarEntries(goarch string) []string {
	switch goarch {
	case "amd64":
		return []string{DarwinAMD64JarEntry, "darwin-x86_64/" + DarwinLibName}
	default:
		return []string{DarwinJarEntry}
	}
}

// LocateCoreLib 按平台优先级自动查找 FlyShadow 原生动态库，必要时从官方 JAR 解压缓存。
func LocateCoreLib() (string, error) {
	// 1. 显式环境变量覆盖
	if envPath := strings.TrimSpace(os.Getenv("FLYSHADOW_CORE_LIB")); envPath != "" {
		if isRegularFile(envPath) {
			return envPath, nil
		}
		return "", fmt.Errorf("环境变量 FLYSHADOW_CORE_LIB 指向的文件不存在: %s", envPath)
	}

	switch runtime.GOOS {
	case "darwin":
		return locateDarwinCoreLib()
	case "windows":
		return locateWindowsCoreLib()
	default:
		return "", fmt.Errorf("暂不支持当前平台 %s 的原生核心库", runtime.GOOS)
	}
}

// LocateGeoIP 查找或从官方 JAR 提取 GeoIP 数据库，返回路径与二进制内容。
func LocateGeoIP() (string, []byte, error) {
	if envPath := strings.TrimSpace(os.Getenv("FLYSHADOW_GEOIP")); envPath != "" {
		b, err := os.ReadFile(envPath)
		if err != nil {
			return "", nil, err
		}
		return envPath, b, nil
	}

	candidates := commonAssetDirs()
	for _, dir := range candidates {
		p := filepath.Join(dir, GeoIPFileName)
		if isRegularFile(p) {
			b, err := os.ReadFile(p)
			if err == nil && len(b) > 0 {
				return p, b, nil
			}
		}
	}

	// 尝试从官方 common-desktop-*.jar 提取并缓存
	for _, jarPath := range findOfficialJars() {
		cachePath, err := extractFromJarCached(jarPath, GeoIPJarEntry, GeoIPFileName)
		if err == nil {
			b, readErr := os.ReadFile(cachePath)
			if readErr == nil && len(b) > 0 {
				return cachePath, b, nil
			}
		}
	}

	return "", nil, errors.New("未找到 Country-only-cn-private.mmdb GeoIP 数据库")
}

func locateDarwinCoreLib() (string, error) {
	// 1. 程序同级目录 & .app/Contents/Resources
	for _, dir := range executableCandidateDirs() {
		p := filepath.Join(dir, DarwinLibName)
		if isRegularFile(p) {
			return p, nil
		}
	}

	// 2. ~/.FlyShadow/lib/<最新版本>/libflyshadow_core_lib.dylib
	if dir, err := config.DefaultDir(); err == nil {
		if p, ok := findLatestVersionedLib(filepath.Join(dir, "lib"), DarwinLibName); ok {
			return p, nil
		}
	}

	// 3. 从 /Applications/FlyShadow.app/Contents/app/common-desktop-*.jar 自动解压（按当前架构选择）
	cacheFileName := fmt.Sprintf("darwin-%s-%s", runtime.GOARCH, DarwinLibName)
	for _, jarPath := range findOfficialJars() {
		for _, entry := range darwinJarEntries(runtime.GOARCH) {
			if extracted, err := extractFromJarCached(jarPath, entry, cacheFileName); err == nil {
				return extracted, nil
			}
		}
	}

	return "", fmt.Errorf("未找到 %s（已检查程序目录、~/.FlyShadow/lib 与 /Applications/FlyShadow.app）", DarwinLibName)
}

func locateWindowsCoreLib() (string, error) {
	// 1. 程序同级目录
	for _, dir := range executableCandidateDirs() {
		p := filepath.Join(dir, WindowsLibName)
		if isRegularFile(p) {
			return p, nil
		}
	}

	// 2. %USERPROFILE%\.FlyShadow\lib\<最新版本>\flyshadow_core_lib.dll
	if dir, err := config.DefaultDir(); err == nil {
		if p, ok := findLatestVersionedLib(filepath.Join(dir, "lib"), WindowsLibName); ok {
			return p, nil
		}
	}

	// 3. Windows 常见安装目录
	for _, dir := range windowsInstallDirs() {
		for _, sub := range []string{"", "app", "lib", "resources"} {
			p := filepath.Join(dir, sub, WindowsLibName)
			if isRegularFile(p) {
				return p, nil
			}
		}
	}

	// 4. 从安装目录内的 common-desktop-*.jar 提取
	for _, jarPath := range findOfficialJars() {
		if extracted, err := extractFromJarCached(jarPath, WindowsJarEntry, WindowsLibName); err == nil {
			return extracted, nil
		}
	}

	return "", fmt.Errorf("未找到 %s（已检查程序目录、%%USERPROFILE%%\\.FlyShadow\\lib 与 FlyShadow 常见安装目录）", WindowsLibName)
}

func executableCandidateDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		dirs = append(dirs, exeDir)
		// macOS .app/Contents/MacOS -> .app/Contents/Resources & .app/Contents/Frameworks
		if runtime.GOOS == "darwin" {
			dirs = append(dirs,
				filepath.Clean(filepath.Join(exeDir, "..", "Resources")),
				filepath.Clean(filepath.Join(exeDir, "..", "Frameworks")),
			)
		}
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	return dirs
}

func commonAssetDirs() []string {
	dirs := executableCandidateDirs()
	if cfgDir, err := config.DefaultDir(); err == nil {
		dirs = append(dirs,
			cfgDir,
			filepath.Join(cfgDir, "geoip"),
			filepath.Join(cfgDir, "flyshadow-bar-cache"),
		)
	}
	return dirs
}

func windowsInstallDirs() []string {
	var dirs []string
	for _, env := range []string{"LOCALAPPDATA", "ProgramFiles", "ProgramFiles(x86)"} {
		base := strings.TrimSpace(os.Getenv(env))
		if base == "" {
			continue
		}
		dirs = append(dirs,
			filepath.Join(base, "FlyShadow"),
			filepath.Join(base, "Programs", "FlyShadow"),
		)
	}
	return dirs
}

func findOfficialJars() []string {
	var patterns []string
	switch runtime.GOOS {
	case "darwin":
		patterns = append(patterns, "/Applications/FlyShadow.app/Contents/app/common-desktop-*.jar")
		if home, err := os.UserHomeDir(); err == nil {
			patterns = append(patterns, filepath.Join(home, "Applications", "FlyShadow.app", "Contents", "app", "common-desktop-*.jar"))
		}
	case "windows":
		for _, dir := range windowsInstallDirs() {
			patterns = append(patterns, filepath.Join(dir, "app", "common-desktop-*.jar"))
			patterns = append(patterns, filepath.Join(dir, "common-desktop-*.jar"))
		}
	}

	var jars []string
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err == nil {
			jars = append(jars, matches...)
		}
	}
	sort.Strings(jars)
	return jars
}

// findLatestVersionedLib 在 libRoot 下按版本号降序查找包含 targetFile 的最新版本目录。
func findLatestVersionedLib(libRoot, targetFile string) (string, bool) {
	entries, err := os.ReadDir(libRoot)
	if err != nil {
		return "", false
	}
	type candidate struct {
		ver  string
		path string
	}
	var found []candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p := filepath.Join(libRoot, entry.Name(), targetFile)
		if isRegularFile(p) {
			found = append(found, candidate{ver: entry.Name(), path: p})
		}
	}
	if len(found) == 0 {
		return "", false
	}
	sort.Slice(found, func(i, j int) bool {
		return compareVersionDesc(found[i].ver, found[j].ver)
	})
	return found[0].path, true
}

// compareVersionDesc 按语义化版本降序比较（a > b 返回 true）。
func compareVersionDesc(a, b string) bool {
	pa := parseVersionParts(a)
	pb := parseVersionParts(b)
	n := max(len(pa), len(pb))
	for i := 0; i < n; i++ {
		var va, vb int
		if i < len(pa) {
			va = pa[i]
		}
		if i < len(pb) {
			vb = pb[i]
		}
		if va != vb {
			return va > vb
		}
	}
	return a > b
}

func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	raw := strings.Split(v, ".")
	parts := make([]int, 0, len(raw))
	for _, s := range raw {
		n, err := strconv.Atoi(s)
		if err != nil {
			n = 0
		}
		parts = append(parts, n)
	}
	return parts
}

func extractFromJarCached(jarPath, entryName, outFileName string) (string, error) {
	cfgDir, err := config.DefaultDir()
	if err != nil {
		return "", err
	}
	cacheDir := filepath.Join(cfgDir, "flyshadow-bar-cache")
	outPath := filepath.Join(cacheDir, outFileName)

	jarInfo, err := os.Stat(jarPath)
	if err != nil {
		return "", err
	}
	// 如果缓存文件已存在且不早于 JAR，直接复用
	if outInfo, err := os.Stat(outPath); err == nil && outInfo.Size() > 0 && !outInfo.ModTime().Before(jarInfo.ModTime()) {
		return outPath, nil
	}

	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	var target *zip.File
	for _, f := range zr.File {
		if f.Name == entryName {
			target = f
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("JAR 中未包含 %s", entryName)
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	rc, err := target.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	tmp, err := os.CreateTemp(cacheDir, ".extract-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, rc); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return "", err
	}
	cleanup = false
	return outPath, nil
}

func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}
