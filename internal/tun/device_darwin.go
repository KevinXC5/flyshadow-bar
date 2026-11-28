//go:build darwin

package tun

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	utunControlName = "com.apple.net.utun_control"
	sysProtoControl = 2
	afSystem        = 32
	afSysControl    = 2
	utunOptIfName   = 2
	ctlIOCGInfo     = 0xc0644e03 // _IOWR('N', 3, struct ctl_info)

	macRouteJournalPath = "/tmp/flyshadow-bar-tun-routes.json"
)

type ctlInfo struct {
	ID   uint32
	Name [96]byte
}

type sockaddrCtl struct {
	Len      uint8
	Family   uint8
	SSSysAddr uint16
	ID       uint32
	Unit     uint32
	Reserved [5]uint32
}

// IsAdmin 返回当前进程是否具有 root 权限。
func IsAdmin() bool {
	return os.Geteuid() == 0
}

// NeedsElevationRestart 返回当前平台在非管理员态是否必须重启整个进程（macOS 走提权辅助进程，无需重启）。
func NeedsElevationRestart() bool {
	return false
}

// RelaunchAsAdmin 在 macOS 上无需重启主程序，直接返回 nil。
func RelaunchAsAdmin() error {
	return nil
}

// openPlatformDevice 在 macOS 上打开 utun 虚拟网卡并应用安全路由：
// - 若当前已具备 root 权限，直接本地打开 utun；
// - 若为普通桌面用户，则通过原生管理员授权弹窗启动内置 `fsbar tun-helper` 提权子进程，
//   由子进程管理 utun 与路由并在主进程退出/崩溃时自动看门狗回滚路由。
func openPlatformDevice(cfg DeviceConfig) (PacketDevice, error) {
	if IsAdmin() {
		return openDirectDarwinDevice(cfg)
	}
	return openHelperDarwinDevice(cfg)
}

// ---------- 直接模式（已具备 root 权限，或由 tun-helper 内部调用） ----------

type directDarwinDevice struct {
	fd     int
	name   string
	mtu    int
	routes *macRouteManager

	closed atomic.Bool
	rMu    sync.Mutex
	wMu    sync.Mutex
}

func openDirectDarwinDevice(cfg DeviceConfig) (*directDarwinDevice, error) {
	fd, name, err := openNativeUTUN()
	if err != nil {
		return nil, err
	}

	rm := newMacRouteManager()
	if err := rm.Apply(name, cfg); err != nil {
		_ = syscall.Close(fd)
		rm.Rollback()
		return nil, err
	}

	return &directDarwinDevice{
		fd:     fd,
		name:   name,
		mtu:    cfg.MTU,
		routes: rm,
	}, nil
}

func (d *directDarwinDevice) Name() string          { return d.name }
func (d *directDarwinDevice) MTU() int              { return d.mtu }
func (d *directDarwinDevice) OutboundIface() string { return d.routes.outboundIface }
func (d *directDarwinDevice) Gateway() string       { return d.routes.gateway }
func (d *directDarwinDevice) RouteCount() int       { return d.routes.Count() }

func (d *directDarwinDevice) ReadPacket(buf []byte) (int, error) {
	d.rMu.Lock()
	defer d.rMu.Unlock()
	tmp := make([]byte, len(buf)+4)
	n, err := syscall.Read(d.fd, tmp)
	if err != nil {
		return 0, err
	}
	if n <= 4 {
		return 0, nil
	}
	copy(buf, tmp[4:n])
	return n - 4, nil
}

func (d *directDarwinDevice) WritePacket(pkt []byte) (int, error) {
	if len(pkt) == 0 {
		return 0, nil
	}
	var family uint32
	switch pkt[0] >> 4 {
	case 4:
		family = syscall.AF_INET
	case 6:
		family = syscall.AF_INET6
	default:
		return 0, nil
	}
	frame := make([]byte, len(pkt)+4)
	binary.BigEndian.PutUint32(frame[0:4], family)
	copy(frame[4:], pkt)

	d.wMu.Lock()
	defer d.wMu.Unlock()
	n, err := syscall.Write(d.fd, frame)
	if err != nil {
		return 0, err
	}
	if n <= 4 {
		return 0, nil
	}
	return n - 4, nil
}

