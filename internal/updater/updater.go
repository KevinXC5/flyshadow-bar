package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/netproxy"

	"github.com/KevinXC5/flyshadow-bar/internal/version"
)

// CheckResult 表示版本更新检查的结果。
type CheckResult struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	HasUpdate      bool   `json:"hasUpdate"`
	ReleaseURL     string `json:"releaseUrl"`
	ReleaseNotes   string `json:"releaseNotes,omitempty"`
	AssetName      string `json:"assetName,omitempty"`
	AssetURL       string `json:"assetUrl,omitempty"`
	PublishedAt    string `json:"publishedAt,omitempty"`
}

// APIBaseURL 为 GitHub API 基础地址，测试时可替换为本地模拟服务地址。
var APIBaseURL = "https://api.github.com"

const (
	userAgent      = "flyshadow-bar"
	githubAccept   = "application/vnd.github+json"
	checkTimeout   = 15 * time.Second
	downloadBufLen = 32 * 1024
)

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	HTMLURL     string        `json:"html_url"`
	Body        string        `json:"body"`
	PublishedAt string        `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Check 查询 GitHub 最新 Release 并判断是否需要更新（自动走系统/环境代理并回退直连）。
func Check(ctx context.Context, repo string, currentVersion string) (*CheckResult, error) {
	return CheckWithProxy(ctx, repo, currentVersion, "")
}

// CheckWithProxy 查询 GitHub 最新 Release；提供 localProxyURL 时优先经由本机内置代理访问，
// 失败自动回退系统代理与直连，无需依赖操作系统全局代理开关。
func CheckWithProxy(ctx context.Context, repo string, currentVersion string, localProxyURL string) (*CheckResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if repo == "" {
		repo = strings.Trim(strings.TrimSpace(version.GithubRepo), "/")
	}
	if repo == "" {
		return nil, fmt.Errorf("未配置 GitHub 仓库地址")
	}

	currentVersion = strings.TrimSpace(currentVersion)
	if currentVersion == "" {
		currentVersion = version.Version
	}

	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(APIBaseURL, "/"), repo)
	headers := map[string]string{
		"User-Agent": userAgent,
		"Accept":     githubAccept,
	}

	resp, err := doGetWithFallback(ctx, endpoint, headers, localProxyURL, checkTimeout)
	if err != nil {
		return nil, fmt.Errorf("请求 GitHub 更新接口失败: %w", err)
	}
	defer resp.Body.Close()

	// 尚未发布任何正式版本时 GitHub 会返回 404，此时按无更新返回而非报错。
	if resp.StatusCode == http.StatusNotFound {
		return &CheckResult{
			CurrentVersion: currentVersion,
			LatestVersion:  "",
			HasUpdate:      false,
			ReleaseURL:     fmt.Sprintf("https://github.com/%s/releases", repo),
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(bodySnippet))
		if msg != "" {
			return nil, fmt.Errorf("GitHub 更新接口返回异常状态码 %d: %s", resp.StatusCode, msg)
		}
		return nil, fmt.Errorf("GitHub 更新接口返回异常状态码 %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("解析 GitHub Release 响应失败: %w", err)
	}

	result := &CheckResult{
		CurrentVersion: currentVersion,
		LatestVersion:  strings.TrimSpace(release.TagName),
		HasUpdate:      CompareVersions(release.TagName, currentVersion) > 0,
		ReleaseURL:     strings.TrimSpace(release.HTMLURL),
		ReleaseNotes:   release.Body,
		PublishedAt:    strings.TrimSpace(release.PublishedAt),
	}
	if result.ReleaseURL == "" {
		result.ReleaseURL = fmt.Sprintf("https://github.com/%s/releases", repo)
	}

	if asset, ok := selectAsset(release.Assets, runtime.GOOS, runtime.GOARCH); ok {
		result.AssetName = asset.Name
		result.AssetURL = asset.BrowserDownloadURL
	}

	return result, nil
}

// CompareVersions 按 SemVer 规则比较两个版本号。
// 返回 -1 表示 a < b，0 表示 a == b，1 表示 a > b。
// 相同主次补丁号时，正式版高于带预发布标签（如 -dev、-rc）的版本。
func CompareVersions(a, b string) int {
	va := parseSemver(a)
	vb := parseSemver(b)

	maxLen := len(va.core)
	if len(vb.core) > maxLen {
		maxLen = len(vb.core)
	}
	for i := 0; i < maxLen; i++ {
		var na, nb int
		if i < len(va.core) {
			na = va.core[i]
		}
		if i < len(vb.core) {
			nb = vb.core[i]
		}
		if na < nb {
			return -1
		}
		if na > nb {
			return 1
		}
	}

	// 主次补丁号相同，按预发布标签比较。
	switch {
	case va.prerelease == "" && vb.prerelease == "":
		return 0
	case va.prerelease == "":
		return 1
	case vb.prerelease == "":
		return -1
	}

	pa := strings.Split(va.prerelease, ".")
	pb := strings.Split(vb.prerelease, ".")
	minLen := len(pa)
	if len(pb) < minLen {
		minLen = len(pb)
	}
	for i := 0; i < minLen; i++ {
		if cmp := comparePrereleaseIdent(pa[i], pb[i]); cmp != 0 {
			return cmp
		}
	}
	if len(pa) < len(pb) {
		return -1
	}
	if len(pa) > len(pb) {
		return 1
	}
	return 0
}

type semverParts struct {
	core       []int
	prerelease string
}

func parseSemver(raw string) semverParts {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")

	// 构建元数据（+ 后内容）不参与版本优先级比较。
	if idx := strings.IndexByte(s, '+'); idx >= 0 {
		s = s[:idx]
	}

	coreStr := s
	prerelease := ""
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		coreStr = s[:idx]
		prerelease = s[idx+1:]
	}

	rawParts := strings.Split(coreStr, ".")
	core := make([]int, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part == "" {
			core = append(core, 0)
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			core = append(core, leadingDigits(part))
			continue
		}
		core = append(core, n)
	}

	return semverParts{
		core:       core,
		prerelease: prerelease,
	}
}

func leadingDigits(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

func comparePrereleaseIdent(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	isNumA := errA == nil && isAllDigits(a)
	isNumB := errB == nil && isAllDigits(b)

	switch {
	case isNumA && isNumB:
		if na < nb {
			return -1
		}
		if na > nb {
			return 1
		}
		return 0
	case isNumA && !isNumB:
		// 按 SemVer 2.0.0 规范，纯数字标识符优先级低于非数字标识符。
		return -1
	case !isNumA && isNumB:
		return 1
	default:
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// DownloadAsset 下载发布包到指定路径，并通过临时文件原子替换避免残缺文件。
func DownloadAsset(ctx context.Context, downloadURL, destPath string, onProgress func(downloaded, total int64)) error {
	return DownloadAssetWithProxy(ctx, downloadURL, destPath, "", onProgress)
}

// DownloadAssetWithProxy 下载发布包到指定路径；提供 localProxyURL 时优先经由本机内置代理访问并自动回退。
func DownloadAssetWithProxy(ctx context.Context, downloadURL, destPath, localProxyURL string, onProgress func(downloaded, total int64)) error {
	if ctx == nil {
		ctx = context.Background()
	}

	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		return fmt.Errorf("下载地址不能为空")
	}
	destPath = strings.TrimSpace(destPath)
	if destPath == "" {
		return fmt.Errorf("目标文件路径不能为空")
	}

	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}

	resp, err := doGetWithFallback(ctx, downloadURL, map[string]string{"User-Agent": userAgent}, localProxyURL, 30*time.Second)
	if err != nil {
		return fmt.Errorf("下载更新资源失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载更新资源失败: 服务器返回状态码 %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp(destDir, ".flyshadow-update-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时下载文件失败: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	total := resp.ContentLength
	var downloaded int64
	buf := make([]byte, downloadBufLen)

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			written, writeErr := tmpFile.Write(buf[:n])
			if writeErr != nil {
				return fmt.Errorf("写入下载文件失败: %w", writeErr)
			}
			if written != n {
				return fmt.Errorf("写入下载文件不完整")
			}
			downloaded += int64(written)
			if onProgress != nil {
				onProgress(downloaded, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("读取下载流失败: %w", readErr)
		}
	}

	if downloaded == 0 && onProgress != nil {
		onProgress(0, total)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("关闭临时下载文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("保存下载文件到目标路径失败: %w", err)
	}
	cleanupTmp = false
	return nil
}

// selectAsset 根据目标操作系统与 CPU 架构挑选最匹配的 Release 附件，优先选择 .zip 包。
func selectAsset(assets []githubAsset, goos, goarch string) (githubAsset, bool) {
	var best githubAsset
	bestScore := 0

	for _, asset := range assets {
		if strings.TrimSpace(asset.BrowserDownloadURL) == "" {
			continue
		}
		score := scoreAsset(asset.Name, goos, goarch)
		if score > bestScore {
			bestScore = score
			best = asset
		}
	}
	if bestScore == 0 {
		return githubAsset{}, false
	}
	return best, true
}

func scoreAsset(name, goos, goarch string) int {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || isAuxAsset(lower) {
		return 0
	}

	tokens := tokenizeAssetName(lower)
	if !matchesOS(lower, tokens, goos) {
		return 0
	}

	score := 0
	switch {
	case matchesArch(lower, tokens, goarch):
		score += 100
	case goos == "darwin" && hasAnyToken(tokens, "universal"):
		score += 60
	case hasAnyKnownArch(lower, tokens):
		// 文件名明确标注了其他架构，不匹配当前设备。
		return 0
	default:
		// 未标注架构的同平台安装包，作为兜底候选。
		score += 20
	}

	switch {
	case strings.HasSuffix(lower, ".dmg"), strings.HasSuffix(lower, ".pkg"):
		score += 24
	case strings.HasSuffix(lower, ".zip"):
		score += 20
	case strings.HasSuffix(lower, ".msi"), (strings.HasSuffix(lower, ".exe") && strings.Contains(lower, "setup")):
		score += 24
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		score += 12
	case strings.HasSuffix(lower, ".exe"):
		score += 8
	default:
		score += 1
	}

	return score
}

func isAuxAsset(lowerName string) bool {
	if strings.Contains(lowerName, "checksum") || strings.HasPrefix(lowerName, "update-") {
		return true
	}
	auxSuffixes := []string{".sha256", ".sha512", ".md5", ".sig", ".asc", ".txt", ".json", ".sbom", ".delta"}
	for _, suffix := range auxSuffixes {
		if strings.HasSuffix(lowerName, suffix) {
			return true
		}
	}
	return false
}

func tokenizeAssetName(lowerName string) map[string]struct{} {
	fields := strings.FieldsFunc(lowerName, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	tokens := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		tokens[f] = struct{}{}
	}
	return tokens
}

func hasAnyToken(tokens map[string]struct{}, candidates ...string) bool {
	for _, c := range candidates {
		if _, ok := tokens[c]; ok {
			return true
		}
	}
	return false
}

func matchesOS(lowerName string, tokens map[string]struct{}, goos string) bool {
	switch goos {
	case "darwin":
		if hasAnyToken(tokens, "windows", "win", "win64", "win32", "linux") {
			return false
		}
		return hasAnyToken(tokens, "darwin", "macos", "osx") ||
			strings.HasSuffix(lowerName, ".dmg") ||
			strings.HasSuffix(lowerName, ".pkg")
	case "windows":
		if hasAnyToken(tokens, "darwin", "macos", "osx", "linux") {
			return false
		}
		return hasAnyToken(tokens, "windows", "win", "win64", "win32") ||
			strings.HasSuffix(lowerName, ".exe") ||
			strings.HasSuffix(lowerName, ".msi")
	case "linux":
		if hasAnyToken(tokens, "darwin", "macos", "osx", "windows", "win", "win64", "win32") {
			return false
		}
		return hasAnyToken(tokens, "linux")
	default:
		return hasAnyToken(tokens, strings.ToLower(goos))
	}
}

func matchesArch(lowerName string, tokens map[string]struct{}, goarch string) bool {
	switch goarch {
	case "amd64":
		return hasAnyToken(tokens, "amd64", "x64") || strings.Contains(lowerName, "x86_64") || strings.Contains(lowerName, "x86-64")
	case "arm64":
		return hasAnyToken(tokens, "arm64", "aarch64")
	case "386":
		return hasAnyToken(tokens, "386", "i386", "i686", "x86") && !strings.Contains(lowerName, "x86_64") && !strings.Contains(lowerName, "x86-64")
	default:
		return hasAnyToken(tokens, strings.ToLower(goarch))
	}
}

func hasAnyKnownArch(lowerName string, tokens map[string]struct{}) bool {
	return hasAnyToken(tokens, "amd64", "x64", "arm64", "aarch64", "386", "i386", "i686", "x86", "armv7", "arm", "riscv64", "ppc64le", "s390x") ||
		strings.Contains(lowerName, "x86_64") || strings.Contains(lowerName, "x86-64")
}

func doGetWithFallback(ctx context.Context, rawURL string, headers map[string]string, localProxyURL string, headerTimeout time.Duration) (*http.Response, error) {
	transports := buildTransports(rawURL, localProxyURL, headerTimeout)
	var lastErr error
	for _, tr := range transports {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		client := &http.Client{Transport: tr}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func buildTransports(rawURL, localProxyURL string, headerTimeout time.Duration) []*http.Transport {
	if isLoopbackURL(rawURL) {
		return []*http.Transport{cloneTransport(nil, headerTimeout)}
	}
	var out []*http.Transport
	if rawProxy := strings.TrimSpace(localProxyURL); rawProxy != "" {
		if parsed, err := url.Parse(rawProxy); err == nil && parsed.Host != "" {
			out = append(out, cloneTransport(http.ProxyURL(parsed), headerTimeout))
		}
	}
	sysTr := netproxy.Transport().Clone()
	sysTr.ResponseHeaderTimeout = headerTimeout
	out = append(out, sysTr)
	out = append(out, cloneTransport(nil, headerTimeout))
	return out
}

func cloneTransport(proxyFn func(*http.Request) (*url.URL, error), headerTimeout time.Duration) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = proxyFn
	tr.DialContext = (&net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = headerTimeout
	return tr
}

func isLoopbackURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
