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
	WinTunDLLName       = "wintun.dll"
	WinTunJarEntry      = "win32-x86-64/wintun.dll"
)

var (
	officialJarsFinder       = findOfficialJars
	legacyDirResolver        = config.LegacyDir
	executableDirsFinder     = executableCandidateDirs
	windowsInstallDirsFinder = windowsInstallDirs
)

func darwinJarEntries(goarch string) []string {
	switch goarch {
	case "amd64":
		return []string{DarwinAMD64JarEntry, "darwin-x86_64/" + DarwinLibName}
	default:
		return []string{DarwinJarEntry, "darwin-arm64/" + DarwinLibName}
	}
}

func windowsJarEntries() []string {
	return []string{
		WindowsJarEntry,
		"win32-x86_64/" + WindowsLibName,
		WindowsLibName,
	}
}

func geoIPJarEntries() []string {
	return []string{
		GeoIPJarEntry,
		GeoIPFileName,
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

	// 1. 程序目录及 ~/.flyshadowbar 显式放置的 GeoIP 文件（不含自动解压缓存目录）
	for _, dir := range geoIPExplicitDirs() {
		p := filepath.Join(dir, GeoIPFileName)
		if isRegularFile(p) {
			b, err := os.ReadFile(p)
			if err == nil && len(b) > 0 {
				return p, b, nil
			}
		}
	}

	// 2. 优先从官方 JAR（FlyShadow.jar / common-desktop-*.jar）提取并按内容指纹自动刷新缓存
	for _, jarPath := range officialJarsFinder() {
		cachePath, err := extractFirstFromJarCached(jarPath, geoIPJarEntries(), GeoIPFileName)
		if err == nil {
			b, readErr := os.ReadFile(cachePath)
			if readErr == nil && len(b) > 0 {
				return cachePath, b, nil
			}
		}
	}

	// 3. 兜底回退至已有缓存及原版 ~/.FlyShadow 目录
	for _, dir := range geoIPFallbackDirs() {
		p := filepath.Join(dir, GeoIPFileName)
		if isRegularFile(p) {
			b, err := os.ReadFile(p)
			if err == nil && len(b) > 0 {
				return p, b, nil
			}
		}
	}

	return "", nil, errors.New("未找到 Country-only-cn-private.mmdb GeoIP 数据库")
}

func locateDarwinCoreLib() (string, error) {
	// 1. 程序同级目录 & .app/Contents/Resources
	for _, dir := range executableDirsFinder() {
		p := filepath.Join(dir, DarwinLibName)
		if isRegularFile(p) {
			return p, nil
		}
	}

	// 2. ~/.flyshadowbar/lib/<最新版本>（用户显式放置的覆盖版本）
	if dir, err := config.DefaultDir(); err == nil {
		if p, ok := findLatestVersionedLib(filepath.Join(dir, "lib"), DarwinLibName); ok {
			return p, nil
		}
	}

	// 3. 优先从官方 JAR（FlyShadow.jar / common-desktop-*.jar）自动解压并按内容指纹刷新缓存。
	// 官方新版客户端已直接随 JAR 分发核心库（不再更新 ~/.FlyShadow/lib），必须先于旧目录与离线缓存检查，
	// 避免用户更新官方客户端后仍误加载旧版核心库。
	cacheFileName := fmt.Sprintf("darwin-%s-%s", runtime.GOARCH, DarwinLibName)
	for _, jarPath := range officialJarsFinder() {
		if extracted, err := extractFirstFromJarCached(jarPath, darwinJarEntries(runtime.GOARCH), cacheFileName); err == nil {
			return extracted, nil
		}
	}

	// 4. 只读兼容原版 ~/.FlyShadow/lib/<最新版本>/libflyshadow_core_lib.dylib
	if legacyDir, err := legacyDirResolver(); err == nil {
		if p, ok := findLatestVersionedLib(filepath.Join(legacyDir, "lib"), DarwinLibName); ok {
			return p, nil
		}
	}

	// 5. 兜底复用 ~/.flyshadowbar/cache 中已缓存的动态库（例如官方客户端已卸载的场景）
	if cacheDir, err := config.CacheDir(); err == nil {
		if p := filepath.Join(cacheDir, cacheFileName); isRegularFile(p) {
			return p, nil
		}
	}

	return "", fmt.Errorf("未找到 %s（已检查程序目录、~/.flyshadowbar、/Applications/FlyShadow.app 与 ~/.FlyShadow/lib）", DarwinLibName)
}

func locateWindowsCoreLib() (string, error) {
	// 1. 程序同级目录
	for _, dir := range executableDirsFinder() {
		p := filepath.Join(dir, WindowsLibName)
		if isRegularFile(p) {
			return p, nil
		}
	}

	// 2. %USERPROFILE%\.flyshadowbar\lib\<最新版本>（用户显式放置的覆盖版本）
	if dir, err := config.DefaultDir(); err == nil {
		if p, ok := findLatestVersionedLib(filepath.Join(dir, "lib"), WindowsLibName); ok {
			return p, nil
		}
	}

	// 3. 优先从安装目录内的官方 JAR（FlyShadow.jar / common-desktop-*.jar）提取并按内容指纹自动刷新缓存
	for _, jarPath := range officialJarsFinder() {
		if extracted, err := extractFirstFromJarCached(jarPath, windowsJarEntries(), WindowsLibName); err == nil {
			return extracted, nil
		}
	}

	// 4. Windows 常见安装目录中的独立 DLL
	for _, dir := range windowsInstallDirsFinder() {
		for _, sub := range []string{"", "app", "lib", "resources"} {
			p := filepath.Join(dir, sub, WindowsLibName)
			if isRegularFile(p) {
				return p, nil
			}
		}
	}

	// 5. 只读兼容原版 %USERPROFILE%\.FlyShadow\lib\<最新版本>\flyshadow_core_lib.dll
	if legacyDir, err := legacyDirResolver(); err == nil {
		if p, ok := findLatestVersionedLib(filepath.Join(legacyDir, "lib"), WindowsLibName); ok {
			return p, nil
		}
	}

	// 6. 兜底复用 %USERPROFILE%\.flyshadowbar\cache 中已缓存的动态库
	if cacheDir, err := config.CacheDir(); err == nil {
		if p := filepath.Join(cacheDir, WindowsLibName); isRegularFile(p) {
			return p, nil
		}
	}

	return "", fmt.Errorf("未找到 %s（已检查程序目录、%%USERPROFILE%%\\.flyshadowbar、FlyShadow 常见安装目录与 %%USERPROFILE%%\\.FlyShadow\\lib）", WindowsLibName)
}

func winTunJarEntries() []string {
	return []string{
		WinTunJarEntry,
		"win32-x86_64/" + WinTunDLLName,
		WinTunDLLName,
	}
}

// LocateWinTunDLL 按优先级查找 Windows 平台所需的 wintun.dll，必要时自动从官方 JAR 提取缓存。
func LocateWinTunDLL() (string, error) {
	if envPath := strings.TrimSpace(os.Getenv("FLYSHADOW_WINTUN_DLL")); envPath != "" {
		if isRegularFile(envPath) {
			return envPath, nil
		}
		return "", fmt.Errorf("环境变量 FLYSHADOW_WINTUN_DLL 指向的文件不存在: %s", envPath)
	}

	// 1. 程序同级目录
	for _, dir := range executableDirsFinder() {
		p := filepath.Join(dir, WinTunDLLName)
		if isRegularFile(p) {
			return p, nil
		}
	}

	// 2. ~/.flyshadowbar/lib 显式覆盖
	if dir, err := config.DefaultDir(); err == nil {
		if p := filepath.Join(dir, WinTunDLLName); isRegularFile(p) {
			return p, nil
		}
		if p := filepath.Join(dir, "lib", WinTunDLLName); isRegularFile(p) {
			return p, nil
		}
		if p, ok := findLatestVersionedLib(filepath.Join(dir, "lib"), WinTunDLLName); ok {
			return p, nil
		}
	}

	// 3. 优先从官方 JAR 提取 win32-x86-64/wintun.dll 并按内容指纹缓存
	for _, jarPath := range officialJarsFinder() {
		if extracted, err := extractFirstFromJarCached(jarPath, winTunJarEntries(), WinTunDLLName); err == nil {
			return extracted, nil
		}
	}

	// 4. Windows 常见安装目录
	for _, dir := range windowsInstallDirsFinder() {
		for _, sub := range []string{"", "app", "lib", "resources"} {
			p := filepath.Join(dir, sub, WinTunDLLName)
			if isRegularFile(p) {
				return p, nil
			}
		}
	}

	// 5. 只读兼容原版 ~/.FlyShadow/lib
	if legacyDir, err := legacyDirResolver(); err == nil {
		if p, ok := findLatestVersionedLib(filepath.Join(legacyDir, "lib"), WinTunDLLName); ok {
			return p, nil
		}
	}

	// 6. 兜底复用缓存目录中的 wintun.dll
	if cacheDir, err := config.CacheDir(); err == nil {
		if p := filepath.Join(cacheDir, WinTunDLLName); isRegularFile(p) {
			return p, nil
		}
	}

	return "", fmt.Errorf("未找到 %s（请确保已安装官方 FlyShadow 客户端，或将 wintun.dll 放置在程序目录或 %%USERPROFILE%%\\.flyshadowbar\\lib）", WinTunDLLName)
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

func geoIPExplicitDirs() []string {
	dirs := executableDirsFinder()
	if cfgDir, err := config.DefaultDir(); err == nil {
		dirs = append(dirs,
			cfgDir,
			filepath.Join(cfgDir, "geoip"),
		)
	}
	return dirs
}

func geoIPFallbackDirs() []string {
	var dirs []string
	if cacheDir, err := config.CacheDir(); err == nil {
		dirs = append(dirs, cacheDir)
	}
	// 只读兼容原版 ~/.FlyShadow 中已有的 GeoIP 文件
	if legacyDir, err := legacyDirResolver(); err == nil {
		dirs = append(dirs,
			legacyDir,
			filepath.Join(legacyDir, "geoip"),
			filepath.Join(legacyDir, "flyshadow-bar-cache"),
		)
	}
	return dirs
}

func windowsInstallDirs() []string {
	var raw []string
	// 优先使用 Windows 卸载注册表记录的实际安装目录，兼容自定义盘符与路径。
	raw = append(raw, discoverWindowsRegistryInstallDirs()...)
	for _, env := range []string{"LOCALAPPDATA", "ProgramFiles", "ProgramFiles(x86)"} {
		base := strings.TrimSpace(os.Getenv(env))
		if base == "" {
			continue
		}
		raw = append(raw,
			filepath.Join(base, "FlyShadow"),
			filepath.Join(base, "Programs", "FlyShadow"),
		)
	}
	return dedupeCleanPaths(raw, true)
}

func officialJarGlobsInDir(dir string) []string {
	return []string{
		filepath.Join(dir, "FlyShadow.jar"),
		filepath.Join(dir, "FlyShadow-*.jar"),
		filepath.Join(dir, "common-desktop-*.jar"),
	}
}

func findOfficialJars() []string {
	var appDirs []string
	switch runtime.GOOS {
	case "darwin":
		appDirs = append(appDirs, "/Applications/FlyShadow.app/Contents/app")
		if home, err := os.UserHomeDir(); err == nil {
			appDirs = append(appDirs, filepath.Join(home, "Applications", "FlyShadow.app", "Contents", "app"))
		}
	case "windows":
		for _, dir := range windowsInstallDirsFinder() {
			appDirs = append(appDirs, filepath.Join(dir, "app"), dir)
		}
	}
	return collectOfficialJarsFromDirs(appDirs)
}

// collectOfficialJarsFromDirs 在给定的官方应用目录集合中收集全部候选 JAR，去重后按修改时间降序排列。
func collectOfficialJarsFromDirs(appDirs []string) []string {
	var jars []string
	for _, dir := range appDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, pat := range officialJarGlobsInDir(dir) {
			matches, err := filepath.Glob(pat)
			if err == nil {
				jars = append(jars, matches...)
			}
		}
	}
	jars = dedupeCleanPaths(jars, runtime.GOOS == "windows")
	sort.Slice(jars, func(i, j int) bool {
		si, ei := os.Stat(jars[i])
		sj, ej := os.Stat(jars[j])
		if ei == nil && ej == nil && !si.ModTime().Equal(sj.ModTime()) {
			return si.ModTime().After(sj.ModTime())
		}
		return jars[i] < jars[j]
	})
	return jars
}

func dedupeCleanPaths(paths []string, caseInsensitive bool) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		cleaned := filepath.Clean(p)
		key := cleaned
		if caseInsensitive {
			key = strings.ToLower(cleaned)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cleaned)
	}
	return out
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
	return extractFirstFromJarCached(jarPath, []string{entryName}, outFileName)
}