func (d *directDarwinDevice) AddBypassIP(ip string) error {
	return d.routes.AddBypassIP(ip)
}

func (d *directDarwinDevice) Close() error {
	if !d.closed.CompareAndSwap(false, true) {
		return nil
	}
	d.routes.Rollback()
	return syscall.Close(d.fd)
}

// openNativeUTUN 使用纯 Go syscall 打开 macOS 原生 utun 设备（零 CGO）。
func openNativeUTUN() (int, string, error) {
	fd, err := syscall.Socket(afSystem, syscall.SOCK_DGRAM, sysProtoControl)
	if err != nil {
		return -1, "", fmt.Errorf("创建 utun 控制套接字失败: %w", err)
	}

	var info ctlInfo
	copy(info.Name[:], utunControlName)
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(ctlIOCGInfo),
		uintptr(unsafe.Pointer(&info)),
	)
	if errno != 0 {
		_ = syscall.Close(fd)
		return -1, "", fmt.Errorf("查询 utun 内核控制 ID 失败: %w", errno)
	}

	// Unit=0 让内核自动分配下一个空闲 utunN，绝不与现有 utun0~utun6（如 Tailscale）冲突
	sc := sockaddrCtl{
		Len:       uint8(unsafe.Sizeof(sockaddrCtl{})),
		Family:    afSystem,
		SSSysAddr: afSysControl,
		ID:        info.ID,
		Unit:      0,
	}
	_, _, errno = syscall.Syscall(
		syscall.SYS_CONNECT,
		uintptr(fd),
		uintptr(unsafe.Pointer(&sc)),
		uintptr(unsafe.Sizeof(sc)),
	)
	if errno != 0 {
		_ = syscall.Close(fd)
		if errno == syscall.EPERM || errno == syscall.EACCES {
			return -1, "", fmt.Errorf("%w: %v", ErrAdminRequired, errno)
		}
		return -1, "", fmt.Errorf("绑定 utun 设备失败: %w", errno)
	}

	var nameBuf [32]byte
	nameLen := uint32(len(nameBuf))
	_, _, errno = syscall.Syscall6(
		syscall.SYS_GETSOCKOPT,
		uintptr(fd),
		uintptr(sysProtoControl),
		uintptr(utunOptIfName),
		uintptr(unsafe.Pointer(&nameBuf[0])),
		uintptr(unsafe.Pointer(&nameLen)),
		0,
	)
	if errno != 0 {
		_ = syscall.Close(fd)
		return -1, "", fmt.Errorf("获取 utun 设备名失败: %w", errno)
	}
	ifName := string(bytes.TrimRight(nameBuf[:nameLen], "\x00"))
	if ifName == "" {
		_ = syscall.Close(fd)
		return -1, "", errors.New("内核返回空的 utun 设备名")
	}
	return fd, ifName, nil
}

// ---------- macOS 安全路由管理器（仅删除自己创建的路由，绝不破坏已有 VPN/远程路由） ----------

type macOwnedRoute struct {
	Target    string `json:"target"`
	Kind      string `json:"kind"` // "net" | "host"
	Gateway   string `json:"gateway,omitempty"`
	Interface string `json:"interface,omitempty"`
}

type macRouteManager struct {
	mu            sync.Mutex
	outboundIface string
	gateway       string
	utunName      string
	owned         []macOwnedRoute
	bypassSet     map[string]struct{}
}

func newMacRouteManager() *macRouteManager {
	return &macRouteManager{
		bypassSet: make(map[string]struct{}),
	}
}

func (m *macRouteManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.owned)
}

