package engine

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/core"
)

// UnknownProcessName 用于归类未能反查到具体客户端进程名的本地代理连接。
const UnknownProcessName = "未知进程"

const (
	maxTrackedProcesses         = 500
	minBackgroundProcLookupGap  = 2 * time.Second
)

// ProcessTrafficStat 记录单个本地进程经由本地代理产生的累计流量与实时活跃概况。
type ProcessTrafficStat struct {
	ProcessName         string `json:"processName"`
	ProxyUploadBytes    int64  `json:"proxyUploadBytes"`
	ProxyDownloadBytes  int64  `json:"proxyDownloadBytes"`
	DirectUploadBytes   int64  `json:"directUploadBytes"`
	DirectDownloadBytes int64  `json:"directDownloadBytes"`
	UploadSpeedBps      int64  `json:"uploadSpeedBps"`
	DownloadSpeedBps    int64  `json:"downloadSpeedBps"`
	ProxyUploadSpeed    int64  `json:"proxyUploadSpeed"`
	ProxyDownloadSpeed  int64  `json:"proxyDownloadSpeed"`
	ActiveConns         int    `json:"activeConns"`
	ProxyActiveConns    int    `json:"proxyActiveConns"`
	TotalConns          int    `json:"totalConns"`
	LastActiveAt        int64  `json:"lastActiveAt"` // Unix 毫秒
}

// ProxyTotalBytes 返回该进程走代理的上下行累计总字节数。
func (s ProcessTrafficStat) ProxyTotalBytes() int64 {
	return s.ProxyUploadBytes + s.ProxyDownloadBytes
}

// DirectTotalBytes 返回该进程命中直连规则的上下行累计总字节数。
func (s ProcessTrafficStat) DirectTotalBytes() int64 {
	return s.DirectUploadBytes + s.DirectDownloadBytes
}

// TotalBytes 返回该进程经由本地代理端口产生的全部上下行累计字节数。
func (s ProcessTrafficStat) TotalBytes() int64 {
	return s.ProxyTotalBytes() + s.DirectTotalBytes()
}

// ProcessTrafficSnapshot 表示自本次启动（或上次清零）以来按进程聚合的流量统计快照。
type ProcessTrafficSnapshot struct {
	Since               int64                `json:"since"` // Unix 毫秒
	TotalProxyUpload    int64                `json:"totalProxyUpload"`
	TotalProxyDownload  int64                `json:"totalProxyDownload"`
	TotalDirectUpload   int64                `json:"totalDirectUpload"`
	TotalDirectDownload int64                `json:"totalDirectDownload"`
	Processes           []ProcessTrafficStat `json:"processes"`
}

// TotalProxyBytes 返回所有进程走代理的累计总流量。
func (s ProcessTrafficSnapshot) TotalProxyBytes() int64 {
	return s.TotalProxyUpload + s.TotalProxyDownload
}

// TotalDirectBytes 返回所有进程走直连的累计总流量。
func (s ProcessTrafficSnapshot) TotalDirectBytes() int64 {
	return s.TotalDirectUpload + s.TotalDirectDownload
}

// TotalBytes 返回全部累计总流量（代理 + 直连）。
func (s ProcessTrafficSnapshot) TotalBytes() int64 {
	return s.TotalProxyBytes() + s.TotalDirectBytes()
}

type connRouteKind uint8

const (
	connRouteProxy connRouteKind = iota
	connRouteDirect
	connRouteReject
)

func classifyConnRoute(proxyType string) connRouteKind {
	switch strings.ToLower(strings.TrimSpace(proxyType)) {
	case "direct", "0":
		return connRouteDirect
	case "reject", "1":
		return connRouteReject
	default:
		return connRouteProxy
	}
}

type trackedConn struct {
	activeTime   int64
	processName  string
	route        connRouteKind
	lastUp       int64
	lastDown     int64
	recordedUp   int64
	recordedDown int64
}

type processTrafficTracker struct {
	mu          sync.Mutex
	since       int64
	activeConns map[string]*trackedConn
	byProcess   map[string]*ProcessTrafficStat
}

