//go:build windows

package tun

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/KevinXC5/flyshadow-bar/internal/core"
)

const (
	wintunRingCapacity = 0x400000 // 4 MiB 环形缓冲区
)

var (
	modShell32      = windows.NewLazySystemDLL("shell32.dll")
	procShellExecW  = modShell32.NewProc("ShellExecuteW")
	modIphlpapi     = windows.NewLazySystemDLL("iphlpapi.dll")
	procLuidToIndex = modIphlpapi.NewProc("ConvertInterfaceLuidToIndex")
)

// IsAdmin 返回当前 Windows 进程是否以管理员权限（UAC 提权）运行。
func IsAdmin() bool {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}

// NeedsElevationRestart 返回 Windows 非管理员态需要触发 UAC 以管理员身份重启。
func NeedsElevationRestart() bool {
	return !IsAdmin()
}

// RelaunchAsAdmin 使用 ShellExecuteW("runas") 弹出 UAC 框以管理员权限重新启动当前应用。
func RelaunchAsAdmin() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取程序路径失败: %w", err)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString("--show")
	r, _, callErr := procShellExecW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0,
		uintptr(windows.SW_SHOWNORMAL),
	)
	if r <= 32 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return fmt.Errorf("请求管理员权限失败: %w", callErr)
		}
		return errors.New("用户取消或拒绝了 Windows UAC 管理员授权")
	}
	return nil
}

// RunMacHelper 仅在 macOS 上使用。
func RunMacHelper([]string) error {
	return ErrUnsupported
}

type winOwnedRoute struct {
	Network string
	Mask    string
	Gateway string
	IfIndex uint32
}

type windowsTunDevice struct {
	dll             *windows.DLL
	pCloseAdapter   *windows.Proc
	pEndSession     *windows.Proc
	pReadWaitEvent  *windows.Proc
	pReceivePacket  *windows.Proc
	pReleasePacket  *windows.Proc
	pAllocSendPkt   *windows.Proc
	pSendPacket     *windows.Proc
	pGetAdapterLUID *windows.Proc

	adapter   uintptr
	session   uintptr
	readEvent windows.Handle
	stopEvent windows.Handle

	name          string
	mtu           int
	ifIndex       uint32
	outboundIface string
	gateway       string
	outboundIdx   uint32

	routeMu   sync.Mutex
	owned     []winOwnedRoute
	bypassSet map[string]struct{}

	wMu    sync.Mutex
	closed atomic.Bool
}