func (m *macRouteManager) Apply(utunName string, cfg DeviceConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.utunName = utunName
	iface, gw, err := detectMacPhysicalDefaultRoute()
	if err != nil && !cfg.ScopedTestMode {
		return fmt.Errorf("探测物理默认网关失败: %w", err)
	}
	m.outboundIface = iface
	m.gateway = gw

	// 1. 启用 utun 虚拟网卡并配置 IP 与 MTU
	if out, err := runCmd("/sbin/ifconfig", utunName, "inet", cfg.TunIP, cfg.TunIP, "mtu", fmt.Sprintf("%d", cfg.MTU), "up"); err != nil {
		return fmt.Errorf("配置 %s 接口失败: %v (%s)", utunName, err, out)
	}

	// 2. 虚拟网卡自身网段路由（198.18.0.0/16 -> utunX）
	if _, ipNet, err := net.ParseCIDR(fmt.Sprintf("%s/%d", cfg.TunIP, cfg.PrefixLen)); err == nil {
		m.addIfaceRouteLocked(ipNet.String(), utunName)
	}

	// 3. 仅在全局模式添加私网与节点豁免路由（绝不覆盖已有非物理网卡路由如 Tailscale 100.64/10）
	if !cfg.ScopedTestMode && gw != "" {
		existingSpecial := detectExistingNonPhysicalRoutePrefixes()
		for _, cidr := range cfg.BypassCIDRs {
			if _, protected := existingSpecial[cidr]; protected {
				log.Printf("[tun] 保留现有特殊路由 %s（检测到已有隧道/虚拟网卡使用）", cidr)
				continue
			}
			m.addGatewayNetRouteLocked(cidr, gw)
		}
		for _, ip := range cfg.BypassIPs {
			m.addBypassIPLocked(ip)
		}
	}

	// 4. 添加接管网段路由（非破坏性分片，绝不删除系统原有 default 路由）
	for _, cidr := range cfg.TakeoverCIDRs {
		if err := m.addIfaceRouteLocked(cidr, utunName); err != nil {
			return fmt.Errorf("添加 TUN 路由 %s 失败: %w", cidr, err)
		}
	}

	m.persistJournalLocked()
	return nil
}

func (m *macRouteManager) AddBypassIP(ip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gateway == "" {
		return nil
	}
	err := m.addBypassIPLocked(ip)
	m.persistJournalLocked()
	return err
}

func (m *macRouteManager) addBypassIPLocked(ip string) error {
	v4 := net.ParseIP(strings.TrimSpace(ip)).To4()
	if v4 == nil {
		return nil
	}
	ipStr := v4.String()
	if _, exists := m.bypassSet[ipStr]; exists {
		return nil
	}
	if _, err := runCmd("/sbin/route", "-n", "add", "-host", ipStr, m.gateway); err != nil {
		// 若已有相同主机路由则尝试 change，不记为失败
		if _, err2 := runCmd("/sbin/route", "-n", "change", "-host", ipStr, m.gateway); err2 != nil {
			return err
		}
	}
	m.bypassSet[ipStr] = struct{}{}
	m.owned = append(m.owned, macOwnedRoute{
		Target:  ipStr,
		Kind:    "host",
		Gateway: m.gateway,
	})
	return nil
}

func (m *macRouteManager) addGatewayNetRouteLocked(cidr, gw string) {
	if _, err := runCmd("/sbin/route", "-n", "add", "-net", cidr, gw); err != nil {
		// 局域网直连网段（如当前 192.168.4.0/24）可能已存在更优直连路由，跳过即可
		return
	}
	m.owned = append(m.owned, macOwnedRoute{
		Target:  cidr,
		Kind:    "net",
		Gateway: gw,
	})
}

func (m *macRouteManager) addIfaceRouteLocked(cidr, iface string) error {
	out, err := runCmd("/sbin/route", "-n", "add", "-net", cidr, "-interface", iface)
	if err != nil {
		// 极少数场景下残留旧路由时改为 change
		if out2, err2 := runCmd("/sbin/route", "-n", "change", "-net", cidr, "-interface", iface); err2 != nil {
			return fmt.Errorf("%v (%s / %s)", err, out, out2)
		}
	}
	m.owned = append(m.owned, macOwnedRoute{
		Target:    cidr,
		Kind:      "net",
		Interface: iface,
	})
	return nil
}

