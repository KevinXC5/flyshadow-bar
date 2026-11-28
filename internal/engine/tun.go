package engine

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/KevinXC5/flyshadow-bar/internal/tun"
)

// TunNeedsElevationRestart 返回当前平台在开启 TUN 前是否需要先以管理员权限重启进程（Windows 非管理员态）。
func (e *Engine) TunNeedsElevationRestart() bool {
	return tun.NeedsElevationRestart()
}

// RelaunchAsAdmin 请求操作系统以管理员身份重启当前应用（仅 Windows 生效）。
func (e *Engine) RelaunchAsAdmin() error {
	return tun.RelaunchAsAdmin()
}

// TunStatus 返回当前 TUN 虚拟网卡运行状态快照。
func (e *Engine) TunStatus() tun.Status {
	e.mu.RLock()
	mgr := e.tunMgr
	lastErr := e.tunLastError
	e.mu.RUnlock()
	if mgr != nil {
		st := mgr.Status()
		if st.LastError == "" {
			st.LastError = lastErr
		}
		return st
	}
	return tun.Status{LastError: lastErr}
}

// SetTunEnabled 开启或关闭 TUN 虚拟网卡模式，并无损同步 config.json 的 tunModelEnable 字段。
// 开启后全机流量先经纯 Go 虚拟网卡与协议栈还原，再通过本地回环混合代理端口转发：
//   - 未开抓包时：直达 FlyShadow 核心库，零额外抓包开销；
//   - 已开抓包时：自动穿透 Go 前置透明抓包代理，实现全机 HTTP / 白名单 HTTPS MITM / UDP 抓包。
func (e *Engine) SetTunEnabled(enabled bool) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	if enabled {
		return e.enableTunLocked(true)
	}
	return e.disableTunLocked(true)
}

func (e *Engine) enableTunLocked(persist bool) error {
	e.mu.RLock()
	if e.tunMgr != nil && e.tunMgr.Status().Active {
		e.mu.RUnlock()
		return nil
	}
	port := e.cfg.ProxyPort
	allowLAN := e.cfg.AllowLanConnection
	user := e.cfg.ProxyUsername
	pass := e.cfg.ProxyPassword
	proxyStarted := e.proxyStarted
	openDev := e.tunOpenDevice
	e.mu.RUnlock()

	e.mu.Lock()
	e.tunStarting = true
	e.tunLastError = ""
	e.mu.Unlock()
	e.emit()

	defer func() {
		e.mu.Lock()
		e.tunStarting = false
		e.mu.Unlock()
		e.emit()
	}()

	// 1. 确保本地混合代理端口已就绪
	if !proxyStarted {
		if err := e.core.StartProxy(port, allowLAN, user, pass); err != nil {
			e.setTunError(fmt.Errorf("启动本地代理端口失败: %w", err))
			return err
		}
		e.mu.Lock()
		e.proxyStarted = true
		e.mu.Unlock()
	}

	// 2. 通知 FlyShadow 核心库启用 TUN 出站绑定（自动通过物理网卡出站，彻底杜绝直连与隧道回环）
	e.core.SetTunModeEnable(true)

	mgr, err := tun.Start(tun.Options{
		ProxyAddr: func() string {
			e.mu.RLock()
			p := e.cfg.ProxyPort
			e.mu.RUnlock()
			return fmt.Sprintf("127.0.0.1:%d", p)
		},
		ProxyAuth: func() (string, string) {
			e.mu.RLock()
			u, pw := e.cfg.ProxyUsername, e.cfg.ProxyPassword
			e.mu.RUnlock()
			return u, pw
		},
		BypassHosts: func() []string {
			return e.collectNodeBypassHosts()
		},
		OpenDevice: openDev,
		OnFatal: func(ferr error) {
			e.handleTunFatal(ferr)
		},
	})
	if err != nil {
		e.core.SetTunModeEnable(false)
		e.setTunError(err)
		return err
	}

	e.mu.Lock()
	e.tunMgr = mgr
	e.tunLastError = ""
	if persist {
		e.cfg.TunModelEnable = true
		if saveErr := e.cfg.Save(); saveErr != nil {
			log.Printf("[tun] 保存 tunModelEnable=true 失败: %v", saveErr)
		}
	}
	e.mu.Unlock()

	st := mgr.Status()
	log.Printf("[tun] TUN 模式已开启: dev=%s ip=%s out=%s gw=%s routes=%d",
		st.DeviceName, st.TunIP, st.OutboundIface, st.Gateway, st.RouteCount)
	return nil
}

