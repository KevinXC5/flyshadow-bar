package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

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

// Snapshot 是整个客户端运行状态的只读快照，用于 UI、托盘与本地控制 API。
type Snapshot struct {
	AppVersion             string                 `json:"appVersion"`
	CoreVersion            string                 `json:"coreVersion"`
	CoreLibPath            string                 `json:"coreLibPath"`
	ConfigPath             string                 `json:"configPath"`
	GeoIPLoaded            bool                   `json:"geoipLoaded"`
	GeoIPPath              string                 `json:"geoipPath,omitempty"`
	RuleCount              int                    `json:"ruleCount"`
	State                  TunnelState            `json:"state"`
	LastError              string                 `json:"lastError,omitempty"`
	CurrentNode            *NodeSnapshot          `json:"currentNode,omitempty"`
	Mode                   string                 `json:"mode"`
	ModeCode               core.ProxyMode         `json:"modeCode"`
	ProxyPort              int                    `json:"proxyPort"`
	AllowLAN               bool                   `json:"allowLan"`
	ProxyAuthEnabled       bool                   `json:"proxyAuthEnabled"`
	SystemProxy            bool                   `json:"systemProxy"`
	UDPAccelerationEnabled bool                   `json:"udpAccelerationEnabled"`
	NativeUDP              bool                   `json:"nativeUdp"`
	EffectiveTransport     core.TransportDecision `json:"effectiveTransport"`
	IPv6Enable             bool                    `json:"ipv6Enable"`
	UseBuildInDNS          bool                    `json:"useBuildInDns"`
	DirectConnPriority     bool                    `json:"directConnPriority"`
	AutoSetSysProxy        bool                    `json:"autoSetSysProxy"`
	Traffic                *config.UserTrafficInfo `json:"traffic,omitempty"`
	CustomRules            []config.DomainRule     `json:"customRules"`
	RuleLabels             []string                `json:"ruleLabels"`
	ActiveRuleLabels       []string                `json:"activeRuleLabels"`
	UploadBytes            int64                   `json:"uploadBytes"`
	DownloadBytes          int64                  `json:"downloadBytes"`
	UploadSpeedBps         int64                  `json:"uploadSpeedBps"`
	DownloadSpeedBps       int64                  `json:"downloadSpeedBps"`
	TunnelPingMs           int32                  `json:"tunnelPingMs"`
	Pinging                bool                   `json:"pinging"`
	PingCompleted          int                    `json:"pingCompleted"`
	PingTotal              int                    `json:"pingTotal"`
	Nodes                  []NodeSnapshot         `json:"nodes"`
	UpdatedAt              int64                  `json:"updatedAt"`
}

// Options 允许在启动或测试时注入依赖。
type Options struct {
	ConfigPath          string
	CoreLibPath         string
	CoreClient          core.Client
	SysProxy            sysproxy.Manager
	AutoConnect         bool
	DisableAutoSysProxy bool
}

// Engine 统一管理配置、底层核心库生命周期、代理、隧道、系统代理与实时统计。
type Engine struct {
	mu                  sync.RWMutex
	opMu                sync.Mutex // 串行化状态变更操作（切节点、启停、重载）
	core                core.Client
	sysProxy            sysproxy.Manager
	cfg                 *config.Config
	disableAutoSysProxy bool

	geoIPLoaded bool
	geoIPPath   string
	ruleCount   int

	state         TunnelState
	lastError     string
	systemProxyOn bool
	proxyStarted  bool

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
	tunnelPingMs      int32

	listeners map[int]func(Snapshot)
	nextSubID int

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

	e := &Engine{
		core:                cli,
		sysProxy:            sp,
		cfg:                 cfg,
		disableAutoSysProxy: opts.DisableAutoSysProxy,
		state:               StateDisconnected,
		delays:              make(map[int]int64),
		testingNodes:        make(map[int]bool),
		listeners:           make(map[int]func(Snapshot)),
		stopCh:              make(chan struct{}),
		tunnelPingMs:        -1,
	}

	// 加载 GeoIP（仅真实打开的核心库且非 mock 或文件存在时加载）
	if geoPath, geoData, err := core.LocateGeoIP(); err == nil && len(geoData) > 0 {
		cli.SetGeoIP("CN", geoData)
		e.geoIPLoaded = true
		e.geoIPPath = geoPath
	}

	e.applyConfigToCoreLocked()

	// 检测当前系统代理是否已经指向本地端口
	if enabled, err := sp.IsEnabled("127.0.0.1", cfg.ProxyPort); err == nil {
		e.systemProxyOn = enabled
	}

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

	go e.statsLoop()

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
				e.emit()
			}
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

	// 退出时自动关闭系统代理，避免系统断网
	if e.sysProxy != nil {
		e.mu.RLock()
		wasOn := e.systemProxyOn
		e.mu.RUnlock()
		if wasOn {
			_ = e.sysProxy.Disable()
			e.mu.Lock()
			e.systemProxyOn = false
			e.mu.Unlock()
		}
	}

	if e.core != nil {
		e.core.Close()
	}

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
		Traffic:                traffic,
		CustomRules:            customRules,
		RuleLabels:             ruleLabels,
		ActiveRuleLabels:       activeLabels,
		UploadBytes:            e.lastUploadBytes,
		DownloadBytes:          e.lastDownloadBytes,
		UploadSpeedBps:         e.uploadSpeedBps,
		DownloadSpeedBps:       e.downloadSpeedBps,
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
	wasActive := e.state == StateConnected || e.state == StateConnecting
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
	wasActive := e.state == StateConnected || e.state == StateConnecting
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
		return err
	}

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
		_ = e.sysProxy.Enable("127.0.0.1", port)
	}

	e.emit()
	return nil
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

