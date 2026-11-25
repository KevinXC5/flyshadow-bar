package ui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

func TestReleaseNotesSince(t *testing.T) {
	raw := "### 0.3.0\n\n新增增量更新\n\n### 0.2.0\n\n新增 macOS Intel 支持\n\n### 0.1.0\n\n首个版本\n"

	got := releaseNotesSince(raw, "0.1.0")
	if !strings.Contains(got, "### 0.3.0") || !strings.Contains(got, "### 0.2.0") || strings.Contains(got, "### 0.1.0") {
		t.Fatalf("跨版本筛选异常: %q", got)
	}

	single := releaseNotesSince(raw, "0.2.0")
	if single != "新增增量更新" {
		t.Fatalf("单版本应去掉版本标题: got=%q", single)
	}

	plain := "普通更新说明"
	if releaseNotesSince(plain, "0.1.0") != plain {
		t.Fatalf("未分节说明应原样返回")
	}
}

func TestUpdateManagerCheckInstallRestart(t *testing.T) {
	restarted := false
	mgr := newUpdateManager(func() { restarted = true }, nil)
	mgr.versionFn = func() string { return "0.1.0" }
	mgr.enabledFn = func() bool { return true }
	mgr.checkFn = func(context.Context) (*mygo.Update, error) {
		return &mygo.Update{
			Version: "0.2.0",
			Notes:   "### 0.2.0\n\n支持签名增量更新\n\n### 0.1.0\n\n首版",
		}, nil
	}
	var lastDown, lastTotal int64
	mgr.progressFn = func(downloaded, total int64) {
		lastDown, lastTotal = downloaded, total
	}
	mgr.installFn = func(_ context.Context, _ *mygo.Update, progress func(int64, int64)) error {
		progress(100, 200)
		progress(200, 200)
		return nil
	}

	// 未安装前拒绝重启
	if err := mgr.Restart(); err == nil {
		t.Fatalf("未安装更新前不应允许重启")
	}

	st, err := mgr.Check(context.Background())
	if err != nil {
		t.Fatalf("Check 失败: %v", err)
	}
	if !st.Enabled || st.Available != "0.2.0" || st.Notes != "支持签名增量更新" || st.Installed {
		t.Fatalf("Check 状态异常: %+v", st)
	}

	st, err = mgr.Install(context.Background())
	if err != nil {
		t.Fatalf("Install 失败: %v", err)
	}
	if !st.Installed || lastDown != 200 || lastTotal != 200 {
		t.Fatalf("Install 状态或进度异常: st=%+v down=%d total=%d", st, lastDown, lastTotal)
	}

	if err := mgr.Restart(); err != nil || !restarted {
		t.Fatalf("Restart 失败: err=%v restarted=%v", err, restarted)
	}
}

func TestUpdateManagerDisabledAndCancel(t *testing.T) {
	mgr := newUpdateManager(nil, nil)
	mgr.enabledFn = func() bool { return false }
	if _, err := mgr.Check(context.Background()); !errors.Is(err, mygo.ErrUpdatesDisabled) {
		t.Fatalf("禁用时应返回 ErrUpdatesDisabled, got=%v", err)
	}

	mgr.enabledFn = func() bool { return true }
	mgr.versionFn = func() string { return "0.1.0" }
	mgr.checkFn = func(context.Context) (*mygo.Update, error) {
		return &mygo.Update{Version: "0.2.0"}, nil
	}
	mgr.installFn = func(ctx context.Context, _ *mygo.Update, _ func(int64, int64)) error {
		<-ctx.Done()
		return ctx.Err()
	}

	if _, err := mgr.Check(context.Background()); err != nil {
		t.Fatalf("Check 失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mgr.Install(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消安装应返回 context.Canceled, got=%v", err)
	}

	// 空闲超时测试
	mgr.idleTimeout = 40 * time.Millisecond
	if _, err := mgr.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("空闲超时应返回超时提示, got=%v", err)
	}
}

func TestDirUpdateSource(t *testing.T) {
	if _, err := newDirUpdateSource("http://example.com/updates", "darwin", "arm64", nil); err == nil {
		t.Fatalf("非本机回环的明文 http 源应被拒绝")
	}

	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		_, _ = w.Write([]byte("ok:" + r.URL.Path))
	}))
	defer srv.Close()

	src, err := newDirUpdateSource(srv.URL, "darwin", "arm64", nil)
	if err != nil {
		t.Fatalf("newDirUpdateSource 失败: %v", err)
	}
	rc, err := src.Manifest(context.Background(), false)
	if err != nil {
		t.Fatalf("Manifest 失败: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "ok:/update-darwin-arm64.json" {
		t.Fatalf("Manifest 响应异常: %q", string(b))
	}

	rc2, err := src.Open(context.Background(), "https://github.com/KevinXC5/flyshadow-bar/releases/download/v0.2.1/flyshadow-bar-0.2.0-to-0.2.1-darwin-arm64.delta")
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	b2, _ := io.ReadAll(rc2)
	rc2.Close()
	if string(b2) != "ok:/flyshadow-bar-0.2.0-to-0.2.1-darwin-arm64.delta" {
		t.Fatalf("Open 文件名映射异常: %q", string(b2))
	}
}