func extractFirstFromJarCached(jarPath string, entryNames []string, outFileName string) (string, error) {
	cacheDir, err := config.CacheDir()
	if err != nil {
		return "", err
	}
	outPath := filepath.Join(cacheDir, outFileName)
	metaPath := outPath + ".meta"

	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	byName := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		byName[f.Name] = f
	}
	var target *zip.File
	for _, entryName := range entryNames {
		if f, ok := byName[entryName]; ok {
			target = f
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("JAR 中未包含 %s", strings.Join(entryNames, " / "))
	}

	// 使用 JAR 文件名 + 条目名 + CRC32 + 原始大小作为内容指纹，
	// 避免官方客户端通过 DMG/安装包升级后 JAR 修改时间早于旧缓存而误判命中。
	stamp := fmt.Sprintf("%s|%s|%08x|%d", filepath.Base(jarPath), target.Name, target.CRC32, target.UncompressedSize64)
	if outInfo, err := os.Stat(outPath); err == nil && outInfo.Size() > 0 && outInfo.Size() == int64(target.UncompressedSize64) {
		if saved, metaErr := os.ReadFile(metaPath); metaErr == nil && strings.TrimSpace(string(saved)) == stamp {
			return outPath, nil
		}
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

	_ = os.WriteFile(metaPath, []byte(stamp+"\n"), 0o644)
	return outPath, nil
}

func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}
