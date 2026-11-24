package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
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
)

const (
	updateProgressThrottle = 50 * time.Millisecond
	updateIdleTimeout      = time.Minute
)

// updateProgressNow 允许单测替换时钟。
var updateProgressNow = time.Now

// UpdateStatus 描述应用内 MyGo 签名增量更新的当前状态。
type UpdateStatus struct {
	Version   string `json:"version"`
	Enabled   bool   `json:"enabled"`
	Available string `json:"available"`
	Notes     string `json:"notes"`
	Installed bool   `json:"installed"`
}

// updateCanceledError 保留 context.Canceled 语义，同时提供中文提示。
type updateCanceledError struct{}

func (updateCanceledError) Error() string { return "更新已取消，可稍后重试" }
func (updateCanceledError) Unwrap() error { return context.Canceled }

// UpdateManager 将检查、Ed25519 签名验证与 bsdiff 增量安装委托给 mygo.Updater。
type UpdateManager struct {
	mu        sync.Mutex
	pending   *mygo.Update
	busy      bool
	installed bool

	cachedNotes        string
	cachedNotesPending *mygo.Update
	cachedNotesRaw     string
	cachedNotesVersion string

	// 依赖注入钩子，未设置时默认调用 mygo.Updater。
	versionFn func() string
	enabledFn func() bool
	checkFn   func(context.Context) (*mygo.Update, error)
	installFn func(context.Context, *mygo.Update, func(int64, int64)) error
	restartFn func()

	availableFn func(UpdateStatus)
	progressFn  func(downloaded, total int64)

	idleTimeout time.Duration
}

func newUpdateManager(restartFn func()) *UpdateManager {
	if raw := strings.TrimSpace(os.Getenv("FLYSHADOW_UPDATE_URL")); raw != "" {
		if src, err := newDirUpdateSource(raw, runtime.GOOS, runtime.GOARCH); err == nil {
			mygo.Updater.SetSource(src)
		}
	}
	return &UpdateManager{
		restartFn:   restartFn,
		idleTimeout: updateIdleTimeout,
	}
}

// dirUpdateSource 支持通过环境变量 FLYSHADOW_UPDATE_URL 指定自建镜像或本地回环测试更新源。
// 所有下载文件仍强制由编译进应用的 Ed25519 公钥验证签名，无法绕过签名校验。
type dirUpdateSource struct {
	baseURL string
	target  string
	client  *http.Client
}

func newDirUpdateSource(rawURL, goos, goarch string) (*dirUpdateSource, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("无效的更新源地址: %q", rawURL)
	}
	host := u.Hostname()
	isLoopback := strings.EqualFold(host, "localhost")
	if !isLoopback {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			isLoopback = true
		}
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback) {
		return nil, fmt.Errorf("更新源仅允许 https 或本机回环 http: %q", rawURL)
	}
	return &dirUpdateSource{
		baseURL: strings.TrimRight(u.String(), "/"),
		target:  goos + "-" + goarch,
		client:  &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

func (s *dirUpdateSource) Manifest(ctx context.Context, _ bool) (io.ReadCloser, error) {
	return s.Open(ctx, s.baseURL+"/update-"+s.target+".json")
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "fsbar-updater")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", targetURL, resp.Status)
	}
	return resp.Body, nil
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
		Version:   u.version(),
		Enabled:   u.enabled(),
		Installed: u.installed,
	}
	if u.pending != nil {
		status.Available = u.pending.Version
		status.Notes = u.resolveNotesLocked(status.Version)
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
func (u *UpdateManager) Check(ctx context.Context) (UpdateStatus, error) {
	if !u.enabled() {
		return UpdateStatus{}, mygo.ErrUpdatesDisabled
	}
	u.mu.Lock()
	if u.installed {
		u.mu.Unlock()
		return u.Status(), nil
	}
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

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	up, err := u.check(ctx)
	if err != nil {
		return UpdateStatus{}, errors.New("检查更新失败，请检查网络连接后重试")
	}
	u.mu.Lock()
	u.pending = up
	u.resolveNotesLocked(u.version())
	u.mu.Unlock()
	return u.Status(), nil
}

// Install 优先下载当前版本对应的签名增量补丁（.delta），校验通过后替换应用；
// 若增量补丁不可用或校验失败，MyGo 会自动回退为下载完整签名归档。
func (u *UpdateManager) Install(ctx context.Context) (UpdateStatus, error) {
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return UpdateStatus{}, errors.New("正在检查或安装更新，请稍后再试")
	}
	if u.installed || u.pending == nil {
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
	idle := u.idleTimeout
	if idle <= 0 {
		idle = updateIdleTimeout
	}
	if err := u.installWithTimeout(ctx, up, idle); err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return UpdateStatus{}, updateCanceledError{}
		case errors.Is(err, context.DeadlineExceeded):
			return UpdateStatus{}, errors.New("更新下载或安装超时，请检查网络连接后重试")
		default:
			return UpdateStatus{}, errors.New("安装更新失败，当前版本仍可继续使用，请稍后重试")
		}
	}
	u.mu.Lock()
	u.installed = true
	u.mu.Unlock()
	return u.Status(), nil
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

// StartBackground 在应用就绪后周期性静默检查更新（退出时随 stopCh 停止）。
func (u *UpdateManager) StartBackground(stopCh <-chan struct{}) {
	if !u.enabled() {
		return
	}
	go func() {
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-timer.C:
				status, err := u.Check(context.Background())
				if err == nil && status.Available != "" && !status.Installed && u.availableFn != nil {
					u.availableFn(status)
				}
				timer.Reset(24 * time.Hour)
			}
		}
	}()
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