func openPlatformDevice(cfg DeviceConfig) (PacketDevice, error) {
	if !IsAdmin() {
		return nil, fmt.Errorf("%w（请以管理员身份运行 FlyShadow Bar 后再开启 TUN 模式）", ErrAdminRequired)
	}

	dllPath := strings.TrimSpace(cfg.WinTunDLLPath)
	if dllPath == "" {
		var err error
		dllPath, err = core.LocateWinTunDLL()
		if err != nil {
			return nil, err
		}
	}

	dll, err := windows.LoadDLL(dllPath)
	if err != nil {
		return nil, fmt.Errorf("加载 wintun.dll 失败 (%s): %w", dllPath, err)
	}

	find := func(name string) (*windows.Proc, error) {
		p, ferr := dll.FindProc(name)
		if ferr != nil {
			return nil, fmt.Errorf("wintun.dll 缺少导出函数 %s: %w", name, ferr)
		}
		return p, nil
	}

	pCreateAdapter, err := find("WintunCreateAdapter")
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	pCloseAdapter, err := find("WintunCloseAdapter")
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	pStartSession, err := find("WintunStartSession")
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	pEndSession, err := find("WintunEndSession")
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	pReadWaitEvent, err := find("WintunGetReadWaitEvent")
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	pReceivePacket, err := find("WintunReceivePacket")
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	pReleasePacket, err := find("WintunReleaseReceivePacket")
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	pAllocSendPkt, err := find("WintunAllocateSendPacket")
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	pSendPacket, err := find("WintunSendPacket")
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	pGetAdapterLUID, err := find("WintunGetAdapterLUID")
	if err != nil {
		_ = dll.Release()
		return nil, err
	}

	adapterName := cfg.AdapterName
	if strings.TrimSpace(adapterName) == "" {
		adapterName = "FlyShadow Bar TUN"
	}
	namePtr, _ := windows.UTF16PtrFromString(adapterName)
	typePtr, _ := windows.UTF16PtrFromString("FlyShadow")

	// 使用固定 GUID 确保重启后复用同一网卡标识而不堆积虚拟网卡
	guid := windows.GUID{
		Data1: 0x5f4e3d2c,
		Data2: 0x7b6a,
		Data3: 0x4988,
		Data4: [8]byte{0x91, 0x22, 0x66, 0x6c, 0x79, 0x73, 0x68, 0x01},
	}

	adapter, _, callErr := pCreateAdapter.Call(
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(typePtr)),
		uintptr(unsafe.Pointer(&guid)),
	)
	if adapter == 0 {
		_ = dll.Release()
		return nil, fmt.Errorf("创建 Wintun 虚拟网卡失败: %w", callErr)
	}

	var luid uint64
	pGetAdapterLUID.Call(adapter, uintptr(unsafe.Pointer(&luid)))
	var ifIdx uint32
	if r, _, _ := procLuidToIndex.Call(uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&ifIdx))); r != 0 || ifIdx == 0 {
		pCloseAdapter.Call(adapter)
		_ = dll.Release()
		return nil, fmt.Errorf("查询 Wintun 接口索引失败 (code=%d)", r)
	}

	session, _, callErr := pStartSession.Call(adapter, uintptr(wintunRingCapacity))
	if session == 0 {
		pCloseAdapter.Call(adapter)
		_ = dll.Release()
		return nil, fmt.Errorf("启动 Wintun 会话失败: %w", callErr)
	}

	readEvent, _, _ := pReadWaitEvent.Call(session)
	stopEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		pEndSession.Call(session)
		pCloseAdapter.Call(adapter)
		_ = dll.Release()
		return nil, fmt.Errorf("创建停止事件失败: %w", err)
	}

	dev := &windowsTunDevice{
		dll:             dll,
		pCloseAdapter:   pCloseAdapter,
		pEndSession:     pEndSession,
		pReadWaitEvent:  pReadWaitEvent,
		pReceivePacket:  pReceivePacket,
		pReleasePacket:  pReleasePacket,
		pAllocSendPkt:   pAllocSendPkt,
		pSendPacket:     pSendPacket,
		pGetAdapterLUID: pGetAdapterLUID,
		adapter:         adapter,
		session:         session,
		readEvent:       windows.Handle(readEvent),
		stopEvent:       stopEvent,
		name:            adapterName,
		mtu:             cfg.MTU,
		ifIndex:         ifIdx,
		bypassSet:       make(map[string]struct{}),
	}

	if err := dev.applyRoutes(cfg); err != nil {
		_ = dev.Close()
		return nil, err
	}
	return dev, nil
}

