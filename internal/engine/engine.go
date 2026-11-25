package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/cloud"
	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/sysproxy"
	"github.com/KevinXC5/flyshadow-bar/internal/version"
)

// TunnelState 表示隧道的高层连接状态。
type TunnelState string

const (
	StateDisconnected TunnelState = "disconnected"
	StateConnecting   TunnelState = "connecting"
	StateConnected    TunnelState = "connected"
	StateError        TunnelState = "error"
)

// NodeSnapshot 是对外暴露的脱敏节点快照（绝对不包含 Password）。
type NodeSnapshot struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	CNName      string   `json:"cnName"`
	DisplayName string   `json:"displayName"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	TrafficRate float64  `json:"trafficRate,omitempty"`
	EnableQuic  bool     `json:"enableQuic"`
	NativeUDP   bool     `json:"nativeUdp"`
	Starred     bool     `json:"starred"`
	Selected    bool     `json:"selected"`
	DelayMs     int64    `json:"delayMs"` // 0=未测速, -1=超时/失败, >0=延迟毫秒
	Testing     bool     `json:"testing"`
}

// SpeedSample 记录单次 1 秒周期的上下行速率采样（字节/秒），用于主面板实时折线图。
type SpeedSample struct {
	UploadBps   int64 `json:"uploadBps"`
	DownloadBps int64 `json:"downloadBps"`
}

const (
	maxSpeedHistoryPoints           = 48
	defaultSysProxyCheckInterval    = 3 * time.Second
	defaultConnectPollInterval      = 200 * time.Millisecond
	defaultConnectPollAttempts      = 40 // 40 * 200ms = 8s，与官方 ProxyService 一致
	defaultDisconnectErrorThreshold = 8  // 已连上后连续 8 秒底层状态非 0 则转为连接异常提示
)

// Snapshot 是整个客户端运行状态的只读快照，用于 UI、托盘与本地控制 API。
type Snapshot struct {
	AppVersion             string                  `json:"appVersion"`
	CoreVersion            string                  `json:"coreVersion"`
	CoreLibPath            string                  `json:"coreLibPath"`
	ConfigPath             string                  `json:"configPath"`
	GeoIPLoaded            bool                    `json:"geoipLoaded"`
	GeoIPPath              string                  `json:"geoipPath,omitempty"`
	RuleCount              int                     `json:"ruleCount"`
	State                  TunnelState             `json:"state"`
	LastError              string                  `json:"lastError,omitempty"`
	CurrentNode            *NodeSnapshot           `json:"currentNode,omitempty"`
	Mode                   string                  `json:"mode"`
	ModeCode               core.ProxyMode          `json:"modeCode"`
	ProxyPort              int                     `json:"proxyPort"`
	AllowLAN               bool                    `json:"allowLan"`
	ProxyAuthEnabled       bool                    `json:"proxyAuthEnabled"`
	SystemProxy            bool                    `json:"systemProxy"`
	UDPAccelerationEnabled bool                    `json:"udpAccelerationEnabled"`
	NativeUDP              bool                    `json:"nativeUdp"`
	EffectiveTransport     core.TransportDecision  `json:"effectiveTransport"`
	IPv6Enable             bool                    `json:"ipv6Enable"`
	UseBuildInDNS          bool                    `json:"useBuildInDns"`
	DirectConnPriority     bool                    `json:"directConnPriority"`
	AutoSetSysProxy        bool                    `json:"autoSetSysProxy"`
	HasCloudAuth           bool                    `json:"hasCloudAuth"`
	Syncing                bool                    `json:"syncing"`
	LastUpdateTime         int64                   `json:"lastUpdateTime,omitempty"`
	Traffic                *config.UserTrafficInfo `json:"traffic,omitempty"`
	CustomRules            []config.DomainRule     `json:"customRules"`
	RuleLabels             []string                `json:"ruleLabels"`
	ActiveRuleLabels       []string                `json:"activeRuleLabels"`
	UploadBytes            int64                   `json:"uploadBytes"`
	DownloadBytes          int64                   `json:"downloadBytes"`
	UploadSpeedBps         int64                   `json:"uploadSpeedBps"`
	DownloadSpeedBps       int64                   `json:"downloadSpeedBps"`
	SpeedHistory           []SpeedSample           `json:"speedHistory,omitempty"`
	TunnelPingMs           int32                   `json:"tunnelPingMs"`
	Pinging                bool                    `json:"pinging"`
	PingCompleted          int                     `json:"pingCompleted"`
	PingTotal              int                     `json:"pingTotal"`
	Nodes                  []NodeSnapshot          `json:"nodes"`
	UpdatedAt              int64                   `json:"updatedAt"`
}

// Options 允许在启动或测试时注入依赖。
type Options struct {
	ConfigPath          string
	CoreLibPath         string
	CoreClient          core.Client
	SysProxy            sysproxy.Manager
	CloudClient         cloud.SyncAPI
	AutoConnect         bool
	DisableAutoSysProxy bool
}

// Engine 统一管理配置、底层核心库生命周期、代理、隧道、系统代理与实时统计。
type Engine struct {
	mu                  sync.RWMutex
	opMu                sync.Mutex // 串行化状态变更操作（切节点、启停、重载）
	core                core.Client
	sysProxy            sysproxy.Manager
	cloud               cloud.SyncAPI
	cfg                 *config.Config
	disableAutoSysProxy bool

	geoIPLoaded bool
	geoIPPath   string
	ruleCount   int

	state              TunnelState
	lastError          string
	tunnelDesired      bool // 用户/启动流程期望隧道保持在线，用于断线后的状态追踪与自动恢复
	tunnelOpInProgress bool // 正在执行建连/切换/断开操作时暂停后台 sampleStats 写状态，避免竞态
	disconnectStreak   int  // 连续采样到 TunnelStatus != 0 的次数
	systemProxyOn      bool
	proxyStarted       bool
	syncing            bool

	delays        map[int]int64
	testingNodes  map[int]bool
	pinging       bool
	pingCompleted int
	pingTotal     int

	lastUploadBytes   int64
	lastDownloadBytes int64
	lastSampleTime    time.Time
	uploadSpeedBps    int64
	downloadSpeedBps  int64
	speedHistory      []SpeedSample
	tunnelPingMs      int32

	listeners      map[int]func(Snapshot)
	nextSubID      int
	connProcCache  map[string]string
	lastProcLookup time.Time
	procLookupFn   func(proxyPort int) map[string]string
	selfProcNames  []string
	procTraffic    *processTrafficTracker

	sysProxyCheckInterval    time.Duration
	connectPollInterval      time.Duration
	connectPollAttempts      int
	disconnectErrorThreshold int
	loopsOnce                sync.Once

	stopCh   chan struct{}
	stopOnce sync.Once
}

// New 创建并初始化 Engine（加载配置与原生核心库，但不自动打开系统代理）。
func New(opts Options) (*Engine, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}

	cli := opts.CoreClient
	if cli == nil {
		cli, err = core.Open(opts.CoreLibPath)
		if err != nil {
			return nil, err
		}
	}
	if err := cli.Init(); err != nil {
		return nil, fmt.Errorf("初始化核心库运行时失败: %w", err)
	}

	sp := opts.SysProxy
	if sp == nil {
		sp = sysproxy.New()
	}
	cc := opts.CloudClient
	if cc == nil {
		cc = cloud.NewClient()
	}

	e := &Engine{
		core:                cli,
		sysProxy:            sp,
		cloud:               cc,
		cfg:                 cfg,
		disableAutoSysProxy: opts.DisableAutoSysProxy,
		state:               StateDisconnected,
		delays:              make(map[int]int64),
		testingNodes:        make(map[int]bool),
		speedHistory:        make([]SpeedSample, maxSpeedHistoryPoints),
		listeners:           make(map[int]func(Snapshot)),
		connProcCache:         make(map[string]string),
		procLookupFn:          lookupLoopbackClients,
		selfProcNames:         currentSelfProcessNames(),
		procTraffic:           newProcessTrafficTracker(time.Now()),
		sysProxyCheckInterval:    defaultSysProxyCheckInterval,
		connectPollInterval:      defaultConnectPollInterval,
		connectPollAttempts:      defaultConnectPollAttempts,
		disconnectErrorThreshold: defaultDisconnectErrorThreshold,
		stopCh:                   make(chan struct{}),
		tunnelPingMs:             -1,
	}

	// 加载 GeoIP（仅真实打开的核心库且非 mock 或文件存在时加载）
	if geoPath, geoData, err := core.LocateGeoIP(); err == nil && len(geoData) > 0 {
		cli.SetGeoIP("CN", geoData)
		e.geoIPLoaded = true
		e.geoIPPath = geoPath
	} else if err != nil {
		log.Printf("[engine] 未加载 GeoIP 数据库: %v", err)
	}

	e.applyConfigToCoreLocked()

	// 检测当前系统代理是否已经指向本地端口
	if enabled, err := sp.IsEnabled("127.0.0.1", cfg.ProxyPort); err == nil {
		e.systemProxyOn = enabled
	}

	log.Printf("[engine] 初始化完成: config=%s core=%s (%s) geoip=%v rules=%d nodes=%d port=%d mode=%s sysproxy=%v",
		cfg.Path, cli.Version(), cli.LibPath(), e.geoIPLoaded, e.ruleCount, len(cfg.Nodes()), cfg.ProxyPort, core.ProxyMode(cfg.ProxyType).String(), e.systemProxyOn)

	return e, nil
}

// Start 启动本地代理、后台流量采样循环，并按需自动连接当前节点。
func (e *Engine) Start(autoConnect bool) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	if err := e.startLocalProxyLocked(); err != nil {
		e.mu.Lock()
		e.state = StateError
		e.lastError = err.Error()
		e.mu.Unlock()
		e.emit()
		return err
	}

	e.loopsOnce.Do(func() {
		go e.statsLoop()
		go e.sysProxyWatchLoop()
	})

	if autoConnect {
		if err := e.connectCurrentLocked(); err != nil {
			// 连接失败不阻断应用启动，保留在状态中供 UI/CLI 查看
			return nil
		}
		// 若官方配置开启了启动时自动设置系统代理且未显式禁止，则在连上后自动启用
		e.mu.RLock()
		autoSys := e.cfg.AutoSetSysProxyOnStartUp && !e.disableAutoSysProxy
		port := e.cfg.ProxyPort
		alreadyOn := e.systemProxyOn
		e.mu.RUnlock()
		if autoSys && !alreadyOn {
			if err := e.sysProxy.Enable("127.0.0.1", port); err == nil {
				e.mu.Lock()
				e.systemProxyOn = true
				e.mu.Unlock()
				log.Printf("[engine] 启动建连后已自动开启系统代理: 127.0.0.1:%d", port)
				e.emit()
			} else {
				log.Printf("[engine] 启动自动开启系统代理失败 (127.0.0.1:%d): %v", port, err)
			}
		}
		// 首次建连后若配置包含订阅凭据且距上次同步超过 5 分钟，后台静默刷新流量、到期时间、节点与云端规则
		e.mu.RLock()
		shouldSync := !e.disableAutoSysProxy && e.cfg.SubscribePassword != "" &&
			(e.cfg.LastUpdateTime == 0 || time.Since(time.UnixMilli(int64(e.cfg.LastUpdateTime))) > 5*time.Minute)
		e.mu.RUnlock()
		if shouldSync {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				_, _ = e.SyncSubscription(ctx)
			}()
		}
	}
	return nil
}

// Shutdown 退出时安全关闭系统代理、断开隧道、停止本地代理并释放核心资源。
func (e *Engine) Shutdown() {
	e.stopOnce.Do(func() {
		close(e.stopCh)
	})

	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.tunnelOpInProgress = true
	e.tunnelDesired = false
	e.disconnectStreak = 0
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.tunnelOpInProgress = false
		e.mu.Unlock()
	}()

	// 退出时自动关闭系统代理，避免系统断网
	if e.sysProxy != nil {
		e.mu.RLock()
		wasOn := e.systemProxyOn
		e.mu.RUnlock()
		if wasOn {
			if err := e.sysProxy.Disable(); err != nil {
				log.Printf("[engine] 退出关闭系统代理失败: %v", err)
			} else {
				log.Printf("[engine] 退出已自动关闭系统代理")
			}
			e.mu.Lock()
			e.systemProxyOn = false
			e.mu.Unlock()
		}
	}

	if e.core != nil {
		e.core.Close()
	}
	log.Printf("[engine] 运行时引擎已停止")

	e.mu.Lock()
	e.state = StateDisconnected
	e.proxyStarted = false
	e.mu.Unlock()
}

// Subscribe 注册状态变更订阅回调，返回取消订阅函数。
func (e *Engine) Subscribe(fn func(Snapshot)) func() {
	e.mu.Lock()
	id := e.nextSubID
	e.nextSubID++
	e.listeners[id] = fn
	e.mu.Unlock()

	return func() {
		e.mu.Lock()
		delete(e.listeners, id)
		e.mu.Unlock()
	}
}

func (e *Engine) emit() {
	snap := e.Snapshot()
	e.mu.RLock()
	fns := make([]func(Snapshot), 0, len(e.listeners))
	for _, fn := range e.listeners {
		fns = append(fns, fn)
	}
	e.mu.RUnlock()

	for _, fn := range fns {
		fn(snap)
	}
}

// Snapshot 返回当前完整运行状态的线程安全脱敏副本。
func (e *Engine) Snapshot() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	cfg := e.cfg
	cur := cfg.CurrentNode()
	curID := -1
	if cur != nil {
		curID = cur.ID
	}

	nodes := cfg.Nodes()
	nodeSnaps := make([]NodeSnapshot, 0, len(nodes))
	var curSnap *NodeSnapshot

	for _, n := range nodes {
		ns := NodeSnapshot{
			ID:          n.ID,
			Name:        n.Name,
			CNName:      n.CNName,
			DisplayName: n.DisplayName(),
			Description: n.Description,
			Tags:        append([]string(nil), n.Tags...),
			TrafficRate: n.TrafficRate,
			EnableQuic:  n.SupportsQuic(),
			NativeUDP:   n.SupportsNativeUDP(),
			Starred:     cfg.IsStarred(n.ID),
			Selected:    n.ID == curID,
			DelayMs:     e.delays[n.ID],
			Testing:     e.testingNodes[n.ID],
		}
		if ns.Selected {
			cp := ns
			curSnap = &cp
		}
		nodeSnaps = append(nodeSnaps, ns)
	}

	var eff core.TransportDecision
	if cur != nil {
		eff = core.ResolveTransportDecision(cfg.UDPAccelerationEnabled, cfg.NativeUDP, *cur)
	}

	customRules := make([]config.DomainRule, len(cfg.CustomRuleList))
	copy(customRules, cfg.CustomRuleList)
	ruleLabels := cfg.RuleLabels()
	var activeLabels []string
	if len(cfg.CustomRuleActiveLabels) == 0 {
		activeLabels = append([]string(nil), ruleLabels...)
	} else {
		activeLabels = append([]string(nil), cfg.CustomRuleActiveLabels...)
	}
	var traffic *config.UserTrafficInfo
	if cfg.SubscribeInfo.User != nil {
		cp := *cfg.SubscribeInfo.User
		traffic = &cp
	}
	speedHist := make([]SpeedSample, len(e.speedHistory))
	copy(speedHist, e.speedHistory)

	mode := core.ProxyMode(cfg.ProxyType)
	return Snapshot{
		AppVersion:             version.Version,
		CoreVersion:            e.core.Version(),
		CoreLibPath:            e.core.LibPath(),
		ConfigPath:             cfg.Path,
		GeoIPLoaded:            e.geoIPLoaded,
		GeoIPPath:              e.geoIPPath,
		RuleCount:              e.ruleCount,
		State:                  e.state,
		LastError:              e.lastError,
		CurrentNode:            curSnap,
		Mode:                   mode.String(),
		ModeCode:               mode,
		ProxyPort:              cfg.ProxyPort,
		AllowLAN:               cfg.AllowLanConnection,
		ProxyAuthEnabled:       cfg.ProxyUsername != "" && cfg.ProxyPassword != "",
		SystemProxy:            e.systemProxyOn,
		UDPAccelerationEnabled: cfg.UDPAccelerationEnabled,
		NativeUDP:              cfg.NativeUDP,
		EffectiveTransport:     eff,
		IPv6Enable:             cfg.IPv6Enable,
		UseBuildInDNS:          cfg.UseBuildInDNS,
		DirectConnPriority:     cfg.DirectConnPriority,
		AutoSetSysProxy:        cfg.AutoSetSysProxyOnStartUp,
		HasCloudAuth:           cfg.SubscribePassword != "",
		Syncing:                e.syncing,
		LastUpdateTime:         int64(cfg.LastUpdateTime),
		Traffic:                traffic,
		CustomRules:            customRules,
		RuleLabels:             ruleLabels,
		ActiveRuleLabels:       activeLabels,
		UploadBytes:            e.lastUploadBytes,
		DownloadBytes:          e.lastDownloadBytes,
		UploadSpeedBps:         e.uploadSpeedBps,
		DownloadSpeedBps:       e.downloadSpeedBps,
		SpeedHistory:           speedHist,
		TunnelPingMs:           e.tunnelPingMs,
		Pinging:                e.pinging,
		PingCompleted:          e.pingCompleted,
		PingTotal:              e.pingTotal,
		Nodes:                  nodeSnaps,
		UpdatedAt:              time.Now().UnixMilli(),
	}
}

// Connect 连接当前选中的节点。
func (e *Engine) Connect() error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	return e.connectCurrentLocked()
}

// Disconnect 主动断开隧道（保留本地代理监听）。
func (e *Engine) Disconnect() error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.tunnelOpInProgress = true
	e.tunnelDesired = false
	e.disconnectStreak = 0
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.tunnelOpInProgress = false
		e.mu.Unlock()
	}()

	e.core.CloseTunnel()
	e.mu.Lock()
	e.state = StateDisconnected
	e.lastError = ""
	e.uploadSpeedBps = 0
	e.downloadSpeedBps = 0
	e.tunnelPingMs = -1
	e.mu.Unlock()
	e.emit()
	return nil
}

// SelectNode 按 ID 切换节点并热重连，同时无损保存至 config.json。
func (e *Engine) SelectNode(nodeID int) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	var target *config.Node
	for _, n := range e.cfg.Nodes() {
		if n.ID == nodeID {
			cp := n
			target = &cp
			break
		}
	}
	if target == nil {
		e.mu.Unlock()
		return fmt.Errorf("未找到 ID 为 %d 的节点", nodeID)
	}
	used := target.ToUsedNodeInfo()
	e.cfg.UsedNodeInfo = &used
	saveErr := e.cfg.Save()
	e.mu.Unlock()

	if saveErr != nil {
		return fmt.Errorf("保存选中节点失败: %w", saveErr)
	}

	return e.connectCurrentLocked()
}

// SelectNodeByQuery 按关键词或 ID 匹配并热切换节点。
func (e *Engine) SelectNodeByQuery(query string) (*NodeSnapshot, error) {
	e.mu.RLock()
	node, err := e.cfg.FindNode(query)
	e.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if err := e.SelectNode(node.ID); err != nil {
		return nil, err
	}
	snap := e.Snapshot()
	return snap.CurrentNode, nil
}

// SetMode 切换代理模式（rule / global / direct）并无损保存。
func (e *Engine) SetMode(mode core.ProxyMode) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.cfg.ProxyType = int(mode)
	e.cfg.UDPProxyType = int(mode)
	saveErr := e.cfg.Save()
	e.mu.Unlock()

	e.core.SetProxyType(mode)
	e.core.SetUDPProxyType(mode)

	if saveErr != nil {
		return fmt.Errorf("保存代理模式失败: %w", saveErr)
	}
	e.emit()
	return nil
}

// SetUDPAcceleration 开关 UDP 加速（QUIC），若当前处于连接状态则热重连使新传输参数生效。
func (e *Engine) SetUDPAcceleration(enabled bool) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.cfg.UDPAccelerationEnabled = enabled
	wasActive := e.tunnelDesired || e.state == StateConnected || e.state == StateConnecting
	saveErr := e.cfg.Save()
	e.mu.Unlock()

	if saveErr != nil {
		return fmt.Errorf("保存 UDP 加速设置失败: %w", saveErr)
	}
	if wasActive {
		return e.connectCurrentLocked()
	}
	e.emit()
	return nil
}

// SetNativeUDP 开关原生 UDP，若当前处于连接状态则热重连使新传输参数生效。
func (e *Engine) SetNativeUDP(enabled bool) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.cfg.NativeUDP = enabled
	wasActive := e.tunnelDesired || e.state == StateConnected || e.state == StateConnecting
	saveErr := e.cfg.Save()
	e.mu.Unlock()

	if saveErr != nil {
		return fmt.Errorf("保存原生 UDP 设置失败: %w", saveErr)
	}
	if wasActive {
		return e.connectCurrentLocked()
	}
	e.emit()
	return nil
}

// SetSystemProxy 开关系统代理（macOS / Windows）。
func (e *Engine) SetSystemProxy(enabled bool) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.RLock()
	port := e.cfg.ProxyPort
	e.mu.RUnlock()

	var err error
	if enabled {
		err = e.sysProxy.Enable("127.0.0.1", port)
	} else {
		err = e.sysProxy.Disable()
	}
	if err != nil {
		log.Printf("[engine] 设置系统代理失败 (enabled=%v port=%d): %v", enabled, port, err)
		return err
	}

	log.Printf("[engine] 系统代理已切换: enabled=%v target=127.0.0.1:%d", enabled, port)
	e.mu.Lock()
	e.systemProxyOn = enabled
	e.mu.Unlock()
	e.emit()
	return nil
}

// ToggleStar 切换指定节点的收藏标记并保存。
func (e *Engine) ToggleStar(nodeID int) (bool, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	starred := e.cfg.ToggleStar(nodeID)
	saveErr := e.cfg.Save()
	e.mu.Unlock()

	if saveErr != nil {
		return starred, fmt.Errorf("保存收藏状态失败: %w", saveErr)
	}
	e.emit()
	return starred, nil
}

// UpdateLocalProxySettings 修改本地代理端口与局域网共享，自动重启本地监听并按需刷新系统代理。
func (e *Engine) UpdateLocalProxySettings(port int, allowLAN bool) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("代理端口无效: %d（范围 1-65535）", port)
	}

	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	oldPort := e.cfg.ProxyPort
	oldLAN := e.cfg.AllowLanConnection
	e.cfg.ProxyPort = port
	e.cfg.AllowLanConnection = allowLAN
	sysProxyWasOn := e.systemProxyOn
	e.mu.Unlock()

	if err := e.startLocalProxyLocked(); err != nil {
		// 回滚配置并尝试恢复原端口监听
		e.mu.Lock()
		e.cfg.ProxyPort = oldPort
		e.cfg.AllowLanConnection = oldLAN
		e.mu.Unlock()
		_ = e.startLocalProxyLocked()
		return err
	}

	e.mu.Lock()
	saveErr := e.cfg.Save()
	e.mu.Unlock()
	if saveErr != nil {
		return fmt.Errorf("保存代理设置失败: %w", saveErr)
	}

	// 如果系统代理处于开启状态且端口变了，自动同步新端口
	if sysProxyWasOn && oldPort != port {
		if err := e.sysProxy.Enable("127.0.0.1", port); err != nil {
			e.syncSystemProxyLocked()
		}
	}

	e.emit()
	return nil
}

// RefreshSystemProxy 主动核对操作系统当前真实的系统代理状态，若与内存状态不一致则更新并广播快照。
// 当有其他状态变更操作正在执行时跳过本次检查，避免与正在进行的系统代理切换产生竞态。
func (e *Engine) RefreshSystemProxy() bool {
	if e.sysProxy == nil {
		return false
	}
	if !e.opMu.TryLock() {
		e.mu.RLock()
		cur := e.systemProxyOn
		e.mu.RUnlock()
		return cur
	}
	defer e.opMu.Unlock()

	return e.syncSystemProxyLocked()
}

func (e *Engine) syncSystemProxyLocked() bool {
	if e.sysProxy == nil {
		return false
	}
	e.mu.RLock()
	port := e.cfg.ProxyPort
	prev := e.systemProxyOn
	e.mu.RUnlock()

	enabled, err := e.sysProxy.IsEnabled("127.0.0.1", port)
	if err != nil {
		return prev
	}
	if enabled != prev {
		e.mu.Lock()
		e.systemProxyOn = enabled
		e.mu.Unlock()
		e.emit()
	}
	return enabled
}

func (e *Engine) sysProxyWatchLoop() {
	interval := e.sysProxyCheckInterval
	if interval <= 0 {
		interval = defaultSysProxyCheckInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.RefreshSystemProxy()
		}
	}
}

// UpdateNetworkSettings 更新 IPv6 / 内置 DNS / 直连优先设置并实时应用到核心库。
func (e *Engine) UpdateNetworkSettings(ipv6, builtInDNS, directPriority bool) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.cfg.IPv6Enable = ipv6
	e.cfg.UseBuildInDNS = builtInDNS
	e.cfg.DirectConnPriority = directPriority
	timeout := e.cfg.DirectConnPriorityTimeout
	saveErr := e.cfg.Save()
	e.mu.Unlock()

	e.core.SetIPv6Enable(ipv6)
	e.core.SetUseBuildInDNS(builtInDNS)
	e.core.SetDirectConnPriority(directPriority, timeout)

	if saveErr != nil {
		return fmt.Errorf("保存网络设置失败: %w", saveErr)
	}
	e.emit()
	return nil
}

// SetAutoSysProxyOnStartUp 设置启动连接后是否自动开启系统代理。
func (e *Engine) SetAutoSysProxyOnStartUp(enabled bool) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.cfg.AutoSetSysProxyOnStartUp = enabled
	saveErr := e.cfg.Save()
	e.mu.Unlock()

	if saveErr != nil {
		return fmt.Errorf("保存启动系统代理设置失败: %w", saveErr)
	}
	e.emit()
	return nil
}

// LocalProxyURL 在隧道已连上且本地代理已启动时返回本地混合代理地址，
// 供云端同步与在线更新优先经由内置隧道访问，无需依赖操作系统全局代理开关。
func (e *Engine) LocalProxyURL() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.proxyStarted || e.state != StateConnected || e.cfg.ProxyPort <= 0 {
		return ""
	}
	u := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("127.0.0.1:%d", e.cfg.ProxyPort),
	}
	if e.cfg.ProxyUsername != "" && e.cfg.ProxyPassword != "" {
		u.User = url.UserPassword(e.cfg.ProxyUsername, e.cfg.ProxyPassword)
	}
	return u.String()
}

// localProxyURLForCloud 保持包内调用简洁，复用 LocalProxyURL。
func (e *Engine) localProxyURLForCloud() string {
	return e.LocalProxyURL()
}

// SyncSubscription 从官方云端同步最新订阅节点、流量用量、到期时间、VIP 等级与自定义分流规则，
// 无损保存至 config.json 并立即热重载核心规则（不断开当前隧道）。
func (e *Engine) SyncSubscription(ctx context.Context) (Snapshot, error) {
	if e.cloud == nil {
		return Snapshot{}, errors.New("未启用云端同步客户端")
	}

	e.mu.Lock()
	if e.syncing {
		e.mu.Unlock()
		return Snapshot{}, errors.New("正在同步中，请稍候")
	}
	pw := e.cfg.SubscribePassword
	if pw == "" {
		e.mu.Unlock()
		return Snapshot{}, errors.New("尚未配置订阅密码，请先在订阅卡片中配置")
	}
	e.syncing = true
	e.mu.Unlock()
	e.emit()

	defer func() {
		e.mu.Lock()
		e.syncing = false
		e.mu.Unlock()
		e.emit()
	}()

	proxyURL := e.localProxyURLForCloud()
	sub, err := e.cloud.FetchSubscribe(ctx, pw, proxyURL)
	if err != nil {
		return Snapshot{}, err
	}
	// 同步拉取云端自定义规则（与原版 SubscribeService.fetchAndSaveSubscribe 一致；若云端规则拉取失败不阻断订阅节点与流量更新）
	customRules, _ := e.cloud.FetchCustomRules(ctx, pw, proxyURL)

	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.cfg.ApplySyncedData(*sub, customRules, time.Now())
	saveErr := e.cfg.Save()
	e.mu.Unlock()
	if saveErr != nil {
		return Snapshot{}, fmt.Errorf("保存同步结果失败: %w", saveErr)
	}

	e.reloadRulesLocked()
	e.emit()
	return e.Snapshot(), nil
}

// SubscribePassword 返回当前已保存的订阅密码（仅供同进程 GUI 密码框回填查看，不经脱敏 Snapshot 对外暴露）。
func (e *Engine) SubscribePassword() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.cfg == nil {
		return ""
	}
	return e.cfg.SubscribePassword
}

// SetSubscribePassword 使用新订阅密码先向云端校验并拉取最新节点、流量与自定义规则，
// 仅当云端校验成功后才原子写回 config.json 并热重载规则，失败时不覆盖现有凭据与配置。
func (e *Engine) SetSubscribePassword(ctx context.Context, password string) (Snapshot, error) {
	if e.cloud == nil {
		return Snapshot{}, errors.New("未启用云端同步客户端")
	}
	pw := strings.TrimSpace(password)
	if pw == "" {
		return Snapshot{}, errors.New("订阅密码不能为空")
	}

	e.mu.Lock()
	if e.syncing {
		e.mu.Unlock()
		return Snapshot{}, errors.New("正在同步中，请稍候")
	}
	e.syncing = true
	e.mu.Unlock()
	e.emit()

	defer func() {
		e.mu.Lock()
		e.syncing = false
		e.mu.Unlock()
		e.emit()
	}()

	proxyURL := e.localProxyURLForCloud()
	sub, err := e.cloud.FetchSubscribe(ctx, pw, proxyURL)
	if err != nil {
		return Snapshot{}, err
	}
	customRules, _ := e.cloud.FetchCustomRules(ctx, pw, proxyURL)

	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.cfg.SetSubscribePassword(pw)
	e.cfg.ApplySyncedData(*sub, customRules, time.Now())
	saveErr := e.cfg.Save()
	e.mu.Unlock()
	if saveErr != nil {
		return Snapshot{}, fmt.Errorf("保存订阅配置失败: %w", saveErr)
	}

	e.reloadRulesLocked()
	e.emit()
	return e.Snapshot(), nil
}

// SyncCustomRules 单独从云端拉取最新自定义规则列表并热重载。
func (e *Engine) SyncCustomRules(ctx context.Context) ([]config.DomainRule, error) {
	if e.cloud == nil {
		return nil, errors.New("未启用云端同步客户端")
	}

	e.mu.Lock()
	if e.syncing {
		e.mu.Unlock()
		return nil, errors.New("正在同步中，请稍候")
	}
	pw := e.cfg.SubscribePassword
	if pw == "" {
		e.mu.Unlock()
		return nil, errors.New("当前配置缺少订阅凭据")
	}
	e.syncing = true
	e.mu.Unlock()
	e.emit()

	defer func() {
		e.mu.Lock()
		e.syncing = false
		e.mu.Unlock()
		e.emit()
	}()

	rules, err := e.cloud.FetchCustomRules(ctx, pw, e.localProxyURLForCloud())
	if err != nil {
		return nil, err
	}

	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.cfg.ReplaceCustomRules(rules, "")
	saveErr := e.cfg.Save()
	e.mu.Unlock()
	if saveErr != nil {
		return nil, fmt.Errorf("保存云端规则失败: %w", saveErr)
	}

	e.reloadRulesLocked()
	e.emit()
	return rules, nil
}

// AddCustomRule 新增自定义规则：存在订阅凭据时同步写入云端并拉取云端 ID，
// 再无损保存本地并立即热重载核心规则（不断开当前隧道）。
func (e *Engine) AddCustomRule(rule config.DomainRule) (config.DomainRule, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.RLock()
	pw := e.cfg.SubscribePassword
	e.mu.RUnlock()

	if pw != "" && e.cloud != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		proxyURL := e.localProxyURLForCloud()
		if err := e.cloud.SaveCustomRule(ctx, pw, rule, proxyURL); err != nil {
			return config.DomainRule{}, err
		}
		// 与原版一致：保存后刷新云端列表以获取服务端分配的权威 ID
		if remoteList, err := e.cloud.FetchCustomRules(ctx, pw, proxyURL); err == nil {
			e.mu.Lock()
			e.cfg.ReplaceCustomRules(remoteList, rule.Label)
			saveErr := e.cfg.Save()
			e.mu.Unlock()
			if saveErr != nil {
				return config.DomainRule{}, fmt.Errorf("保存自定义规则失败: %w", saveErr)
			}
			e.reloadRulesLocked()
			e.emit()
			for _, r := range remoteList {
				if r.Domain == rule.Domain && r.Matching == rule.Matching && r.ProxyType == rule.ProxyType {
					return r, nil
				}
			}
			if len(remoteList) > 0 {
				return remoteList[0], nil
			}
			return rule, nil
		}
	}

	e.mu.Lock()
	added, err := e.cfg.AddCustomRule(rule)
	if err != nil {
		e.mu.Unlock()
		return config.DomainRule{}, err
	}
	saveErr := e.cfg.Save()
	e.mu.Unlock()
	if saveErr != nil {
		return config.DomainRule{}, fmt.Errorf("保存自定义规则失败: %w", saveErr)
	}

	e.reloadRulesLocked()
	e.emit()
	return added, nil
}

// UpdateCustomRule 更新已有自定义规则：存在订阅凭据时同步至云端并立即热生效。
func (e *Engine) UpdateCustomRule(rule config.DomainRule) (config.DomainRule, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.RLock()
	pw := e.cfg.SubscribePassword
	existing, exists := e.cfg.FindCustomRule(rule.ID)
	e.mu.RUnlock()
	if !exists {
		return config.DomainRule{}, fmt.Errorf("未找到 ID 为 %d 的自定义规则", rule.ID)
	}
	if rule.Enable == nil {
		rule.Enable = existing.Enable
		if rule.Enable == nil {
			rule.Enable = config.IntPtr(1)
		}
	}
	if rule.CreateTime == 0 {
		rule.CreateTime = existing.CreateTime
	}

	if pw != "" && e.cloud != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := e.cloud.UpdateCustomRule(ctx, pw, rule, e.localProxyURLForCloud()); err != nil {
			return config.DomainRule{}, err
		}
	}

	e.mu.Lock()
	updated, err := e.cfg.UpdateCustomRule(rule)
	if err != nil {
		e.mu.Unlock()
		return config.DomainRule{}, err
	}
	saveErr := e.cfg.Save()
	e.mu.Unlock()
	if saveErr != nil {
		return config.DomainRule{}, fmt.Errorf("保存自定义规则失败: %w", saveErr)
	}

	e.reloadRulesLocked()
	e.emit()
	return updated, nil
}

// DeleteCustomRule 删除指定 ID 的自定义规则：存在订阅凭据时同步从云端删除并立即热生效。
func (e *Engine) DeleteCustomRule(id int) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.RLock()
	pw := e.cfg.SubscribePassword
	_, exists := e.cfg.FindCustomRule(id)
	e.mu.RUnlock()
	if !exists {
		return fmt.Errorf("未找到 ID 为 %d 的自定义规则", id)
	}

	if pw != "" && e.cloud != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := e.cloud.RemoveCustomRule(ctx, pw, id, e.localProxyURLForCloud()); err != nil {
			return err
		}
	}

	e.mu.Lock()
	if err := e.cfg.DeleteCustomRule(id); err != nil {
		e.mu.Unlock()
		return err
	}
	saveErr := e.cfg.Save()
	e.mu.Unlock()
	if saveErr != nil {
		return fmt.Errorf("保存自定义规则失败: %w", saveErr)
	}

	e.reloadRulesLocked()
	e.emit()
	return nil
}

// ToggleCustomRule 切换指定 ID 的自定义规则启用状态：存在订阅凭据时同步至云端并立即热生效。
func (e *Engine) ToggleCustomRule(id int) (bool, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.RLock()
	pw := e.cfg.SubscribePassword
	cur, exists := e.cfg.FindCustomRule(id)
	e.mu.RUnlock()
	if !exists {
		return false, fmt.Errorf("未找到 ID 为 %d 的自定义规则", id)
	}

	next := !cur.IsEnabled()
	v := 0
	if next {
		v = 1
	}
	cur.Enable = &v

	if pw != "" && e.cloud != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := e.cloud.UpdateCustomRule(ctx, pw, cur, e.localProxyURLForCloud()); err != nil {
			return false, err
		}
	}

	e.mu.Lock()
	enabled, err := e.cfg.ToggleCustomRule(id)
	if err != nil {
		e.mu.Unlock()
		return false, err
	}
	saveErr := e.cfg.Save()
	e.mu.Unlock()
	if saveErr != nil {
		return enabled, fmt.Errorf("保存自定义规则失败: %w", saveErr)
	}

	e.reloadRulesLocked()
	e.emit()
	return enabled, nil
}

// SetRuleLabelActive 启停指定自定义规则标签分组并立即热生效。
func (e *Engine) SetRuleLabelActive(label string, active bool) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.cfg.SetRuleLabelActive(label, active)
	saveErr := e.cfg.Save()
	e.mu.Unlock()
	if saveErr != nil {
		return fmt.Errorf("保存规则分组状态失败: %w", saveErr)
	}

	e.reloadRulesLocked()
	e.emit()
	return nil
}

// ActiveRules 按需返回当前加载至核心库的全部生效规则（含来源与命中优先级顺序），不进入高频状态快照。
func (e *Engine) ActiveRules() []config.ActiveRuleEntry {
	e.mu.RLock()
	cfg := e.cfg
	e.mu.RUnlock()
	if cfg == nil {
		return nil
	}
	return cfg.ActiveRuleEntries()
}

// Connections 返回底层核心库当前活跃连接列表（按活跃时间降序），并顺带更新进程累计流量统计。
func (e *Engine) Connections() ([]core.ConnectionInfo, error) {
	list, err := e.core.TunnelConnections()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	e.enrichConnectionProcesses(list, now, true)
	if e.procTraffic != nil {
		e.procTraffic.Observe(list, now)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].ActiveTime != list[j].ActiveTime {
			return list[i].ActiveTime > list[j].ActiveTime
		}
		ti := list[i].UploadTrafficTotal + list[i].DownloadTrafficTotal
		tj := list[j].UploadTrafficTotal + list[j].DownloadTrafficTotal
		return ti > tj
	})
	return list, nil
}

// ProcessTrafficStats 返回本次运行（或上次清零后）按进程聚合的代理与直连流量排行快照。
func (e *Engine) ProcessTrafficStats() ProcessTrafficSnapshot {
	if e.procTraffic == nil {
		return ProcessTrafficSnapshot{}
	}
	return e.procTraffic.Snapshot()
}

// ResetProcessTraffic 清零按进程统计的累计流量，便于重新观察当前阶段的流量占用排行。
func (e *Engine) ResetProcessTraffic() {
	if e.procTraffic == nil {
		return
	}
	e.procTraffic.Reset(time.Now())
}

// enrichConnectionProcesses 修正 macOS 下核心库按端口反查进程时误将代理服务端自身当作来源进程的问题。
// forceLookup 为 false（后台定时采样）时仅在发现尚未缓存的未知连接且距上次反查超过节流间隔时才触发系统反查，降低开销。
func (e *Engine) enrichConnectionProcesses(list []core.ConnectionInfo, now time.Time, forceLookup bool) {
	if len(list) == 0 {
		e.mu.Lock()
		clear(e.connProcCache)
		e.mu.Unlock()
		return
	}

	e.mu.RLock()
	proxyPort := e.cfg.ProxyPort
	lookupFn := e.procLookupFn
	selfNames := e.selfProcNames
	lastLookup := e.lastProcLookup
	needsLookup := false
	for i := range list {
		c := &list[i]
		if _, cached := e.connProcCache[c.CloseKey()]; cached {
			continue
		}
		if c.ProcessName == "" || isSelfProcessName(c.ProcessName, selfNames) {
			needsLookup = true
			break
		}
	}
	e.mu.RUnlock()

	var resolved map[string]string
	if lookupFn != nil && proxyPort > 0 && needsLookup && (forceLookup || now.Sub(lastLookup) >= minBackgroundProcLookupGap) {
		resolved = lookupFn(proxyPort)
		e.mu.Lock()
		e.lastProcLookup = now
		e.mu.Unlock()
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	nextCache := make(map[string]string, len(list))
	for i := range list {
		c := &list[i]
		closeKey := c.CloseKey()
		srcKey := formatConnSourceKey(c.SourceAddr, c.SourcePort)

		if proc, ok := resolved[srcKey]; ok && proc != "" {
			c.ProcessName = proc
			nextCache[closeKey] = proc
			continue
		}
		if cached, ok := e.connProcCache[closeKey]; ok && cached != "" {
			c.ProcessName = cached
			nextCache[closeKey] = cached
			continue
		}
		if isSelfProcessName(c.ProcessName, selfNames) {
			c.ProcessName = ""
			continue
		}
		if c.ProcessName != "" {
			nextCache[closeKey] = c.ProcessName
		}
	}
	e.connProcCache = nextCache
}

// CloseConnection 强制关闭核心库中的指定连接（使新规则立即生效）。
func (e *Engine) CloseConnection(key string) {
	if key == "" {
		return
	}
	e.core.CloseConnection(key)
}

func (e *Engine) reloadRulesLocked() {
	e.mu.RLock()
	rules := e.cfg.ActiveRules()
	e.mu.RUnlock()

	e.core.ClearDomainRules()
	for _, r := range rules {
		e.core.SetDomainRule(r.Domain, int32(r.Matching), int32(r.ProxyType), int32(r.DirectConnPriority))
	}

	e.mu.Lock()
	e.ruleCount = len(rules)
	e.mu.Unlock()
}

// Reload 重新从磁盘读取配置文件，刷新规则、代理监听与隧道。
func (e *Engine) Reload() error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.RLock()
	path := e.cfg.Path
	oldPort := e.cfg.ProxyPort
	oldLAN := e.cfg.AllowLanConnection
	oldUser := e.cfg.ProxyUsername
	oldPass := e.cfg.ProxyPassword
	wasActive := e.tunnelDesired || e.state == StateConnected || e.state == StateConnecting
	e.mu.RUnlock()

	newCfg, err := config.Load(path)
	if err != nil {
		return err
	}

	e.mu.Lock()
	e.cfg = newCfg
	e.mu.Unlock()

	e.applyConfigToCoreLocked()

	proxyChanged := oldPort != newCfg.ProxyPort ||
		oldLAN != newCfg.AllowLanConnection ||
		oldUser != newCfg.ProxyUsername ||
		oldPass != newCfg.ProxyPassword
	if !e.proxyStarted || proxyChanged {
		if err := e.startLocalProxyLocked(); err != nil {
			return err
		}
	}

	if wasActive {
		if err := e.connectCurrentLocked(); err != nil {
			return err
		}
	}
	e.syncSystemProxyLocked()
	e.emit()
	return nil
}

// PingAll 并发测速全部节点（默认限流并发数 6），实时更新延迟快照。
func (e *Engine) PingAll(ctx context.Context) ([]NodeSnapshot, error) {
	e.mu.Lock()
	if e.pinging {
		e.mu.Unlock()
		return nil, errors.New("测速正在进行中")
	}
	nodes := e.cfg.Nodes()
	udpAcc := e.cfg.UDPAccelerationEnabled
	if len(nodes) == 0 {
		e.mu.Unlock()
		return nil, errors.New("没有可测速的节点")
	}
	e.pinging = true
	e.pingCompleted = 0
	e.pingTotal = len(nodes)
	for _, n := range nodes {
		e.testingNodes[n.ID] = true
	}
	e.mu.Unlock()
	e.emit()

	defer func() {
		e.mu.Lock()
		e.pinging = false
		clear(e.testingNodes)
		e.mu.Unlock()
		e.emit()
	}()

	const maxWorkers = 6
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup

	for _, node := range nodes {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(n config.Node) {
			defer wg.Done()
			defer func() { <-sem }()

			enableQuic := udpAcc && n.SupportsQuic()
			delay := e.core.TestTunnelDelay(n, enableQuic)
			if delay <= 0 {
				delay = -1
			}

			e.mu.Lock()
			e.delays[n.ID] = delay
			delete(e.testingNodes, n.ID)
			e.pingCompleted++
			e.mu.Unlock()
			e.emit()
		}(node)
	}
	wg.Wait()

	snap := e.Snapshot()
	sorted := make([]NodeSnapshot, len(snap.Nodes))
	copy(sorted, snap.Nodes)
	sort.SliceStable(sorted, func(i, j int) bool {
		di, dj := sorted[i].DelayMs, sorted[j].DelayMs
		if di <= 0 && dj <= 0 {
			return sorted[i].ID < sorted[j].ID
		}
		if di <= 0 {
			return false
		}
		if dj <= 0 {
			return true
		}
		return di < dj
	})
	return sorted, nil
}

// PingNode 对单个节点进行测速。
func (e *Engine) PingNode(nodeID int) (int64, error) {
	e.mu.Lock()
	var target *config.Node
	for _, n := range e.cfg.Nodes() {
		if n.ID == nodeID {
			cp := n
			target = &cp
			break
		}
	}
	if target == nil {
		e.mu.Unlock()
		return -1, fmt.Errorf("未找到 ID 为 %d 的节点", nodeID)
	}
	udpAcc := e.cfg.UDPAccelerationEnabled
	e.testingNodes[nodeID] = true
	e.mu.Unlock()
	e.emit()

	enableQuic := udpAcc && target.SupportsQuic()
	delay := e.core.TestTunnelDelay(*target, enableQuic)
	if delay <= 0 {
		delay = -1
	}

	e.mu.Lock()
	e.delays[nodeID] = delay
	delete(e.testingNodes, nodeID)
	e.mu.Unlock()
	e.emit()
	return delay, nil
}

func (e *Engine) applyConfigToCoreLocked() {
	e.mu.RLock()
	cfg := e.cfg
	e.mu.RUnlock()

	if cfg.UUID != "" {
		e.core.SetClientUUID(cfg.UUID)
	}
	e.core.SetProxyType(core.ProxyMode(cfg.ProxyType))
	e.core.SetUDPProxyType(core.ProxyMode(cfg.UDPProxyType))
	e.core.SetIPv6Enable(cfg.IPv6Enable)
	e.core.SetUseBuildInDNS(cfg.UseBuildInDNS)
	e.core.SetDirectConnPriority(cfg.DirectConnPriority, cfg.DirectConnPriorityTimeout)

	rules := cfg.ActiveRules()
	e.core.ClearDomainRules()
	for _, r := range rules {
		e.core.SetDomainRule(r.Domain, int32(r.Matching), int32(r.ProxyType), int32(r.DirectConnPriority))
	}

	e.mu.Lock()
	e.ruleCount = len(rules)
	e.mu.Unlock()
}

func (e *Engine) startLocalProxyLocked() error {
	e.mu.RLock()
	port := e.cfg.ProxyPort
	allowLAN := e.cfg.AllowLanConnection
	user := e.cfg.ProxyUsername
	pass := e.cfg.ProxyPassword
	e.mu.RUnlock()

	// 重启更新时旧进程可能仍在短暂释放监听套接字，最多重试 10 次（约 1.5s），绝不擅自改动端口
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if err = e.core.StartProxy(port, allowLAN, user, pass); err == nil {
			e.mu.Lock()
			e.proxyStarted = true
			e.mu.Unlock()
			log.Printf("[engine] 本地混合代理已监听: port=%d allowLAN=%v auth=%v attempt=%d", port, allowLAN, user != "" && pass != "", attempt+1)
			return nil
		}
		select {
		case <-e.stopCh:
			return err
		case <-time.After(150 * time.Millisecond):
		}
	}
	log.Printf("[engine] 启动本地混合代理失败 (port=%d allowLAN=%v): %v", port, allowLAN, err)
	return err
}

func (e *Engine) connectCurrentLocked() error {
	e.mu.Lock()
	e.tunnelOpInProgress = true
	e.disconnectStreak = 0
	cfg := e.cfg
	node := cfg.CurrentNode()
	udpAcc := cfg.UDPAccelerationEnabled
	nativeUDP := cfg.NativeUDP
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.tunnelOpInProgress = false
		e.mu.Unlock()
	}()

	if node == nil {
		err := errors.New("没有可连接的节点")
		e.mu.Lock()
		e.state = StateError
		e.tunnelDesired = false
		e.lastError = err.Error()
		e.uploadSpeedBps = 0
		e.downloadSpeedBps = 0
		e.tunnelPingMs = -1
		e.mu.Unlock()
		e.emit()
		return err
	}

	decision := core.ResolveTransportDecision(udpAcc, nativeUDP, *node)
	log.Printf("[engine] 开始连接节点: id=%d name=%q quic=%v nativeUDP=%v", node.ID, node.DisplayName(), decision.EnableQuic, decision.EnableNativeUDP)

	e.mu.Lock()
	e.state = StateConnecting
	e.lastError = ""
	e.uploadSpeedBps = 0
	e.downloadSpeedBps = 0
	e.tunnelPingMs = -1
	e.mu.Unlock()
	e.emit()

	// 先关闭可能残留的旧隧道会话，避免轮询误读上一节点的就绪状态（与官方 reSelectTunnel 一致）
	e.core.CloseTunnel()

	// 先更新原生 UDP 选项，再建连（支持热切节点与热切 UDP/QUIC 参数）
	e.core.SetNativeUDP(decision.EnableNativeUDP)
	if err := e.core.ConnectTunnel(node.Host, node.Port, node.Password, decision.EnableQuic); err != nil {
		log.Printf("[engine] 节点建连调用失败 (id=%d name=%q): %v", node.ID, node.DisplayName(), err)
		e.mu.Lock()
		e.state = StateError
		e.tunnelDesired = false
		e.lastError = err.Error()
		e.mu.Unlock()
		e.emit()
		return err
	}

	pollInterval := e.connectPollInterval
	if pollInterval <= 0 {
		pollInterval = defaultConnectPollInterval
	}
	pollAttempts := e.connectPollAttempts
	if pollAttempts <= 0 {
		pollAttempts = defaultConnectPollAttempts
	}

	// 轮询等待核心库确认隧道就绪（status == 0 表示已连接；默认最多等待 8 秒，与官方 ProxyService 一致）
	connected := false
	for i := 0; i < pollAttempts; i++ {
		if e.core.TunnelStatus() == 0 {
			connected = true
			break
		}
		select {
		case <-e.stopCh:
			e.core.CloseTunnel()
			e.mu.Lock()
			e.tunnelDesired = false
			e.mu.Unlock()
			return errors.New("客户端正在退出")
		case <-time.After(pollInterval):
		}
	}

	var timeoutStatus int32
	if !connected {
		timeoutStatus = e.core.TunnelStatus()
		e.core.CloseTunnel()
	}

	e.mu.Lock()
	if connected {
		e.state = StateConnected
		e.tunnelDesired = true
		e.disconnectStreak = 0
		e.lastError = ""
		e.lastUploadBytes = 0
		e.lastDownloadBytes = 0
		e.uploadSpeedBps = 0
		e.downloadSpeedBps = 0
		e.lastSampleTime = time.Now()
		e.tunnelPingMs = e.core.TunnelPingDelay()
		log.Printf("[engine] 节点已连接: id=%d name=%q ping=%dms", node.ID, node.DisplayName(), e.tunnelPingMs)
	} else {
		e.state = StateError
		e.tunnelDesired = false
		e.disconnectStreak = 0
		e.lastError = fmt.Sprintf("连接节点 %s 超时（状态码 %d）", node.DisplayName(), timeoutStatus)
		log.Printf("[engine] 节点连接超时: id=%d name=%q status=%d", node.ID, node.DisplayName(), timeoutStatus)
	}
	e.mu.Unlock()
	e.emit()

	if !connected {
		return errors.New(e.lastError)
	}
	return nil
}

func (e *Engine) statsLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.stopCh:
			return
		case now := <-ticker.C:
			e.sampleStats(now)
		}
	}
}

func (e *Engine) sampleStats(now time.Time) {
	e.mu.RLock()
	opInProgress := e.tunnelOpInProgress
	active := e.tunnelDesired || e.state == StateConnected || e.state == StateConnecting
	e.mu.RUnlock()

	if opInProgress || !active {
		return
	}

	status := e.core.TunnelStatus()
	var up, down int64
	var ping int32 = -1
	if status == 0 {
		// 核心库 get_tunnel_upload / get_tunnel_download 在 1s 周期下直接返回当前每秒上下行字节数（Bytes/s），
		// 与官方客户端 ProxyService 行为一致，不可对相邻两次读数做差值相减。
		up = e.core.TunnelUploadBytes()
		down = e.core.TunnelDownloadBytes()
		if up < 0 {
			up = 0
		}
		if down < 0 {
			down = 0
		}
		ping = e.core.TunnelPingDelay()
	}

	var logMsg string
	e.mu.Lock()
	// 二次确认：避免在读取核心库期间被显式建连/断开操作抢先改写后出现旧状态回写
	if e.tunnelOpInProgress || !(e.tunnelDesired || e.state == StateConnected || e.state == StateConnecting) {
		e.mu.Unlock()
		return
	}

	nodeName := ""
	nodeID := 0
	if cur := e.cfg.CurrentNode(); cur != nil {
		nodeName = cur.DisplayName()
		nodeID = cur.ID
	}
	threshold := e.disconnectErrorThreshold
	if threshold <= 0 {
		threshold = defaultDisconnectErrorThreshold
	}

	if status == 0 {
		prevState := e.state
		e.disconnectStreak = 0
		if prevState != StateConnected {
			e.state = StateConnected
			e.lastError = ""
			logMsg = fmt.Sprintf("[engine] 隧道已恢复连接: id=%d name=%q ping=%dms", nodeID, nodeName, ping)
		}
		e.uploadSpeedBps = up
		e.downloadSpeedBps = down
		e.lastUploadBytes += up
		e.lastDownloadBytes += down
		e.lastSampleTime = now
		e.tunnelPingMs = ping
		e.appendSpeedSampleLocked(up, down)
	} else {
		prevState := e.state
		e.disconnectStreak++
		e.uploadSpeedBps = 0
		e.downloadSpeedBps = 0
		e.lastSampleTime = now
		e.tunnelPingMs = -1
		e.appendSpeedSampleLocked(0, 0)

		if e.disconnectStreak >= threshold {
			e.state = StateError
			e.lastError = fmt.Sprintf("节点 %s 连接中断（状态码 %d），正在尝试重连", nodeName, status)
			if prevState != StateError {
				logMsg = fmt.Sprintf("[engine] 隧道持续断开，已转为连接异常: id=%d name=%q status=%d streak=%d", nodeID, nodeName, status, e.disconnectStreak)
			}
		} else if prevState == StateConnected {
			e.state = StateConnecting
			e.lastError = ""
			logMsg = fmt.Sprintf("[engine] 隧道连接中断，正在重连: id=%d name=%q status=%d", nodeID, nodeName, status)
		}
	}
	e.mu.Unlock()

	if logMsg != "" {
		log.Print(logMsg)
	}

	// 在后台每秒顺带采样活跃连接以持续累计各进程走代理与直连的流量，
	// 即使主窗口未打开也能完整记录短连接和后台进程的流量大户。
	if e.procTraffic != nil {
		if conns, err := e.core.TunnelConnections(); err == nil {
			e.enrichConnectionProcesses(conns, now, false)
			e.procTraffic.Observe(conns, now)
		}
	}

	e.emit()
}

func (e *Engine) appendSpeedSampleLocked(upBps, downBps int64) {
	if len(e.speedHistory) >= maxSpeedHistoryPoints {
		copy(e.speedHistory, e.speedHistory[1:])
		e.speedHistory[len(e.speedHistory)-1] = SpeedSample{
			UploadBps:   upBps,
			DownloadBps: downBps,
		}
		return
	}
	e.speedHistory = append(e.speedHistory, SpeedSample{
		UploadBps:   upBps,
		DownloadBps: downBps,
	})
}
