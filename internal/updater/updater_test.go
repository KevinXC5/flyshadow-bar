package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{a: "v0.2.0", b: "0.1.0-dev", want: 1},
		{a: "0.1.0", b: "0.1.0-dev", want: 1},
		{a: "0.1.0-dev", b: "0.1.0", want: -1},
		{a: "v1.2.3", b: "1.2.3", want: 0},
		{a: "1.2", b: "1.2.0", want: 0},
		{a: "v0.1.0-rc.2", b: "0.1.0-rc.10", want: -1},
		{a: "0.1.0-rc.1", b: "0.1.0-beta.9", want: 1},
		{a: "0.1.0-rc.1", b: "0.1.0-rc", want: 1},
		{a: "1.0.0+build.1", b: "v1.0.0+build.2", want: 0},
		{a: "v2.0.0", b: "v1.9.9", want: 1},
	}

	for _, tc := range cases {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSelectAsset(t *testing.T) {
	assets := []githubAsset{
		{Name: "flyshadow-bar-0.2.0-arm64.dmg", BrowserDownloadURL: "https://example.com/darwin-arm64.dmg"},
		{Name: "flyshadow-bar-0.2.0-amd64.dmg", BrowserDownloadURL: "https://example.com/darwin-amd64.dmg"},
		{Name: "flyshadow-bar-0.2.0-darwin-arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin-arm64.tar.gz"},
		{Name: "flyshadow-bar-0.1.0-to-0.2.0-darwin-arm64.delta", BrowserDownloadURL: "https://example.com/darwin-arm64.delta"},
		{Name: "update-darwin-arm64.json", BrowserDownloadURL: "https://example.com/update-darwin-arm64.json"},
		{Name: "flyshadow-bar-0.2.0-darwin-arm64.zip", BrowserDownloadURL: "https://example.com/darwin-arm64.zip"},
		{Name: "flyshadow-bar-0.2.0-darwin-arm64.zip.sha256", BrowserDownloadURL: "https://example.com/darwin-arm64.zip.sha256"},
		{Name: "flyshadow-bar-0.2.0-darwin-universal.zip", BrowserDownloadURL: "https://example.com/darwin-universal.zip"},
		{Name: "flyshadow-bar-setup-0.2.0-amd64.exe", BrowserDownloadURL: "https://example.com/setup-amd64.exe"},
		{Name: "flyshadow-bar-0.2.0-windows-amd64.exe", BrowserDownloadURL: "https://example.com/windows-amd64.exe"},
		{Name: "flyshadow-bar-0.2.0-windows-amd64.zip", BrowserDownloadURL: "https://example.com/windows-amd64.zip"},
		{Name: "flyshadow-bar-0.2.0-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux-amd64.tar.gz"},
	}

	cases := []struct {
		goos   string
		goarch string
		want   string
	}{
		{goos: "darwin", goarch: "arm64", want: "flyshadow-bar-0.2.0-arm64.dmg"},
		{goos: "darwin", goarch: "amd64", want: "flyshadow-bar-0.2.0-amd64.dmg"},
		{goos: "windows", goarch: "amd64", want: "flyshadow-bar-setup-0.2.0-amd64.exe"},
		{goos: "linux", goarch: "amd64", want: "flyshadow-bar-0.2.0-linux-amd64.tar.gz"},
	}

	for _, tc := range cases {
		got, ok := selectAsset(assets, tc.goos, tc.goarch)
		if !ok {
			t.Fatalf("selectAsset(%s/%s) 未找到匹配资产", tc.goos, tc.goarch)
		}
		if got.Name != tc.want {
			t.Errorf("selectAsset(%s/%s) = %q, want %q", tc.goos, tc.goarch, got.Name, tc.want)
		}
	}

	// 确保 darwin 附件及增量/清单文件不会被误判为 Windows 资产。
	onlyDarwin := []githubAsset{
		{Name: "flyshadow-bar-0.2.0-amd64.dmg", BrowserDownloadURL: "https://example.com/darwin-amd64.dmg"},
		{Name: "flyshadow-bar-0.1.0-to-0.2.0-windows-amd64.delta", BrowserDownloadURL: "https://example.com/win.delta"},
		{Name: "update-windows-amd64.json", BrowserDownloadURL: "https://example.com/update-windows-amd64.json"},
	}
	if _, ok := selectAsset(onlyDarwin, "windows", "amd64"); ok {
		t.Fatalf("darwin/delta/manifest 附件不应匹配 windows 安装包")
	}
}

func TestCheckUpdateAvailable(t *testing.T) {
	expectedZip := "flyshadow-bar-v0.2.0-" + runtime.GOOS + "-" + runtime.GOARCH + ".zip"
	expectedTar := "flyshadow-bar-v0.2.0-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"

	var gotPath, gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(githubRelease{
			TagName:     "v0.2.0",
			HTMLURL:     "https://github.com/KevinXC5/flyshadow-bar/releases/tag/v0.2.0",
			Body:        "修复若干问题并优化体验",
			PublishedAt: "2026-11-24T08:00:00Z",
			Assets: []githubAsset{
				{Name: expectedTar, BrowserDownloadURL: "https://example.com/" + expectedTar},
				{Name: expectedZip, BrowserDownloadURL: "https://example.com/" + expectedZip},
				{Name: expectedZip + ".sha256", BrowserDownloadURL: "https://example.com/" + expectedZip + ".sha256"},
			},
		})
	}))
	defer srv.Close()

	origBase := APIBaseURL
	APIBaseURL = srv.URL
	defer func() { APIBaseURL = origBase }()

	res, err := Check(context.Background(), "", "0.1.0-dev")
	if err != nil {
		t.Fatalf("Check 返回意外错误: %v", err)
	}
	if gotPath != "/repos/KevinXC5/flyshadow-bar/releases/latest" {
		t.Fatalf("请求路径不符: got=%q", gotPath)
	}
	if gotUA != "flyshadow-bar" || gotAccept != "application/vnd.github+json" {
		t.Fatalf("请求头不符: User-Agent=%q, Accept=%q", gotUA, gotAccept)
	}
	if !res.HasUpdate {
		t.Fatalf("期望检测到更新: %+v", res)
	}
	if res.LatestVersion != "v0.2.0" || res.CurrentVersion != "0.1.0-dev" {
		t.Fatalf("版本信息不符: %+v", res)
	}
	if res.AssetName != expectedZip || res.AssetURL != "https://example.com/"+expectedZip {
		t.Fatalf("期望优先选中 .zip 附件: gotName=%q, gotURL=%q", res.AssetName, res.AssetURL)
	}
	if res.ReleaseNotes != "修复若干问题并优化体验" || res.PublishedAt != "2026-11-24T08:00:00Z" {
		t.Fatalf("发布说明或时间不符: %+v", res)
	}
}

func TestCheckNotFoundReturnsNoUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	origBase := APIBaseURL
	APIBaseURL = srv.URL
	defer func() { APIBaseURL = origBase }()

	res, err := Check(context.Background(), "owner/repo", "0.1.0-dev")
	if err != nil {
		t.Fatalf("404 应平滑返回无更新而非错误: %v", err)
	}
	if res.HasUpdate {
		t.Fatalf("404 时 HasUpdate 应为 false")
	}
	if res.ReleaseURL != "https://github.com/owner/repo/releases" {
		t.Fatalf("404 时 ReleaseURL 不符: got=%q", res.ReleaseURL)
	}
}

func TestDownloadAsset(t *testing.T) {
	payload := bytes.Repeat([]byte("flyshadow-update-payload-"), 2048)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "flyshadow-bar" {
			t.Errorf("下载请求缺少预期 User-Agent: %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "nested", "flyshadow-bar.zip")
	var lastDownloaded, lastTotal int64
	calls := 0

	err := DownloadAsset(context.Background(), srv.URL+"/asset.zip", dest, func(downloaded, total int64) {
		calls++
		lastDownloaded = downloaded
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("DownloadAsset 失败: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("读取下载文件失败: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("下载内容不一致: got %d bytes, want %d bytes", len(got), len(payload))
	}
	if calls == 0 || lastDownloaded != int64(len(payload)) || lastTotal != int64(len(payload)) {
		t.Fatalf("下载进度回调异常: calls=%d, downloaded=%d, total=%d", calls, lastDownloaded, lastTotal)
	}
}