func (d *windowsTunDevice) applyRoutes(cfg DeviceConfig) error {
	gw, outIdx, outName, err := detectWindowsPhysicalDefaultRoute(d.ifIndex)
	if err != nil && !cfg.ScopedTestMode {
		return fmt.Errorf("探测 Windows 物理默认网关失败: %w", err)
	}
	d.gateway = gw
	d.outboundIdx = outIdx
	d.outboundIface = outName

	// 1. 配置 Wintun 虚拟网卡 IPv4 地址、子网掩码与 MTU
	_, mask, _, err := CIDRToNetworkMask(fmt.Sprintf("%s/%d", cfg.TunIP, cfg.PrefixLen))
	if err != nil {
		return err
	}
	if out, err := runHiddenCmd("netsh", "interface", "ipv4", "set", "address",
		fmt.Sprintf("name=%d", d.ifIndex), "source=static",
		fmt.Sprintf("address=%s", cfg.TunIP), fmt.Sprintf("mask=%s", mask)); err != nil {
		return fmt.Errorf("配置 Wintun IPv4 地址失败: %v (%s)", err, out)
	}
	_, _ = runHiddenCmd("netsh", "interface", "ipv4", "set", "subinterface",
		fmt.Sprintf("%d", d.ifIndex), fmt.Sprintf("mtu=%d", cfg.MTU), "store=active")

	// 2. 仅在全局模式添加私网、CGNAT 与远程节点豁免路由
	d.routeMu.Lock()
	defer d.routeMu.Unlock()
	if !cfg.ScopedTestMode && d.gateway != "" && d.outboundIdx > 0 {
		for _, cidr := range cfg.BypassCIDRs {
			if netAddr, netMask, _, cerr := CIDRToNetworkMask(cidr); cerr == nil {
				d.addWinRouteLocked(netAddr, netMask, d.gateway, d.outboundIdx, 5)
			}
		}
		for _, ip := range cfg.BypassIPs {
			d.addBypassIPLocked(ip)
		}
	}

	// 3. 添加分片接管路由到 Wintun 接口（保留原 0.0.0.0 默认路由）
	for _, cidr := range cfg.TakeoverCIDRs {
		netAddr, netMask, _, cerr := CIDRToNetworkMask(cidr)
		if cerr != nil {
			continue
		}
		if err := d.addWinRouteStrictLocked(netAddr, netMask, "0.0.0.0", d.ifIndex, 5); err != nil {
			return fmt.Errorf("添加 Windows TUN 路由 %s 失败: %w", cidr, err)
		}
	}
	return nil
}

func (d *windowsTunDevice) addWinRouteStrictLocked(netAddr, netMask, gw string, ifIdx uint32, metric int) error {
	out, err := runHiddenCmd("route", "add", netAddr, "mask", netMask, gw,
		"metric", strconv.Itoa(metric), "if", strconv.FormatUint(uint64(ifIdx), 10))
	if err != nil {
		return fmt.Errorf("%v (%s)", err, out)
	}
	d.owned = append(d.owned, winOwnedRoute{
		Network: netAddr,
		Mask:    netMask,
		Gateway: gw,
		IfIndex: ifIdx,
	})
	return nil
}

func (d *windowsTunDevice) addWinRouteLocked(netAddr, netMask, gw string, ifIdx uint32, metric int) {
	if err := d.addWinRouteStrictLocked(netAddr, netMask, gw, ifIdx, metric); err != nil {
		log.Printf("[tun] 跳过已存在或无需设置的 Windows 路由 %s/%s: %v", netAddr, netMask, err)
	}
}

func (d *windowsTunDevice) addBypassIPLocked(ip string) {
	v4 := net.ParseIP(strings.TrimSpace(ip)).To4()
	if v4 == nil || d.gateway == "" || d.outboundIdx == 0 {
		return
	}
	ipStr := v4.String()
	if _, exists := d.bypassSet[ipStr]; exists {
		return
	}
	if err := d.addWinRouteStrictLocked(ipStr, "255.255.255.255", d.gateway, d.outboundIdx, 1); err == nil {
		d.bypassSet[ipStr] = struct{}{}
	}
}

func (d *windowsTunDevice) Name() string          { return d.name }
func (d *windowsTunDevice) MTU() int              { return d.mtu }
func (d *windowsTunDevice) OutboundIface() string { return d.outboundIface }
func (d *windowsTunDevice) Gateway() string       { return d.gateway }

func (d *windowsTunDevice) RouteCount() int {
	d.routeMu.Lock()
	defer d.routeMu.Unlock()
	return len(d.owned)
}

func (d *windowsTunDevice) AddBypassIP(ip string) error {
	d.routeMu.Lock()
	defer d.routeMu.Unlock()
	d.addBypassIPLocked(ip)
	return nil
}

