package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"

	"github.com/KevinXC5/flyshadow-bar/internal/updater"
	"github.com/KevinXC5/flyshadow-bar/internal/version"
)

const (
	updateProgressThrottle = 50 * time.Millisecond
	updateIdleTimeout      = time.Minute
	updateInitialDelay     = 5 * time.Second
	updateRetryDelay       = 30 * time.Second
	updateCheckInterval    = 30 * time.Minute
)

// updateProgressNow 允许单测替换时钟。
var updateProgressNow = time.Now

// UpdateStatus 描述应用内 MyGo 签名增量更新的当前状态。
type UpdateStatus struct {
	Version          string `json:"version"`
	Enabled          bool   `json:"enabled"`
	Available        string `json:"available"`
	InstalledVersion string `json:"installedVersion"`
	Notes            string `json:"notes"`
	Installed        bool   `json:"installed"`
	CanReinstall     bool   `json:"canReinstall"`
}

// HasSupersedingUpdate 报告是否已有已就绪更新，且远端又发布了比该已就绪版本更新的版本。
func (s UpdateStatus) HasSupersedingUpdate() bool {
	return s.Installed && s.Available != "" && s.InstalledVersion != "" &&
		updater.CompareVersions(s.Available, s.InstalledVersion) > 0
}

// updateCanceledError 保留 context.Canceled 语义，同时提供中文提示。
type updateCanceledError struct{}

func (updateCanceledError) Error() string { return "更新已取消，可稍后重试" }
func (updateCanceledError) Unwrap() error { return context.Canceled }

// UpdateManager 将检查、Ed25519 签名验证与 bsdiff 增量安装委托给 mygo.Updater。
type UpdateManager struct {
	mu               sync.Mutex
	pending          *mygo.Update
	busy             bool
	installed        bool
	installedVersion string
	cancelInstall    context.CancelFunc

	cachedNotes        string
	cachedNotesPending *mygo.Update
	cachedNotesRaw     string
	cachedNotesVersion string

	// 依赖注入钩子，未设置时默认调用 mygo.Updater。
	versionFn      func() string
	enabledFn      func() bool
	canReinstallFn func() bool
	checkFn        func(context.Context) (*mygo.Update, error)
	installFn      func(context.Context, *mygo.Update, func(int64, int64)) error
	restartFn      func()

	availableFn   func(UpdateStatus)
	readyFn       func(UpdateStatus)
	installFailFn func()
	progressFn    func(downloaded, total int64)

	idleTimeout   time.Duration
	initialDelay  time.Duration
	retryDelay    time.Duration
	checkInterval time.Duration
}

func newUpdateManager(restartFn func(), localProxyFn func() string) *UpdateManager {
	if raw := strings.TrimSpace(os.Getenv("FLYSHADOW_UPDATE_URL")); raw != "" {
		if src, err := newDirUpdateSource(raw, runtime.GOOS, runtime.GOARCH, localProxyFn); err == nil {
			mygo.Updater.SetSource(src)
			log.Printf("[updater] 已启用自定义更新源: %s", raw)
		} else {
			log.Printf("[updater] 自定义更新源 FLYSHADOW_UPDATE_URL 无效，回退默认源: %v", err)
		}
	} else if repo := strings.Trim(strings.TrimSpace(version.GithubRepo), "/"); repo != "" {
		src := newGitHubFallbackUpdateSource(repo, runtime.GOOS, runtime.GOARCH, localProxyFn)
		mygo.Updater.SetSource(src)
	}
	return &UpdateManager{
		restartFn:     restartFn,
		idleTimeout:   updateIdleTimeout,
		initialDelay:  updateInitialDelay,
		retryDelay:    updateRetryDelay,
		checkInterval: updateCheckInterval,
	}
}

type updateRoute struct {
	name      string
	transport *http.Transport
}