func (e *Engine) disableTunLocked(persist bool) error {
	e.mu.Lock()
	mgr := e.tunMgr
	e.tunMgr = nil
	e.tunStarting = false
	e.tunLastError = ""
	if persist {
		e.cfg.TunModelEnable = false
		if saveErr := e.cfg.Save(); saveErr != nil {
			log.Printf("[tun] 保存 tunModelEnable=false 失败: %v", saveErr)
		}
	}
	e.mu.Unlock()

	var closeErr error
	if mgr != nil {
		closeErr = mgr.Close()
	}
	e.core.SetTunModeEnable(false)
	e.emit()
	if closeErr != nil {
		return fmt.Errorf("关闭 TUN 虚拟网卡时发生异常: %w", closeErr)
	}
	log.Printf("[tun] TUN 模式已关闭")
	return nil
}

// stopTunForShutdownLocked 在程序退出时释放 TUN 虚拟网卡与系统路由（不篡改用户持久化的 tunModelEnable 偏好）。
func (e *Engine) stopTunForShutdownLocked() {
	e.mu.Lock()
	mgr := e.tunMgr
	e.tunMgr = nil
	e.tunStarting = false
	e.mu.Unlock()

	if mgr != nil {
		if err := mgr.Close(); err != nil {
			log.Printf("[tun] 退出清理 TUN 虚拟网卡失败: %v", err)
		} else {
			log.Printf("[tun] 退出已自动关闭 TUN 虚拟网卡并还原路由")
		}
	}
	if e.core != nil {
		e.core.SetTunModeEnable(false)
	}
}

func (e *Engine) handleTunFatal(err error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	mgr := e.tunMgr
	e.tunMgr = nil
	e.tunStarting = false
	if err != nil {
		e.tunLastError = err.Error()
	} else {
		e.tunLastError = "TUN 虚拟网卡意外断开"
	}
	e.mu.Unlock()

	if mgr != nil {
		_ = mgr.Close()
	}
	if e.core != nil {
		e.core.SetTunModeEnable(false)
	}
	log.Printf("[tun] 检测到 TUN 虚拟网卡异常断开并已自动清理: %v", err)
	e.emit()
}

func (e *Engine) setTunError(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	e.tunLastError = err.Error()
	e.mu.Unlock()
	log.Printf("[tun] 开启 TUN 模式失败: %v", err)
}

func (e *Engine) collectNodeBypassHosts() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var hosts []string
	if cur := e.cfg.CurrentNode(); cur != nil && strings.TrimSpace(cur.Host) != "" {
		hosts = append(hosts, cur.Host)
	}
	// 同时把订阅中其余节点主机名提前豁免（最多 32 个），确保 TUN 运行期间测速或切节点时直走物理网关
	for i, n := range e.cfg.Nodes() {
		if i >= 32 {
			break
		}
		if h := strings.TrimSpace(n.Host); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func (e *Engine) syncTunBypassForHost(host string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	e.mu.RLock()
	mgr := e.tunMgr
	e.mu.RUnlock()
	if mgr != nil {
		mgr.AddBypassHosts([]string{host})
	}
}

// IsTunAdminRequiredError 判断错误是否属于“需要管理员权限”。
func IsTunAdminRequiredError(err error) bool {
	return errors.Is(err, tun.ErrAdminRequired)
}