func (m *macRouteManager) Rollback() {
	m.mu.Lock()
	owned := append([]macOwnedRoute(nil), m.owned...)
	m.owned = nil
	m.bypassSet = make(map[string]struct{})
	m.mu.Unlock()

	for i := len(owned) - 1; i >= 0; i-- {
		r := owned[i]
		switch {
		case r.Kind == "host":
			_, _ = runCmd("/sbin/route", "-n", "delete", "-host", r.Target, r.Gateway)
		case r.Interface != "":
			_, _ = runCmd("/sbin/route", "-n", "delete", "-net", r.Target, "-interface", r.Interface)
		default:
			_, _ = runCmd("/sbin/route", "-n", "delete", "-net", r.Target, r.Gateway)
		}
	}
	_ = os.Remove(macRouteJournalPath)
}

func (m *macRouteManager) persistJournalLocked() {
	data, err := json.MarshalIndent(m.owned, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(macRouteJournalPath, data, 0o600)
}

// detectMacPhysicalDefaultRoute 优先通过 route -n get default 寻找物理出口网卡与网关，
// 若默认路由已指向 utun/lo/gif，则回退扫描 netstat 找出物理网卡默认网关。
func detectMacPhysicalDefaultRoute() (iface string, gateway string, err error) {
	if out, cmdErr := runCmd("/sbin/route", "-n", "get", "default"); cmdErr == nil {
		var gw, dev string
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "gateway:") {
				gw = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
			} else if strings.HasPrefix(line, "interface:") {
				dev = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
			}
		}
		if net.ParseIP(gw).To4() != nil && dev != "" && !isVirtualMacIface(dev) {
			return dev, gw, nil
		}
	}

	out, cmdErr := runCmd("/usr/sbin/netstat", "-rn", "-f", "inet")
	if cmdErr != nil {
		return "", "", cmdErr
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "default" {
			continue
		}
		gw := fields[1]
		dev := fields[3]
		if net.ParseIP(gw).To4() != nil && !isVirtualMacIface(dev) {
			return dev, gw, nil
		}
	}
	return "", "", errors.New("未找到可用的物理 IPv4 默认网关")
}

func isVirtualMacIface(name string) bool {
	return strings.HasPrefix(name, "utun") ||
		strings.HasPrefix(name, "lo") ||
		strings.HasPrefix(name, "gif") ||
		strings.HasPrefix(name, "stf") ||
		strings.HasPrefix(name, "awdl") ||
		strings.HasPrefix(name, "llw")
}

// detectExistingNonPhysicalRoutePrefixes 检测系统当前已被其他虚拟网卡（如 Tailscale utun3）
// 占用的网段前缀，防止 TUN 豁免路由误覆写这些已有隧道的路由。
func detectExistingNonPhysicalRoutePrefixes() map[string]struct{} {
	res := make(map[string]struct{})
	out, err := runCmd("/usr/sbin/netstat", "-rn", "-f", "inet")
	if err != nil {
		return res
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		dst := fields[0]
		dev := fields[3]
		if !strings.HasPrefix(dev, "utun") && !strings.HasPrefix(dev, "bridge") {
			continue
		}
		if norm := normalizeNetstatCIDR(dst); norm != "" {
			res[norm] = struct{}{}
		}
	}
	return res
}

func normalizeNetstatCIDR(dst string) string {
	if !strings.Contains(dst, "/") {
		return ""
	}
	parts := strings.SplitN(dst, "/", 2)
	prefix := parts[1]
	octets := strings.Split(parts[0], ".")
	for len(octets) < 4 {
		octets = append(octets, "0")
	}
	candidate := strings.Join(octets, ".") + "/" + prefix
	if _, ipNet, err := net.ParseCIDR(candidate); err == nil {
		return ipNet.String()
	}
	return ""
}