// buildUpdateRoutes 按「本机内置代理 → 系统/环境代理 → 直连」构造多路由回退链。
// 当目标为本机回环地址（本地测试源）时仅保留直连，避免被代理劫持。
func buildUpdateRoutes(targetURL string, localProxyURL string, headerTimeout time.Duration) []updateRoute {
	if isLoopbackURL(targetURL) {
		return []updateRoute{{
			name:      "direct",
			transport: newUpdateTransport(nil, headerTimeout),
		}}
	}

	routes := make([]updateRoute, 0, 3)
	if rawProxy := strings.TrimSpace(localProxyURL); rawProxy != "" {
		if parsed, err := url.Parse(rawProxy); err == nil && parsed.Host != "" {
			routes = append(routes, updateRoute{
				name:      "local-proxy",
				transport: newUpdateTransport(http.ProxyURL(parsed), headerTimeout),
			})
		}
	}

	sysTransport := netproxy.Transport().Clone()
	sysTransport.ResponseHeaderTimeout = headerTimeout
	routes = append(routes, updateRoute{
		name:      "system-proxy",
		transport: sysTransport,
	})

	routes = append(routes, updateRoute{
		name:      "direct",
		transport: newUpdateTransport(nil, headerTimeout),
	})
	return routes
}

func newUpdateTransport(proxyFn func(*http.Request) (*url.URL, error), headerTimeout time.Duration) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = proxyFn
	base.DialContext = (&net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	base.TLSHandshakeTimeout = 10 * time.Second
	base.ResponseHeaderTimeout = headerTimeout
	return base
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

func openURLWithFallback(ctx context.Context, targetURL string, localProxyFn func() string, headerTimeout time.Duration) (io.ReadCloser, error) {
	u, err := url.Parse(targetURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("无效的更新下载地址: %q", targetURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackURL(targetURL)) {
		return nil, fmt.Errorf("更新地址仅允许 https 或本机回环 http: %q", targetURL)
	}

	var localProxy string
	if localProxyFn != nil {
		localProxy = strings.TrimSpace(localProxyFn())
	}
	routes := buildUpdateRoutes(targetURL, localProxy, headerTimeout)

	var lastErr error
	for _, rt := range routes {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			return nil, err
		}
		// 保持 ASCII User-Agent，与 mygo-updater 对齐，避免 GitHub CDN 拒绝非 ASCII 请求头
		req.Header.Set("User-Agent", "mygo-updater")

		client := &http.Client{Transport: rt.transport}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("[%s] %w", rt.name, err)
			log.Printf("[updater] 通过 %s 请求失败 (%s): %v", rt.name, targetURL, err)
			continue
		}
		if resp.StatusCode == http.StatusOK {
			log.Printf("[updater] 通过 %s 获取更新资源成功 (%s)", rt.name, targetURL)
			return resp.Body, nil
		}
		resp.Body.Close()
		lastErr = fmt.Errorf("[%s] GET %s: %s", rt.name, targetURL, resp.Status)
		log.Printf("[updater] 通过 %s 请求返回异常状态 (%s): %s", rt.name, targetURL, resp.Status)
		if resp.StatusCode == http.StatusNotFound {
			return nil, lastErr
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("无法下载更新资源: %s", targetURL)
}

// githubFallbackUpdateSource 默认指向 GitHub Releases 最新清单，并优先经由本机内置代理访问，
// 解决 Windows / macOS 未开启系统代理或环境变量代理冲突导致在线更新失败的问题。
// 所有更新包仍强制由编译进应用的 Ed25519 公钥验证签名，安全性与默认源完全一致。
type githubFallbackUpdateSource struct {
	manifestURL  string
	localProxyFn func() string
}

func newGitHubFallbackUpdateSource(repo, goos, goarch string, localProxyFn func() string) *githubFallbackUpdateSource {
	name := "update-" + goos + "-" + goarch + ".json"
	return &githubFallbackUpdateSource{
		manifestURL:  "https://github.com/" + strings.Trim(repo, "/") + "/releases/latest/download/" + name,
		localProxyFn: localProxyFn,
	}
}

func (s *githubFallbackUpdateSource) Manifest(ctx context.Context, _ bool) (io.ReadCloser, error) {
	return openURLWithFallback(ctx, s.manifestURL, s.localProxyFn, 12*time.Second)
}

func (s *githubFallbackUpdateSource) Open(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	return openURLWithFallback(ctx, rawURL, s.localProxyFn, 30*time.Second)
}

// dirUpdateSource 支持通过环境变量 FLYSHADOW_UPDATE_URL 指定自建镜像或本地回环测试更新源。
// 所有下载文件仍强制由编译进应用的 Ed25519 公钥验证签名，无法绕过签名校验。
type dirUpdateSource struct {
	baseURL      string
	target       string
	localProxyFn func() string
}

func newDirUpdateSource(rawURL, goos, goarch string, localProxyFn func() string) (*dirUpdateSource, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("无效的更新源地址: %q", rawURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackURL(rawURL)) {
		return nil, fmt.Errorf("更新源仅允许 https 或本机回环 http: %q", rawURL)
	}
	return &dirUpdateSource{
		baseURL:      strings.TrimRight(u.String(), "/"),
		target:       goos + "-" + goarch,
		localProxyFn: localProxyFn,
	}, nil
}

func (s *dirUpdateSource) Manifest(ctx context.Context, _ bool) (io.ReadCloser, error) {
	return openURLWithFallback(ctx, s.baseURL+"/update-"+s.target+".json", s.localProxyFn, 12*time.Second)
}

func (s *dirUpdateSource) Open(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	targetURL := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		// 将清单中的文件名映射到当前镜像/本地测试源目录，保留 Ed25519 签名校验。
		if idx := strings.LastIndex(u.Path, "/"); idx >= 0 && idx+1 < len(u.Path) {
			targetURL = s.baseURL + "/" + u.Path[idx+1:]
		} else {
			targetURL = s.baseURL + "/" + strings.TrimLeft(u.Path, "/")
		}
	}
	return openURLWithFallback(ctx, targetURL, s.localProxyFn, 30*time.Second)
}

func (u *UpdateManager) version() string {
	if u.versionFn != nil {
		return u.versionFn()
	}
	return mygo.App.Version()
}

func (u *UpdateManager) enabled() bool {
	if u.enabledFn != nil {
		return u.enabledFn()
	}
	return mygo.Updater.Enabled()
}

// canReinstallBeforeRestart 报告当前平台是否支持在未重启应用前再次安装覆盖已暂存更新：
// - macOS：mygo.Updater 通过 stageBundle 暂存新 .app，二次 Install 会自动替换并清理旧暂存包；
// - Windows：mygo.Updater 原地将运行中 .exe 重命名为固定 .old-<pid>，同进程二次 Install 会撞进程文件锁。
func (u *UpdateManager) canReinstallBeforeRestart() bool {
	if u.canReinstallFn != nil {
		return u.canReinstallFn()
	}
	return runtime.GOOS == "darwin"
}

func (u *UpdateManager) check(ctx context.Context) (*mygo.Update, error) {
	if u.checkFn != nil {
		return u.checkFn(ctx)
	}
	return mygo.Updater.Check(ctx)
}

func (u *UpdateManager) install(ctx context.Context, up *mygo.Update, progress func(int64, int64)) error {
	if u.installFn != nil {
		return u.installFn(ctx, up, progress)
	}
	return up.Install(ctx, progress)
}

func (u *UpdateManager) progress(downloaded, total int64) {
	if u.progressFn != nil {
		u.progressFn(downloaded, total)
	}
}

// Status 返回当前增量更新状态的线程安全快照。
func (u *UpdateManager) Status() UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	status := UpdateStatus{
		Version:          u.version(),
		Enabled:          u.enabled(),
		Installed:        u.installed,
		InstalledVersion: u.installedVersion,
		CanReinstall:     u.canReinstallBeforeRestart(),
	}
	if u.pending != nil {
		status.Available = u.pending.Version
		status.Notes = u.resolveNotesLocked(status.Version)
	} else if u.installed && u.installedVersion != "" {
		status.Available = u.installedVersion
	}
	return status
}