func TestBuildUpdateRoutesPriorityAndLoopback(t *testing.T) {
	// 公网 HTTPS 请求在内置代理可用时，必须按 local-proxy -> system-proxy -> direct 顺序回退
	routes := buildUpdateRoutes("https://github.com/KevinXC5/flyshadow-bar/releases/latest/download/update-windows-amd64.json", "http://127.0.0.1:7890", 10*time.Second)
	if len(routes) != 3 || routes[0].name != "local-proxy" || routes[1].name != "system-proxy" || routes[2].name != "direct" {
		t.Fatalf("unexpected routes order: %+v", routes)
	}

	// 内置代理未连接时仅保留 system-proxy -> direct
	routesNoLocal := buildUpdateRoutes("https://github.com/KevinXC5/flyshadow-bar/releases/latest/download/update-windows-amd64.json", "", 10*time.Second)
	if len(routesNoLocal) != 2 || routesNoLocal[0].name != "system-proxy" || routesNoLocal[1].name != "direct" {
		t.Fatalf("unexpected routes without local proxy: %+v", routesNoLocal)
	}

	// 回环地址仅直连
	loopRoutes := buildUpdateRoutes("http://127.0.0.1:18080/update-darwin-arm64.json", "http://127.0.0.1:7890", 10*time.Second)
	if len(loopRoutes) != 1 || loopRoutes[0].name != "direct" {
		t.Fatalf("loopback URL should only use direct route: %+v", loopRoutes)
	}
}

func TestUpdateManagerAutoDownloadReady(t *testing.T) {
	mgr := newUpdateManager(nil, nil)
	mgr.versionFn = func() string { return "0.2.0" }
	mgr.enabledFn = func() bool { return true }
	mgr.initialDelay = 10 * time.Millisecond
	mgr.retryDelay = 15 * time.Millisecond
	mgr.checkInterval = 20 * time.Millisecond
	mgr.checkFn = func(context.Context) (*mygo.Update, error) {
		return &mygo.Update{Version: "0.2.2", Notes: "### 0.2.2\n\n自动静默增量更新"}, nil
	}
	mgr.installFn = func(_ context.Context, _ *mygo.Update, progress func(int64, int64)) error {
		progress(86000, 86000)
		return nil
	}

	readyCh := make(chan UpdateStatus, 1)
	mgr.readyFn = func(st UpdateStatus) {
		readyCh <- st
	}

	stopCh := make(chan struct{})
	defer close(stopCh)
	mgr.StartBackground(stopCh)

	select {
	case st := <-readyCh:
		if !st.Installed || st.Available != "0.2.2" {
			t.Fatalf("自动增量安装状态异常: %+v", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("等待后台自动增量更新就绪超时")
	}
}

func TestUpdateManagerEarlyRetryAfterInitialFailure(t *testing.T) {
	mgr := newUpdateManager(nil, nil)
	mgr.versionFn = func() string { return "0.2.9" }
	mgr.enabledFn = func() bool { return true }
	mgr.initialDelay = 10 * time.Millisecond
	mgr.retryDelay = 20 * time.Millisecond
	mgr.checkInterval = 5 * time.Second

	attempts := 0
	mgr.checkFn = func(context.Context) (*mygo.Update, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("tunnel still connecting")
		}
		return &mygo.Update{Version: "0.2.10"}, nil
	}
	mgr.installFn = func(_ context.Context, _ *mygo.Update, _ func(int64, int64)) error {
		return nil
	}

	readyCh := make(chan UpdateStatus, 1)
	mgr.readyFn = func(st UpdateStatus) {
		readyCh <- st
	}

	stopCh := make(chan struct{})
	defer close(stopCh)
	mgr.StartBackground(stopCh)

	select {
	case st := <-readyCh:
		if !st.Installed || attempts < 2 {
			t.Fatalf("期望首轮失败后快速重试成功: st=%+v attempts=%d", st, attempts)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("首轮失败后未按 retryDelay 触发快速重试")
	}
}