func newProcessTrafficTracker(now time.Time) *processTrafficTracker {
	return &processTrafficTracker{
		since:       now.UnixMilli(),
		activeConns: make(map[string]*trackedConn),
		byProcess:   make(map[string]*ProcessTrafficStat),
	}
}

// Observe 根据当前活跃连接快照累加各进程流量增量，并刷新活跃连接数与实时速率。
func (t *processTrafficTracker) Observe(list []core.ConnectionInfo, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	nowMs := now.UnixMilli()

	// 清空上一帧的实时速率与活跃连接计数，按本帧活跃连接重算，避免已关闭连接残留僵尸速率。
	for _, st := range t.byProcess {
		st.ActiveConns = 0
		st.ProxyActiveConns = 0
		st.UploadSpeedBps = 0
		st.DownloadSpeedBps = 0
		st.ProxyUploadSpeed = 0
		st.ProxyDownloadSpeed = 0
	}

	alive := make(map[string]struct{}, len(list))
	for _, c := range list {
		key := c.CloseKey()
		if key == "" {
			continue
		}
		alive[key] = struct{}{}

		route := classifyConnRoute(c.ProxyType)
		curUp := max(int64(0), c.UploadTrafficTotal)
		curDown := max(int64(0), c.DownloadTrafficTotal)
		upSpeed := max(int64(0), c.UploadTraffic)
		downSpeed := max(int64(0), c.DownloadTraffic)
		procName := normalizeProcessName(c.ProcessName)

		prev, existed := t.activeConns[key]
		isNewConn := !existed ||
			(prev.activeTime != 0 && c.ActiveTime != 0 && c.ActiveTime != prev.activeTime) ||
			curUp < prev.lastUp ||
			curDown < prev.lastDown

		var deltaUp, deltaDown int64
		if isNewConn {
			deltaUp = curUp
			deltaDown = curDown
			tc := &trackedConn{
				activeTime:   c.ActiveTime,
				processName:  procName,
				route:        route,
				lastUp:       curUp,
				lastDown:     curDown,
				recordedUp:   deltaUp,
				recordedDown: deltaDown,
			}
			t.activeConns[key] = tc
			st := t.ensureProcessLocked(procName)
			st.TotalConns++
			addTrafficBytes(st, route, deltaUp, deltaDown)
		} else {
			// 若首次采样时未查到进程名、后续补全了真实进程名，将本连接之前暂记在「未知进程」的流量迁移过去。
			if prev.processName == UnknownProcessName && procName != UnknownProcessName {
				t.migrateUnknownConnLocked(prev, procName)
			} else if procName == UnknownProcessName && prev.processName != UnknownProcessName {
				procName = prev.processName
			}

			deltaUp = curUp - prev.lastUp
			deltaDown = curDown - prev.lastDown
			prev.lastUp = curUp
			prev.lastDown = curDown
			prev.activeTime = c.ActiveTime
			prev.route = route
			prev.recordedUp += deltaUp
			prev.recordedDown += deltaDown

			st := t.ensureProcessLocked(prev.processName)
			addTrafficBytes(st, route, deltaUp, deltaDown)
			procName = prev.processName
		}

		st := t.ensureProcessLocked(procName)
		st.ActiveConns++
		st.UploadSpeedBps += upSpeed
		st.DownloadSpeedBps += downSpeed
		if route == connRouteProxy {
			st.ProxyActiveConns++
			st.ProxyUploadSpeed += upSpeed
			st.ProxyDownloadSpeed += downSpeed
		}
		st.LastActiveAt = nowMs
	}

	// 清理已结束连接的跟踪记录（累计流量已保留在 byProcess 中）。
	for key := range t.activeConns {
		if _, ok := alive[key]; !ok {
			delete(t.activeConns, key)
		}
	}

	// 极端长期运行场景下限制总条目上限，优先保留活跃进程和高流量进程。
	if len(t.byProcess) > maxTrackedProcesses {
		t.pruneLocked()
	}
}

func (t *processTrafficTracker) migrateUnknownConnLocked(tc *trackedConn, realProc string) {
	if old := t.byProcess[UnknownProcessName]; old != nil {
		subTrafficBytes(old, tc.route, tc.recordedUp, tc.recordedDown)
		if old.TotalConns > 0 {
			old.TotalConns--
		}
		if old.TotalBytes() == 0 && old.ActiveConns == 0 && old.TotalConns == 0 {
			delete(t.byProcess, UnknownProcessName)
		}
	}
	dst := t.ensureProcessLocked(realProc)
	dst.TotalConns++
	addTrafficBytes(dst, tc.route, tc.recordedUp, tc.recordedDown)
	tc.processName = realProc
}