func (u *UpdateManager) resolveNotesLocked(currentVersion string) string {
	if u.pending == nil {
		u.cachedNotes = ""
		u.cachedNotesPending = nil
		u.cachedNotesRaw = ""
		u.cachedNotesVersion = ""
		return ""
	}
	if u.cachedNotesPending == u.pending && u.cachedNotesRaw == u.pending.Notes && u.cachedNotesVersion == currentVersion {
		return u.cachedNotes
	}
	u.cachedNotes = releaseNotesSince(u.pending.Notes, currentVersion)
	u.cachedNotesPending = u.pending
	u.cachedNotesRaw = u.pending.Notes
	u.cachedNotesVersion = currentVersion
	return u.cachedNotes
}

// Check 查询当前平台的签名更新清单。
// 即使已有已暂存就绪的版本，仍会向远端确认是否发布了更新版本，避免重启后再次提示升级。
func (u *UpdateManager) Check(ctx context.Context) (UpdateStatus, error) {
	if !u.enabled() {
		return UpdateStatus{}, mygo.ErrUpdatesDisabled
	}
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return UpdateStatus{}, errors.New("正在检查或安装更新，请稍后再试")
	}
	u.busy = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	log.Printf("[updater] 开始检查更新 (current=%s)", u.version())
	up, err := u.check(ctx)
	if err != nil {
		log.Printf("[updater] 检查更新失败: %v", err)
		return UpdateStatus{}, errors.New("检查更新失败，请检查网络连接后重试")
	}

	u.mu.Lock()
	if u.installed {
		if up != nil && (u.installedVersion == "" || updater.CompareVersions(up.Version, u.installedVersion) > 0) {
			log.Printf("[updater] 已就绪版本 %s 之外发现更新版本: %s", u.installedVersion, up.Version)
			u.pending = up
			u.resolveNotesLocked(u.version())
		} else {
			log.Printf("[updater] 已暂存版本 %s 仍为最新版本", u.installedVersion)
		}
	} else {
		if up != nil {
			log.Printf("[updater] 发现新版本: current=%s available=%s", u.version(), up.Version)
		} else {
			log.Printf("[updater] 当前已是最新版本: %s", u.version())
		}
		u.pending = up
		u.resolveNotesLocked(u.version())
	}
	u.mu.Unlock()
	return u.Status(), nil
}

