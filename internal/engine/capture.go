package engine

import (
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/capture"
	"github.com/KevinXC5/flyshadow-bar/internal/config"
)

// CaptureStatus 描述抓包前置代理与白名单 HTTPS MITM 的当前运行状态。
type CaptureStatus struct {
	Enabled          bool               `json:"enabled"`
	Paused           bool               `json:"paused"`
	ProxyPort        int                `json:"proxyPort"`
	CoreInternalPort int                `json:"coreInternalPort,omitempty"`
	MITMEnabled      bool               `json:"mitmEnabled"`
	MITMHosts        []string           `json:"mitmHosts,omitempty"`
	Cert             capture.CertStatus `json:"cert"`
	EntryCount       int                `json:"entryCount"`
}

// CaptureStore 返回内存抓包记录仓库，供独立抓包分析窗口订阅与展示。
func (e *Engine) CaptureStore() *capture.Store {
	return e.captureStore
}

// CaptureStatus 返回当前抓包代理、白名单配置与 CA 信任状态的快照。
func (e *Engine) CaptureStatus() CaptureStatus {
	e.mu.RLock()
	enabled := e.captureEnabled
	port := e.cfg.ProxyPort
	corePort := e.captureCorePort
	mitmEnabled := e.mitmEnabled
	mitmHosts := append([]string(nil), e.mitmHosts...)
	certCached := e.certStatusCached
	certStatus := e.certStatus
	e.mu.RUnlock()

	if !certCached && e.captureCA != nil {
		certStatus = e.captureCA.Status()
		e.mu.Lock()
		e.certStatus = certStatus
		e.certStatusCached = true
		e.mu.Unlock()
	}

	return CaptureStatus{
		Enabled:          enabled,
		Paused:           e.captureStore != nil && e.captureStore.Paused(),
		ProxyPort:        port,
		CoreInternalPort: corePort,
		MITMEnabled:      mitmEnabled,
		MITMHosts:        mitmHosts,
		Cert:             certStatus,
		EntryCount:       e.captureStore.Len(),
	}
}

// RefreshMITMCertStatus 主动重新检测系统根证书信任状态并缓存结果。
func (e *Engine) RefreshMITMCertStatus() capture.CertStatus {
	if e.captureCA == nil {
		return capture.CertStatus{}
	}
	st := e.captureCA.Status()
	e.mu.Lock()
	e.certStatus = st
	e.certStatusCached = true
	e.mu.Unlock()
	e.emit()
	return st
}

// InstallMITMCA 生成（若尚未生成）并调用系统原生授权一键安装与信任本机专属自签名 CA。
func (e *Engine) InstallMITMCA() (capture.CertStatus, error) {
	if e.captureCA == nil {
		return capture.CertStatus{}, fmt.Errorf("证书管理器未初始化")
	}
	st, err := e.captureCA.InstallAndTrust()
	e.mu.Lock()
	e.certStatus = st
	e.certStatusCached = true
	e.mu.Unlock()
	if err == nil {
		log.Printf("[capture] 已安装并信任本机 MITM 根证书: %s", st.CertPath)
	} else {
		log.Printf("[capture] 安装本机 MITM 根证书失败: %v", err)
	}
	e.emit()
	return st, err
}

// UninstallMITMCA 从系统受信任根证书存储卸载当前本机 CA。
func (e *Engine) UninstallMITMCA() (capture.CertStatus, error) {
	if e.captureCA == nil {
		return capture.CertStatus{}, fmt.Errorf("证书管理器未初始化")
	}
	st, err := e.captureCA.Uninstall()
	e.mu.Lock()
	e.certStatus = st
	e.certStatusCached = true
	e.mu.Unlock()
	if err == nil {
		log.Printf("[capture] 已从系统根证书存储卸载本机 MITM 根证书")
	} else {
		log.Printf("[capture] 卸载本机 MITM 根证书失败: %v", err)
	}
	e.emit()
	return st, err
}

// SetCapturePaused 暂停或恢复向内存缓冲区追加抓包记录（不中断代理转发）。
func (e *Engine) SetCapturePaused(paused bool) {
	if e.captureStore != nil {
		e.captureStore.SetPaused(paused)
	}
}

// ClearCapture 清空内存中的所有抓包记录。
func (e *Engine) ClearCapture() {
	if e.captureStore != nil {
		e.captureStore.Clear()
	}
}