func (t *processTrafficTracker) ensureProcessLocked(name string) *ProcessTrafficStat {
	st, ok := t.byProcess[name]
	if !ok {
		st = &ProcessTrafficStat{ProcessName: name}
		t.byProcess[name] = st
	}
	return st
}

func (t *processTrafficTracker) pruneLocked() {
	list := make([]*ProcessTrafficStat, 0, len(t.byProcess))
	for _, st := range t.byProcess {
		list = append(list, st)
	}
	sort.Slice(list, func(i, j int) bool {
		if (list[i].ActiveConns > 0) != (list[j].ActiveConns > 0) {
			return list[i].ActiveConns > 0
		}
		if list[i].TotalBytes() != list[j].TotalBytes() {
			return list[i].TotalBytes() > list[j].TotalBytes()
		}
		return list[i].LastActiveAt > list[j].LastActiveAt
	})
	for i := maxTrackedProcesses; i < len(list); i++ {
		if list[i].ActiveConns == 0 {
			delete(t.byProcess, list[i].ProcessName)
		}
	}
}

// Reset 清零累计流量统计，但保留当前活跃连接的字节基线，避免把重置前的历史大连接重新算入。
func (t *processTrafficTracker) Reset(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.since = now.UnixMilli()
	clear(t.byProcess)
	for _, tc := range t.activeConns {
		tc.recordedUp = 0
		tc.recordedDown = 0
	}
}

// Snapshot 返回当前按进程聚合的只读流量快照（默认按代理累计流量降序排列）。
func (t *processTrafficTracker) Snapshot() ProcessTrafficSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	snap := ProcessTrafficSnapshot{
		Since:     t.since,
		Processes: make([]ProcessTrafficStat, 0, len(t.byProcess)),
	}
	for _, st := range t.byProcess {
		cp := *st
		snap.TotalProxyUpload += cp.ProxyUploadBytes
		snap.TotalProxyDownload += cp.ProxyDownloadBytes
		snap.TotalDirectUpload += cp.DirectUploadBytes
		snap.TotalDirectDownload += cp.DirectDownloadBytes
		snap.Processes = append(snap.Processes, cp)
	}
	sort.SliceStable(snap.Processes, func(i, j int) bool {
		a, b := snap.Processes[i], snap.Processes[j]
		if a.ProxyTotalBytes() != b.ProxyTotalBytes() {
			return a.ProxyTotalBytes() > b.ProxyTotalBytes()
		}
		if a.TotalBytes() != b.TotalBytes() {
			return a.TotalBytes() > b.TotalBytes()
		}
		if a.DownloadSpeedBps != b.DownloadSpeedBps {
			return a.DownloadSpeedBps > b.DownloadSpeedBps
		}
		return a.ProcessName < b.ProcessName
	})
	return snap
}

func normalizeProcessName(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return UnknownProcessName
	}
	return s
}

func addTrafficBytes(st *ProcessTrafficStat, route connRouteKind, up, down int64) {
	if up < 0 {
		up = 0
	}
	if down < 0 {
		down = 0
	}
	switch route {
	case connRouteDirect:
		st.DirectUploadBytes += up
		st.DirectDownloadBytes += down
	case connRouteProxy:
		st.ProxyUploadBytes += up
		st.ProxyDownloadBytes += down
	}
}

func subTrafficBytes(st *ProcessTrafficStat, route connRouteKind, up, down int64) {
	switch route {
	case connRouteDirect:
		st.DirectUploadBytes = max(int64(0), st.DirectUploadBytes-up)
		st.DirectDownloadBytes = max(int64(0), st.DirectDownloadBytes-down)
	case connRouteProxy:
		st.ProxyUploadBytes = max(int64(0), st.ProxyUploadBytes-up)
		st.ProxyDownloadBytes = max(int64(0), st.ProxyDownloadBytes-down)
	}
}