func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ---------- 非 root 桌面态：通过原生授权弹窗启动内置 fsbar tun-helper ----------

type helperDarwinDevice struct {
	conn          net.Conn
	sockDir       string
	name          string
	mtu           int
	outboundIface string
	gateway       string
	routeCount    atomic.Int32

	pktCh  chan []byte
	errCh  chan error
	wMu    sync.Mutex
	closed atomic.Bool
}

func openHelperDarwinDevice(cfg DeviceConfig) (PacketDevice, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("获取程序路径失败: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exePath); rerr == nil && resolved != "" {
		exePath = resolved
	}

	sockDir, err := os.MkdirTemp("/tmp", "fsbar-tun-*")
	if err != nil {
		return nil, fmt.Errorf("创建 TUN 通信目录失败: %w", err)
	}
	_ = os.Chmod(sockDir, 0o755)
	sockPath := filepath.Join(sockDir, "tun.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		_ = os.RemoveAll(sockDir)
		return nil, fmt.Errorf("监听 TUN Unix Socket 失败: %w", err)
	}
	_ = os.Chmod(sockPath, 0o666)

	var tokBytes [24]byte
	if _, err := rand.Read(tokBytes[:]); err != nil {
		_ = ln.Close()
		_ = os.RemoveAll(sockDir)
		return nil, err
	}
	token := hex.EncodeToString(tokBytes[:])

	helperArgs := []string{
		"tun-helper",
		"--sock", sockPath,
		"--token", token,
		"--ppid", fmt.Sprintf("%d", os.Getpid()),
		"--tun-ip", cfg.TunIP,
		"--mtu", fmt.Sprintf("%d", cfg.MTU),
		"--routes", strings.Join(cfg.TakeoverCIDRs, ","),
	}
	if len(cfg.BypassIPs) > 0 {
		helperArgs = append(helperArgs, "--bypass-ips", strings.Join(cfg.BypassIPs, ","))
	}
	if cfg.ScopedTestMode {
		helperArgs = append(helperArgs, "--scoped")
	}

	// 构造带单引号安全转义的后台拉起命令，通过 osascript 弹出 macOS 系统原生管理员授权框。
	// 注意：macOS 的 `do shell script ... with administrator privileges` 无控制终端，不能使用系统自带 `nohup`
	//（否则会报错 `nohup: can't detach from console: No such process`），改为 `trap '' HUP` + 完整 stdio 重定向，
	// 并由 RunMacHelper 自身忽略 SIGHUP 与调用 setsid 脱离原会话。
	logFile := filepath.Join(sockDir, "helper.log")
	shellCmd := buildMacHelperLaunchCmd(exePath, helperArgs, logFile)
	appleScript := fmt.Sprintf(
		`do shell script %q with prompt "FlyShadow Bar 需要管理员权限以创建 TUN 虚拟网卡并配置路由（退出后自动还原）。" with administrator privileges`,
		shellCmd,
	)

	// 异步等待 helper 回连，主流程同步执行 osascript 授权弹窗（授权期间不设短超时，避免用户输入密码稍慢导致误超时）
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		c, aerr := ln.Accept()
		acceptCh <- acceptResult{conn: c, err: aerr}
	}()

	osaCmd := exec.Command("/usr/bin/osascript", "-e", appleScript)
	osaOut, osaErr := osaCmd.CombinedOutput()
	if osaErr != nil {
		_ = ln.Close()
		_ = os.RemoveAll(sockDir)
		msg := strings.TrimSpace(string(osaOut))
		if strings.Contains(msg, "User canceled") || strings.Contains(msg, "-128") {
			return nil, fmt.Errorf("%w（用户取消了系统管理员授权）", ErrAdminRequired)
		}
		return nil, fmt.Errorf("macOS 管理员授权失败: %s", msg)
	}

	// osascript 返回后 helper 已在后台拉起，正常 200ms 内即可回连；最多等待 8 秒避免异常时久卡 UI
	var res acceptResult
	select {
	case res = <-acceptCh:
	case <-time.After(8 * time.Second):
		_ = ln.Close()
		res = <-acceptCh
		if res.err == nil && res.conn != nil {
			break
		}
		helperLog, _ := os.ReadFile(logFile)
		_ = os.RemoveAll(sockDir)
		detail := strings.TrimSpace(string(helperLog))
		if detail != "" {
			return nil, fmt.Errorf("等待 TUN 辅助服务连接超时: %s", detail)
		}
		return nil, fmt.Errorf("等待 TUN 辅助服务连接超时（8 秒内未收到回连）")
	}
	_ = ln.Close()
	if res.err != nil {
		helperLog, _ := os.ReadFile(logFile)
		_ = os.RemoveAll(sockDir)
		return nil, fmt.Errorf("等待 TUN 辅助服务连接失败: %v %s", res.err, strings.TrimSpace(string(helperLog)))
	}
	conn := res.conn

	// 校验 helper 认证与就绪消息
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	typ, payload, err := readHelperFrame(conn, nil)
	if err != nil {
		_ = conn.Close()
		_ = os.RemoveAll(sockDir)
		return nil, fmt.Errorf("读取 TUN 辅助服务握手失败: %w", err)
	}
	if typ == helperFrameError {
		var em helperErrorMsg
		_ = json.Unmarshal(payload, &em)
		_ = conn.Close()
		_ = os.RemoveAll(sockDir)
		return nil, fmt.Errorf("TUN 辅助服务启动失败: %s", em.Error)
	}
	if typ != helperFrameHello {
		_ = conn.Close()
		_ = os.RemoveAll(sockDir)
		return nil, errors.New("TUN 辅助服务握手协议异常")
	}
	var hello helperHelloMsg
	if err := json.Unmarshal(payload, &hello); err != nil || hello.Token != token {
		_ = conn.Close()
		_ = os.RemoveAll(sockDir)
		return nil, errors.New("TUN 辅助服务身份令牌校验失败")
	}

	typ, payload, err = readHelperFrame(conn, nil)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		_ = os.RemoveAll(sockDir)
		return nil, fmt.Errorf("等待 TUN 设备就绪失败: %w", err)
	}
	if typ == helperFrameError {
		var em helperErrorMsg
		_ = json.Unmarshal(payload, &em)
		_ = conn.Close()
		_ = os.RemoveAll(sockDir)
		return nil, fmt.Errorf("创建虚拟网卡失败: %s", em.Error)
	}
	if typ != helperFrameReady {
		_ = conn.Close()
		_ = os.RemoveAll(sockDir)
		return nil, errors.New("TUN 辅助服务返回未知就绪帧")
	}
	var ready helperReadyMsg
	if err := json.Unmarshal(payload, &ready); err != nil {
		_ = conn.Close()
		_ = os.RemoveAll(sockDir)
		return nil, err
	}

	dev := &helperDarwinDevice{
		conn:          conn,
		sockDir:       sockDir,
		name:          ready.DeviceName,
		mtu:           ready.MTU,
		outboundIface: ready.OutboundIface,
		gateway:       ready.Gateway,
		pktCh:         make(chan []byte, 512),
		errCh:         make(chan error, 1),
	}
	dev.routeCount.Store(int32(ready.RouteCount))
	go dev.readLoop()
	return dev, nil
}