// Install 优先下载当前版本对应的签名增量补丁（.delta），校验通过后替换应用；
// 若增量补丁不可用或校验失败，MyGo 会自动回退为下载完整签名归档。
// 在支持未重启覆盖的平台（macOS）上，若远端发布了高于已暂存版本的新版本，允许直接覆盖升级至最新版。
func (u *UpdateManager) Install(ctx context.Context) (UpdateStatus, error) {
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return UpdateStatus{}, errors.New("正在检查或安装更新，请稍后再试")
	}
	canSupersede := u.installed && u.canReinstallBeforeRestart() &&
		u.pending != nil && u.installedVersion != "" &&
		updater.CompareVersions(u.pending.Version, u.installedVersion) > 0
	if u.pending == nil || (u.installed && !canSupersede) {
		u.mu.Unlock()
		return UpdateStatus{}, errors.New("没有需要安装的更新")
	}
	up := u.pending
	u.busy = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	u.mu.Lock()
	u.cancelInstall = cancel
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.cancelInstall = nil
		u.mu.Unlock()
	}()
	idle := u.idleTimeout
	if idle <= 0 {
		idle = updateIdleTimeout
	}
	log.Printf("[updater] 开始下载并安装更新: target=%s", up.Version)
	if err := u.installWithTimeout(ctx, up, idle); err != nil {
		log.Printf("[updater] 安装更新失败 (target=%s): %v", up.Version, err)
		switch {
		case errors.Is(err, context.Canceled):
			return UpdateStatus{}, updateCanceledError{}
		case errors.Is(err, context.DeadlineExceeded):
			return UpdateStatus{}, errors.New("更新下载或安装超时，请检查网络连接后重试")
		default:
			return UpdateStatus{}, errors.New("安装更新失败，当前版本仍可继续使用，请稍后重试")
		}
	}
	log.Printf("[updater] 更新已安装就绪，等待重启生效: target=%s", up.Version)
	u.mu.Lock()
	u.installed = true
	u.installedVersion = up.Version
	u.mu.Unlock()
	return u.Status(), nil
}