// SetMITMEnabled 开关白名单 HTTPS 解密模式并持久化到偏好文件。
func (e *Engine) SetMITMEnabled(enabled bool) error {
	if enabled && e.captureCA != nil {
		if err := e.captureCA.EnsureCA(); err != nil {
			return fmt.Errorf("准备本机 CA 证书失败: %w", err)
		}
	}

	e.mu.Lock()
	e.mitmEnabled = enabled
	hosts := append([]string(nil), e.mitmHosts...)
	proxy := e.captureProxy
	prefsPath := e.prefsPath
	e.certStatusCached = false
	e.mu.Unlock()

	if proxy != nil {
		proxy.UpdateMITMConfig(enabled, hosts)
	}
	if prefsPath != "" {
		if _, err := config.UpdateAppPrefs(prefsPath, func(p *config.AppPrefs) {
			p.CaptureMITMEnabled = enabled
			p.CaptureMITMHosts = hosts
		}); err != nil {
			log.Printf("[capture] 保存 MITM 开关偏好失败: %v", err)
		}
	}
	e.emit()
	return nil
}

// AddMITMHost 将单个域名或通配规则（如 github.com、*.openai.com）加入 HTTPS 解密白名单。
func (e *Engine) AddMITMHost(pattern string) ([]string, error) {
	norm := capture.NormalizeHostPattern(pattern)
	if norm == "" {
		return nil, fmt.Errorf("请输入有效的域名或通配规则（如 github.com、*.openai.com）")
	}

	e.mu.Lock()
	next := capture.NormalizeHostPatterns(append(append([]string(nil), e.mitmHosts...), norm))
	e.mitmHosts = next
	enabled := e.mitmEnabled
	proxy := e.captureProxy
	prefsPath := e.prefsPath
	e.mu.Unlock()

	if proxy != nil {
		proxy.UpdateMITMConfig(enabled, next)
	}
	if prefsPath != "" {
		if _, err := config.UpdateAppPrefs(prefsPath, func(p *config.AppPrefs) {
			p.CaptureMITMHosts = next
		}); err != nil {
			return next, fmt.Errorf("保存白名单失败: %w", err)
		}
	}
	e.emit()
	return next, nil
}

// RemoveMITMHost 从 HTTPS 解密白名单移除指定规则。
func (e *Engine) RemoveMITMHost(pattern string) ([]string, error) {
	norm := capture.NormalizeHostPattern(pattern)
	e.mu.Lock()
	next := make([]string, 0, len(e.mitmHosts))
	for _, h := range e.mitmHosts {
		if h != norm {
			next = append(next, h)
		}
	}
	e.mitmHosts = next
	enabled := e.mitmEnabled
	proxy := e.captureProxy
	prefsPath := e.prefsPath
	e.mu.Unlock()

	if proxy != nil {
		proxy.UpdateMITMConfig(enabled, next)
	}
	if prefsPath != "" {
		if _, err := config.UpdateAppPrefs(prefsPath, func(p *config.AppPrefs) {
			p.CaptureMITMHosts = next
		}); err != nil {
			return next, fmt.Errorf("保存白名单失败: %w", err)
		}
	}
	e.emit()
	return next, nil
}

// SetCaptureEnabled 热切换 Go 前置透明抓包代理：
//   - 开启时：将底层核心库代理切至空闲内部回环端口，由 Go 抓包代理接管用户代理端口（如 7890）；
//   - 关闭时：停止 Go 抓包代理并还原核心库直接监听用户代理端口，平时零额外开销。
func (e *Engine) SetCaptureEnabled(enabled bool) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.RLock()
	cur := e.captureEnabled
	e.mu.RUnlock()
	if cur == enabled {
		return nil
	}

	if enabled {
		if err := e.startCaptureFrontProxyLocked(); err != nil {
			return err
		}
	} else {
		if err := e.stopCaptureFrontProxyLocked(); err != nil {
			return err
		}
	}
	e.emit()
	return nil
}