func (d *helperDarwinDevice) readLoop() {
	scratch := make([]byte, 65536)
	for {
		typ, payload, err := readHelperFrame(d.conn, scratch)
		if err != nil {
			if !d.closed.Load() {
				select {
				case d.errCh <- err:
				default:
				}
			}
			close(d.pktCh)
			return
		}
		switch typ {
		case helperFramePacket:
			pkt := make([]byte, len(payload))
			copy(pkt, payload)
			select {
			case d.pktCh <- pkt:
			default:
				// 队列满时丢弃最老或当前包避免阻塞控制帧
			}
		case helperFramePong:
		case helperFrameError:
			var em helperErrorMsg
			_ = json.Unmarshal(payload, &em)
			select {
			case d.errCh <- errors.New(em.Error):
			default:
			}
		}
	}
}

func (d *helperDarwinDevice) Name() string          { return d.name }
func (d *helperDarwinDevice) MTU() int              { return d.mtu }
func (d *helperDarwinDevice) OutboundIface() string { return d.outboundIface }
func (d *helperDarwinDevice) Gateway() string       { return d.gateway }
func (d *helperDarwinDevice) RouteCount() int       { return int(d.routeCount.Load()) }

func (d *helperDarwinDevice) ReadPacket(buf []byte) (int, error) {
	select {
	case pkt, ok := <-d.pktCh:
		if !ok {
			select {
			case err := <-d.errCh:
				return 0, err
			default:
				return 0, io.EOF
			}
		}
		n := copy(buf, pkt)
		return n, nil
	case err := <-d.errCh:
		return 0, err
	}
}

