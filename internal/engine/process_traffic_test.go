package engine

import (
	"testing"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/core"
)

func TestProcessTrafficTrackerAccumulatesAcrossClosedAndReusedConns(t *testing.T) {
	base := time.Date(2026, 11, 25, 21, 0, 0, 0, time.Local)
	tr := newProcessTrafficTracker(base)

	// 第 1 次采样：chrome 走代理、curl 走直连、一条尚未识别进程名的代理连接
	tr.Observe([]core.ConnectionInfo{
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50001, TargetAddr: "a.example", TargetPort: 443, ProcessName: "Google Chrome", ProxyType: "Proxy", ActiveTime: 100, UploadTraffic: 100, DownloadTraffic: 500, UploadTrafficTotal: 1000, DownloadTrafficTotal: 5000},
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50002, TargetAddr: "b.example", TargetPort: 443, ProcessName: "curl", ProxyType: "Direct", ActiveTime: 101, UploadTraffic: 50, DownloadTraffic: 200, UploadTrafficTotal: 500, DownloadTrafficTotal: 2000},
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50003, TargetAddr: "c.example", TargetPort: 443, ProcessName: "", ProxyType: "Proxy", ActiveTime: 102, UploadTraffic: 20, DownloadTraffic: 80, UploadTrafficTotal: 200, DownloadTrafficTotal: 800},
	}, base.Add(time.Second))

	// 第 2 次采样：
	// - chrome 50001 增长
	// - curl 50002 已关闭（不再出现在列表中，累计流量必须保留且实时速率归零）
	// - 50003 补全了真实进程名 "git"，早期流量应从「未知进程」迁移到 "git"
	tr.Observe([]core.ConnectionInfo{
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50001, TargetAddr: "a.example", TargetPort: 443, ProcessName: "Google Chrome", ProxyType: "Proxy", ActiveTime: 100, UploadTraffic: 300, DownloadTraffic: 1500, UploadTrafficTotal: 2500, DownloadTrafficTotal: 12000},
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50003, TargetAddr: "c.example", TargetPort: 443, ProcessName: "git", ProxyType: "Proxy", ActiveTime: 102, UploadTraffic: 40, DownloadTraffic: 160, UploadTrafficTotal: 600, DownloadTrafficTotal: 2000},
	}, base.Add(2*time.Second))

	snap := tr.Snapshot()
	if snap.TotalProxyUpload != 3100 || snap.TotalProxyDownload != 14000 {
		t.Fatalf("unexpected total proxy bytes: up=%d down=%d", snap.TotalProxyUpload, snap.TotalProxyDownload)
	}
	if snap.TotalDirectUpload != 500 || snap.TotalDirectDownload != 2000 {
		t.Fatalf("unexpected total direct bytes: up=%d down=%d", snap.TotalDirectUpload, snap.TotalDirectDownload)
	}
	// 未知进程已完全迁移至 git，不应残留
	if len(snap.Processes) != 3 {
		t.Fatalf("expected 3 processes (Chrome, git, curl), got %d: %+v", len(snap.Processes), snap.Processes)
	}
	if snap.Processes[0].ProcessName != "Google Chrome" || snap.Processes[0].ProxyTotalBytes() != 14500 || snap.Processes[0].ProxyActiveConns != 1 {
		t.Fatalf("rank 1 mismatch: %+v", snap.Processes[0])
	}
	if snap.Processes[1].ProcessName != "git" || snap.Processes[1].ProxyTotalBytes() != 2600 || snap.Processes[1].TotalConns != 1 {
		t.Fatalf("rank 2 mismatch (migration): %+v", snap.Processes[1])
	}
	if snap.Processes[2].ProcessName != "curl" || snap.Processes[2].DirectTotalBytes() != 2500 || snap.Processes[2].ActiveConns != 0 || snap.Processes[2].DownloadSpeedBps != 0 {
		t.Fatalf("rank 3 mismatch (closed conn): %+v", snap.Processes[2])
	}

	// 第 3 次采样：同一源端口 50001 被新连接复用（ActiveTime 变化且计数重新从 300/700 起算），应按新连接增量累加
	tr.Observe([]core.ConnectionInfo{
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50001, TargetAddr: "a2.example", TargetPort: 443, ProcessName: "Google Chrome", ProxyType: "Proxy", ActiveTime: 200, UploadTrafficTotal: 300, DownloadTrafficTotal: 700},
	}, base.Add(3*time.Second))
	snap2 := tr.Snapshot()
	if snap2.Processes[0].ProxyUploadBytes != 2800 || snap2.Processes[0].ProxyDownloadBytes != 12700 || snap2.Processes[0].TotalConns != 2 {
		t.Fatalf("reused port delta mismatch: %+v", snap2.Processes[0])
	}

	// 重置统计：活跃连接 50001 的历史基线 (300/700) 不应倒灌，只计重置后新增字节
	tr.Reset(base.Add(4 * time.Second))
	if got := tr.Snapshot(); len(got.Processes) != 0 || got.TotalBytes() != 0 {
		t.Fatalf("after reset expected empty stats, got %+v", got)
	}
	tr.Observe([]core.ConnectionInfo{
		{Protocol: "TCP", SourceAddr: "127.0.0.1", SourcePort: 50001, TargetAddr: "a2.example", TargetPort: 443, ProcessName: "Google Chrome", ProxyType: "Proxy", ActiveTime: 200, UploadTrafficTotal: 450, DownloadTrafficTotal: 1100},
	}, base.Add(5*time.Second))
	snap3 := tr.Snapshot()
	if len(snap3.Processes) != 1 || snap3.Processes[0].ProxyUploadBytes != 150 || snap3.Processes[0].ProxyDownloadBytes != 400 {
		t.Fatalf("post-reset delta mismatch: %+v", snap3.Processes)
	}
}