func (d *windowsTunDevice) ReadPacket(buf []byte) (int, error) {
	handles := []windows.Handle{d.stopEvent, d.readEvent}
	for {
		if d.closed.Load() {
			return 0, errors.New("wintun 已关闭")
		}
		var size uint32
		pktPtr, _, callErr := d.pReceivePacket.Call(d.session, uintptr(unsafe.Pointer(&size)))
		if pktPtr != 0 {
			base := *(**byte)(unsafe.Pointer(&pktPtr))
			src := unsafe.Slice(base, int(size))
			n := copy(buf, src)
			d.pReleasePacket.Call(d.session, pktPtr)
			return n, nil
		}
		if errno, ok := callErr.(syscall.Errno); ok && errno == windows.ERROR_NO_MORE_ITEMS {
			ev, werr := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
			if werr != nil || ev == windows.WAIT_OBJECT_0 {
				return 0, errors.New("wintun 已停止")
			}
			continue
		}
		return 0, fmt.Errorf("读取 Wintun 数据包失败: %w", callErr)
	}
}

func (d *windowsTunDevice) WritePacket(pkt []byte) (int, error) {
	if d.closed.Load() || len(pkt) == 0 {
		return 0, nil
	}
	d.wMu.Lock()
	defer d.wMu.Unlock()
	dstPtr, _, callErr := d.pAllocSendPkt.Call(d.session, uintptr(len(pkt)))
	if dstPtr == 0 {
		return 0, fmt.Errorf("分配 Wintun 发送缓冲区失败: %w", callErr)
	}
	base := *(**byte)(unsafe.Pointer(&dstPtr))
	dst := unsafe.Slice(base, len(pkt))
	copy(dst, pkt)
	d.pSendPacket.Call(d.session, dstPtr)
	return len(pkt), nil
}

func (d *windowsTunDevice) Close() error {
	if !d.closed.CompareAndSwap(false, true) {
		return nil
	}
	if d.stopEvent != 0 {
		_ = windows.SetEvent(d.stopEvent)
	}

	d.routeMu.Lock()
	owned := append([]winOwnedRoute(nil), d.owned...)
	d.owned = nil
	d.routeMu.Unlock()
	for i := len(owned) - 1; i >= 0; i-- {
		r := owned[i]
		_, _ = runHiddenCmd("route", "delete", r.Network, "mask", r.Mask, r.Gateway,
			"if", strconv.FormatUint(uint64(r.IfIndex), 10))
	}

	if d.session != 0 {
		d.pEndSession.Call(d.session)
		d.session = 0
	}
	if d.adapter != 0 {
		d.pCloseAdapter.Call(d.adapter)
		d.adapter = 0
	}
	if d.stopEvent != 0 {
		_ = windows.CloseHandle(d.stopEvent)
		d.stopEvent = 0
	}
	if d.dll != nil {
		_ = d.dll.Release()
		d.dll = nil
	}
	return nil
}

func detectWindowsPhysicalDefaultRoute(excludeIfIdx uint32) (gateway string, ifIndex uint32, ifaceName string, err error) {
	out, cmdErr := runHiddenCmd("route", "print", "-4")
	if cmdErr != nil {
		return "", 0, "", cmdErr
	}
	ifaces, _ := net.Interfaces()
	ipToIface := make(map[string]net.Interface, len(ifaces))
	for _, ifc := range ifaces {
		if uint32(ifc.Index) == excludeIfIdx {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok {
				if v4 := ipNet.IP.To4(); v4 != nil {
					ipToIface[v4.String()] = ifc
				}
			}
		}
	}

	bestMetric := 1<<30 - 1
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "0.0.0.0" || fields[1] != "0.0.0.0" {
			continue
		}
		gw := fields[2]
		localIP := fields[3]
		metric, merr := strconv.Atoi(fields[4])
		if merr != nil || net.ParseIP(gw).To4() == nil {
			continue
		}
		ifc, ok := ipToIface[localIP]
		if !ok || uint32(ifc.Index) == excludeIfIdx {
			continue
		}
		if metric < bestMetric {
			bestMetric = metric
			gateway = gw
			ifIndex = uint32(ifc.Index)
			ifaceName = ifc.Name
		}
	}
	if gateway == "" || ifIndex == 0 {
		return "", 0, "", errors.New("未找到可用的 Windows 默认 IPv4 出口网关")
	}
	return gateway, ifIndex, ifaceName, nil
}

func runHiddenCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