func (d *helperDarwinDevice) WritePacket(pkt []byte) (int, error) {
	if d.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	d.wMu.Lock()
	defer d.wMu.Unlock()
	if err := writeHelperFrame(d.conn, helperFramePacket, pkt); err != nil {
		return 0, err
	}
	return len(pkt), nil
}

func (d *helperDarwinDevice) AddBypassIP(ip string) error {
	if d.closed.Load() {
		return nil
	}
	d.wMu.Lock()
	defer d.wMu.Unlock()
	return writeHelperJSON(d.conn, helperFrameAddBypass, helperBypassMsg{IP: ip})
}

func (d *helperDarwinDevice) Close() error {
	if !d.closed.CompareAndSwap(false, true) {
		return nil
	}
	d.wMu.Lock()
	_ = writeHelperFrame(d.conn, helperFrameStop, nil)
	d.wMu.Unlock()
	time.Sleep(80 * time.Millisecond)
	err := d.conn.Close()
	_ = os.RemoveAll(d.sockDir)
	return err
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func buildMacHelperLaunchCmd(exePath string, helperArgs []string, logFile string) string {
	cmdParts := make([]string, 0, 1+len(helperArgs))
	cmdParts = append(cmdParts, shellSingleQuote(exePath))
	for _, a := range helperArgs {
		cmdParts = append(cmdParts, shellSingleQuote(a))
	}
	return fmt.Sprintf("trap '' HUP; %s >%s 2>&1 </dev/null &", strings.Join(cmdParts, " "), shellSingleQuote(logFile))
}

// RunMacHelper 是内置提权子命令 `fsbar tun-helper` 的执行入口：
// 负责以 root 权限打开 utun、配置安全路由、通过 Unix Socket 双向转发原始 IPv4 包，
// 并在收到停止命令、Socket 断开、信号退出或父进程死亡时 500ms 内自动回滚全部路由。
func RunMacHelper(args []string) error {
	// 忽略 SIGHUP 并尝试创建独立会话，防止 osascript 授权子 shell 退出时误杀刚拉起的辅助进程
	signal.Ignore(syscall.SIGHUP)
	_, _ = syscall.Setsid()

	fs := flag.NewFlagSet("tun-helper", flag.ContinueOnError)
	sockPath := fs.String("sock", "", "Unix socket path")
	token := fs.String("token", "", "Auth token")
	ppid := fs.Int("ppid", 0, "Parent PID to watch")
	tunIP := fs.String("tun-ip", DefaultTunIP, "TUN IPv4 address")
	mtu := fs.Int("mtu", DefaultMTU, "TUN MTU")
	routesRaw := fs.String("routes", "", "Comma-separated takeover CIDRs")
	bypassIPsRaw := fs.String("bypass-ips", "", "Comma-separated bypass IPv4s")
	scoped := fs.Bool("scoped", false, "Scoped test route mode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !IsAdmin() {
		return ErrAdminRequired
	}
	if strings.TrimSpace(*sockPath) == "" || strings.TrimSpace(*token) == "" {
		return errors.New("缺少必要的 --sock 或 --token 参数")
	}

	conn, err := net.DialTimeout("unix", *sockPath, 10*time.Second)
	if err != nil {
		return fmt.Errorf("连接主进程 Unix Socket 失败: %w", err)
	}
	defer conn.Close()

	var writeMu sync.Mutex
	sendFrame := func(typ byte, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeHelperFrame(conn, typ, data)
	}
	sendJSON := func(typ byte, v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeHelperJSON(conn, typ, v)
	}

	if err := sendJSON(helperFrameHello, helperHelloMsg{Token: *token}); err != nil {
		return err
	}

	takeoverCIDRs := ParseCIDRList(*routesRaw)
	if len(takeoverCIDRs) == 0 {
		takeoverCIDRs = DefaultTakeoverCIDRs()
	}
	var bypassCIDRs []string
	var bypassIPs []string
	if !*scoped {
		bypassCIDRs = DefaultBypassCIDRs()
		if strings.TrimSpace(*bypassIPsRaw) != "" {
			for _, ip := range strings.Split(*bypassIPsRaw, ",") {
				if v4 := net.ParseIP(strings.TrimSpace(ip)).To4(); v4 != nil {
					bypassIPs = append(bypassIPs, v4.String())
				}
			}
		}
	}

	dev, err := openDirectDarwinDevice(DeviceConfig{
		TunIP:          *tunIP,
		PrefixLen:      DefaultTunPrefix,
		MTU:            *mtu,
		TakeoverCIDRs:  takeoverCIDRs,
		BypassCIDRs:    bypassCIDRs,
		BypassIPs:      bypassIPs,
		ScopedTestMode: *scoped,
	})
	if err != nil {
		_ = sendJSON(helperFrameError, helperErrorMsg{Error: err.Error()})
		return err
	}
	defer dev.Close()

	if err := sendJSON(helperFrameReady, helperReadyMsg{
		DeviceName:    dev.Name(),
		MTU:           dev.MTU(),
		OutboundIface: dev.OutboundIface(),
		Gateway:       dev.Gateway(),
		RouteCount:    dev.RouteCount(),
	}); err != nil {
		return err
	}

	stopCh := make(chan struct{})
	var stopOnce sync.Once
	triggerStop := func() {
		stopOnce.Do(func() {
			close(stopCh)
			_ = dev.Close()
			_ = conn.Close()
		})
	}

	// 信号退出自动还原路由（不监听 SIGHUP，避免授权父 shell 退出时误触发停止）
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			triggerStop()
		case <-stopCh:
		}
	}()

	// 父进程存活看门狗：主进程一旦崩溃或被 kill -9，500ms 内立刻自动回滚全部路由并销毁 utun
	if *ppid > 1 {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-ticker.C:
					if err := syscall.Kill(*ppid, 0); err != nil && errors.Is(err, syscall.ESRCH) {
						triggerStop()
						return
					}
				}
			}
		}()
	}

	// utun -> 主进程
	go func() {
		buf := make([]byte, max(dev.MTU()+64, 65536))
		for {
			n, rerr := dev.ReadPacket(buf)
			if rerr != nil {
				triggerStop()
				return
			}
			if n <= 0 {
				continue
			}
			if werr := sendFrame(helperFramePacket, buf[:n]); werr != nil {
				triggerStop()
				return
			}
		}
	}()

	// 主进程 -> utun + 控制帧
	scratch := make([]byte, 65536)
	for {
		typ, payload, rerr := readHelperFrame(conn, scratch)
		if rerr != nil {
			triggerStop()
			return nil
		}
		switch typ {
		case helperFramePacket:
			if len(payload) > 0 {
				_, _ = dev.WritePacket(payload)
			}
		case helperFrameAddBypass:
			var bm helperBypassMsg
			if json.Unmarshal(payload, &bm) == nil && bm.IP != "" {
				_ = dev.AddBypassIP(bm.IP)
			}
		case helperFramePing:
			_ = sendFrame(helperFramePong, nil)
		case helperFrameStop:
			triggerStop()
			return nil
		}
	}
}