// CancelInstall 取消当前正在进行的手动或后台自动更新下载。
func (u *UpdateManager) CancelInstall() {
	u.mu.Lock()
	cancel := u.cancelInstall
	u.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Restart 在安装完成后安全重启应用并切换至新版本。
func (u *UpdateManager) Restart() error {
	u.mu.Lock()
	installed := u.installed
	u.mu.Unlock()
	if !installed {
		return errors.New("请先完成更新安装")
	}
	if u.restartFn == nil {
		return errors.New("当前环境无法自动重启，请关闭应用后重新打开")
	}
	u.restartFn()
	return nil
}

// StartBackground 在启动 5 秒后首次检查更新，之后每 30 分钟持续巡检；
// 发现新版本后直接在后台静默完成增量下载与签名安装，就绪后通过 readyFn 提示重启。
// 若用户长时间未重启且线上又发布了更高版本，macOS 会自动静默覆盖暂存至最新版，Windows 则刷新横幅提醒重启后继续升级。
func (u *UpdateManager) StartBackground(stopCh <-chan struct{}) {
	if !u.enabled() {
		return
	}
	initDelay := u.initialDelay
	if initDelay <= 0 {
		initDelay = updateInitialDelay
	}
	retryDelay := u.retryDelay
	if retryDelay <= 0 {
		retryDelay = updateRetryDelay
	}
	interval := u.checkInterval
	if interval <= 0 {
		interval = updateCheckInterval
	}
	go func() {
		timer := time.NewTimer(initDelay)
		defer timer.Stop()
		retriedEarly := false
		for {
			select {
			case <-stopCh:
				return
			case <-timer.C:
				_, failed := u.checkAndAutoInstall(stopCh)
				if failed && !retriedEarly {
					// 刚启动时隧道可能尚在建连，30 秒后快速补检一次
					retriedEarly = true
					timer.Reset(retryDelay)
					continue
				}
				retriedEarly = true
				timer.Reset(interval)
			}
		}
	}()
}

// checkAndAutoInstall 执行一轮静默检查，若发现新版本则自动后台下载安装增量更新。
// 返回 (installed, failed)：installed 为 true 表示已有更新就绪，failed 表示本轮检查或安装出错。
func (u *UpdateManager) checkAndAutoInstall(stopCh <-chan struct{}) (bool, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	prevStatus := u.Status()
	status, err := u.Check(ctx)
	if err != nil {
		return prevStatus.Installed, true
	}

	if status.Installed {
		if status.HasSupersedingUpdate() {
			if status.CanReinstall {
				if u.availableFn != nil {
					u.availableFn(status)
				}
				installedStatus, err := u.Install(ctx)
				if err != nil || !installedStatus.Installed {
					if u.installFailFn != nil {
						u.installFailFn()
					}
					return true, true
				}
				if u.readyFn != nil {
					u.readyFn(installedStatus)
				}
				return true, false
			}
			// 不支持未重启覆盖的平台（Windows）：仅在首次发现更新版时刷新界面提示，避免重复撞文件锁
			if status.Available != prevStatus.Available && u.readyFn != nil {
				u.readyFn(status)
			}
		}
		return true, false
	}

	if status.Available == "" {
		return false, false
	}
	if u.availableFn != nil {
		u.availableFn(status)
	}

	installedStatus, err := u.Install(ctx)
	if err != nil || !installedStatus.Installed {
		if u.installFailFn != nil {
			u.installFailFn()
		}
		return false, true
	}
	if u.readyFn != nil {
		u.readyFn(installedStatus)
	}
	return true, false
}

// installWithTimeout 在下载长时间无字节进展时取消请求，避免卡死在慢连接上。
func (u *UpdateManager) installWithTimeout(ctx context.Context, up *mygo.Update, idle time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timedOut atomic.Bool
	var mu sync.Mutex
	var downloaded, total int64
	finished, complete := false, false
	lastProgress := time.Now()
	var timer *time.Timer
	timer = time.AfterFunc(idle, func() {
		mu.Lock()
		defer mu.Unlock()
		if finished || complete {
			return
		}
		if remaining := idle - time.Since(lastProgress); remaining > 0 {
			timer.Reset(remaining)
			return
		}
		timedOut.Store(true)
		cancel()
	})
	defer func() {
		mu.Lock()
		finished = true
		timer.Stop()
		mu.Unlock()
	}()
	err := u.install(ctx, up, func(n, size int64) {
		mu.Lock()
		if !finished && (n > downloaded || size != total) {
			downloaded, total = n, size
			lastProgress = time.Now()
			complete = size > 0 && n >= size
			if complete {
				timer.Stop()
			} else {
				timer.Reset(idle)
			}
		}
		mu.Unlock()
		u.progress(n, size)
	})
	if err != nil {
		if timedOut.Load() {
			return context.DeadlineExceeded
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return err
}

// newThrottledUpdateProgress 对高频下载字节回调按 50ms 节流，确保首包、阶段切换和完成立即透传。
func newThrottledUpdateProgress(emit func(downloaded, total int64)) (func(downloaded, total int64), func()) {
	var (
		mu             sync.Mutex
		hasProgress    bool
		lastEmit       time.Time
		lastDownloaded int64
		lastTotal      int64
	)
	reset := func() {
		mu.Lock()
		hasProgress = false
		lastEmit = time.Time{}
		lastDownloaded = 0
		lastTotal = 0
		mu.Unlock()
	}
	fn := func(downloaded, total int64) {
		now := updateProgressNow()
		mu.Lock()
		complete := total > 0 && downloaded >= total
		lastComplete := hasProgress && lastTotal > 0 && lastDownloaded >= lastTotal
		stateChanged := !hasProgress ||
			downloaded < lastDownloaded ||
			(downloaded > 0) != (lastDownloaded > 0) ||
			(total > 0) != (lastTotal > 0) ||
			(complete && !lastComplete)
		shouldEmit := stateChanged || now.Sub(lastEmit) >= updateProgressThrottle
		if shouldEmit {
			hasProgress = true
			lastEmit = now
			lastDownloaded = downloaded
			lastTotal = total
		}
		mu.Unlock()
		if shouldEmit {
			emit(downloaded, total)
		}
	}
	return fn, reset
}

func parseThreePartVersion(s string) (v [3]int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != len(v) {
		return v, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// releaseNotesSince 从按 "### 版本号" 分节的更新说明中筛出高于 current 的版本内容，
// 方便跨版本增量更新时完整展示中间每个版本的变更。
func releaseNotesSince(notes, current string) string {
	type section struct {
		version [3]int
		lines   []string
	}
	var sections []section
	for _, line := range strings.Split(strings.ReplaceAll(notes, "\r\n", "\n"), "\n") {
		if rest, found := strings.CutPrefix(line, "### "); found {
			if v, ok := parseThreePartVersion(rest); ok {
				sections = append(sections, section{version: v})
			}
		}
		if len(sections) == 0 {
			if strings.TrimSpace(line) != "" {
				return notes
			}
			continue
		}
		last := &sections[len(sections)-1]
		last.lines = append(last.lines, line)
	}
	if len(sections) == 0 {
		return notes
	}
	kept := sections
	if base, ok := parseThreePartVersion(current); ok {
		kept = nil
		for _, s := range sections {
			if slices.Compare(s.version[:], base[:]) > 0 {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			kept = sections[:1]
		}
	}
	if len(kept) == 1 {
		return strings.TrimSpace(strings.Join(kept[0].lines[1:], "\n"))
	}
	var b strings.Builder
	for _, s := range kept {
		b.WriteString(strings.TrimSpace(strings.Join(s.lines, "\n")))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}