func (e *Engine) startCaptureFrontProxyLocked() error {
	e.mu.RLock()
	userPort := e.cfg.ProxyPort
	allowLAN := e.cfg.AllowLanConnection
	user := e.cfg.ProxyUsername
	pass := e.cfg.ProxyPassword
	mitmEnabled := e.mitmEnabled
	mitmHosts := append([]string(nil), e.mitmHosts...)
	oldProxy := e.captureProxy
	e.mu.RUnlock()

	if oldProxy != nil {
		_ = oldProxy.Close()
		e.mu.Lock()
		e.captureProxy = nil
		e.mu.Unlock()
	}

	corePort, err := pickFreeLoopbackPort(userPort)
	if err != nil {
		return fmt.Errorf("分配内部上游端口失败: %w", err)
	}

	// 1. 让底层核心库切换到内部回环端口并释放用户代理端口
	if err := e.core.StartProxy(corePort, false, "", ""); err != nil {
		return fmt.Errorf("切换底层代理至内部端口失败: %w", err)
	}

	listenHost := "127.0.0.1"
	if allowLAN {
		listenHost = "0.0.0.0"
	}
	listenAddr := fmt.Sprintf("%s:%d", listenHost, userPort)
	upstreamAddr := fmt.Sprintf("127.0.0.1:%d", corePort)

	// 2. 在用户代理端口启动 Go 前置抓包代理（短重试等待核心库释放原监听套接字）
	var front *capture.Proxy
	for attempt := 0; attempt < 10; attempt++ {
		front, err = capture.StartProxy(capture.ProxyOptions{
			ListenAddr:     listenAddr,
			UpstreamAddr:   upstreamAddr,
			Username:       user,
			Password:       pass,
			Store:          e.captureStore,
			CA:             e.captureCA,
			MITMEnabled:    mitmEnabled,
			MITMHosts:      mitmHosts,
			ResolveProcess: func(addr string) string { return e.resolveCaptureClientProcess(addr, userPort) },
		})
		if err == nil {
			break
		}
		select {
		case <-e.stopCh:
			_ = e.core.StartProxy(userPort, allowLAN, user, pass)
			return err
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		// 启动前置抓包代理失败时立刻回滚核心库到用户原端口，绝不导致断网
		_ = e.core.StartProxy(userPort, allowLAN, user, pass)
		return fmt.Errorf("启动前置抓包代理失败 (%s): %w", listenAddr, err)
	}

	e.mu.Lock()
	e.captureProxy = front
	e.captureEnabled = true
	e.captureCorePort = corePort
	e.proxyStarted = true
	e.mu.Unlock()

	log.Printf("[capture] 前置抓包代理已启用: listen=%s upstream=%s mitm=%v hosts=%d",
		listenAddr, upstreamAddr, mitmEnabled, len(mitmHosts))
	return nil
}

func (e *Engine) stopCaptureFrontProxyLocked() error {
	e.mu.Lock()
	front := e.captureProxy
	e.captureProxy = nil
	e.captureEnabled = false
	e.captureCorePort = 0
	userPort := e.cfg.ProxyPort
	allowLAN := e.cfg.AllowLanConnection
	user := e.cfg.ProxyUsername
	pass := e.cfg.ProxyPassword
	e.mu.Unlock()

	if front != nil {
		_ = front.Close()
	}

	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if err = e.core.StartProxy(userPort, allowLAN, user, pass); err == nil {
			e.mu.Lock()
			e.proxyStarted = true
			e.mu.Unlock()
			log.Printf("[capture] 前置抓包代理已关闭，已恢复核心代理直连监听: port=%d", userPort)
			return nil
		}
		select {
		case <-e.stopCh:
			return err
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("恢复核心代理端口失败 (%d): %w", userPort, err)
}

func (e *Engine) resolveCaptureClientProcess(clientAddr string, listenPort int) string {
	key := normalizeEndpoint(clientAddr)
	if key == "" {
		return ""
	}
	now := time.Now()
	e.captureProcMu.Lock()
	if e.captureProcMap != nil {
		if name, ok := e.captureProcMap[key]; ok && now.Sub(e.captureProcAt) < 2*time.Second {
			e.captureProcMu.Unlock()
			return name
		}
	}
	needRefresh := now.Sub(e.captureProcAt) >= 1200*time.Millisecond
	lookupFn := e.procLookupFn
	e.captureProcMu.Unlock()

	if !needRefresh || lookupFn == nil || listenPort <= 0 {
		e.captureProcMu.Lock()
		name := e.captureProcMap[key]
		e.captureProcMu.Unlock()
		return name
	}

	resolved := lookupFn(listenPort)
	e.captureProcMu.Lock()
	if resolved != nil {
		e.captureProcMap = resolved
	}
	e.captureProcAt = time.Now()
	name := e.captureProcMap[key]
	e.captureProcMu.Unlock()
	return strings.TrimSpace(name)
}

func pickFreeLoopbackPort(avoid int) (int, error) {
	for i := 0; i < 8; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if port > 0 && port != avoid {
			return port, nil
		}
	}
	return 0, fmt.Errorf("无法找到可用本地回环端口")
}