// AddCustomRule 新增自定义规则、无损保存并立即热重载核心规则（不断开当前隧道）。
func (e *Engine) AddCustomRule(rule config.DomainRule) (config.DomainRule, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

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

// UpdateCustomRule 更新已有自定义规则并立即热生效。
func (e *Engine) UpdateCustomRule(rule config.DomainRule) (config.DomainRule, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

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

// DeleteCustomRule 删除指定 ID 的自定义规则并立即热生效。
func (e *Engine) DeleteCustomRule(id int) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

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

// ToggleCustomRule 切换指定 ID 的自定义规则启用状态并立即热生效。
func (e *Engine) ToggleCustomRule(id int) (bool, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

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

// Connections 返回底层核心库当前活跃连接列表（按活跃时间降序）。
func (e *Engine) Connections() ([]core.ConnectionInfo, error) {
	list, err := e.core.TunnelConnections()
	if err != nil {
		return nil, err
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

// Reload 重新从磁盘读取 ~/.FlyShadow/config.json，刷新规则、代理监听与隧道。
func (e *Engine) Reload() error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.RLock()
	path := e.cfg.Path
	oldPort := e.cfg.ProxyPort
	oldLAN := e.cfg.AllowLanConnection
	oldUser := e.cfg.ProxyUsername
	oldPass := e.cfg.ProxyPassword
	wasActive := e.state == StateConnected || e.state == StateConnecting
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

	if err := e.core.StartProxy(port, allowLAN, user, pass); err != nil {
		return err
	}
	e.mu.Lock()
	e.proxyStarted = true
	e.mu.Unlock()
	return nil
}

func (e *Engine) connectCurrentLocked() error {
	e.mu.RLock()
	cfg := e.cfg
	node := cfg.CurrentNode()
	udpAcc := cfg.UDPAccelerationEnabled
	nativeUDP := cfg.NativeUDP
	e.mu.RUnlock()

	if node == nil {
		err := errors.New("没有可连接的节点")
		e.mu.Lock()
		e.state = StateError
		e.lastError = err.Error()
		e.mu.Unlock()
		e.emit()
		return err
	}

	decision := core.ResolveTransportDecision(udpAcc, nativeUDP, *node)

	e.mu.Lock()
	e.state = StateConnecting
	e.lastError = ""
	e.mu.Unlock()
	e.emit()

	// 先更新原生 UDP 选项，再建连（支持热切节点与热切 UDP/QUIC 参数）
	e.core.SetNativeUDP(decision.EnableNativeUDP)
	if err := e.core.ConnectTunnel(node.Host, node.Port, node.Password, decision.EnableQuic); err != nil {
		e.mu.Lock()
		e.state = StateError
		e.lastError = err.Error()
		e.mu.Unlock()
		e.emit()
		return err
	}

	// 轮询等待核心库确认隧道就绪（status == 0 表示已连接）
	connected := false
	for i := 0; i < 20; i++ {
		if e.core.TunnelStatus() == 0 {
			connected = true
			break
		}
		select {
		case <-e.stopCh:
			return errors.New("客户端正在退出")
		case <-time.After(200 * time.Millisecond):
		}
	}

	e.mu.Lock()
	if connected {
		e.state = StateConnected
		e.lastError = ""
		e.lastUploadBytes = e.core.TunnelUploadBytes()
		e.lastDownloadBytes = e.core.TunnelDownloadBytes()
		e.lastSampleTime = time.Now()
		e.tunnelPingMs = e.core.TunnelPingDelay()
	} else {
		e.state = StateError
		e.lastError = fmt.Sprintf("连接节点 %s 超时（状态码 %d）", node.DisplayName(), e.core.TunnelStatus())
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
	e.mu.Lock()
	active := e.state == StateConnected || e.state == StateConnecting
	e.mu.Unlock()

	if !active {
		return
	}

	status := e.core.TunnelStatus()
	up := e.core.TunnelUploadBytes()
	down := e.core.TunnelDownloadBytes()
	ping := e.core.TunnelPingDelay()

	e.mu.Lock()
	if e.state == StateConnecting && status == 0 {
		e.state = StateConnected
		e.lastError = ""
	}
	if !e.lastSampleTime.IsZero() {
		dt := now.Sub(e.lastSampleTime).Seconds()
		if dt > 0 {
			du := up - e.lastUploadBytes
			dd := down - e.lastDownloadBytes
			if du < 0 {
				du = 0
			}
			if dd < 0 {
				dd = 0
			}
			e.uploadSpeedBps = int64(float64(du) / dt)
			e.downloadSpeedBps = int64(float64(dd) / dt)
		}
	}
	e.lastUploadBytes = up
	e.lastDownloadBytes = down
	e.lastSampleTime = now
	e.tunnelPingMs = ping
	e.mu.Unlock()

	e.emit()
}
