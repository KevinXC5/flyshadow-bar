package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/applog"
	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/tun"
	"github.com/KevinXC5/flyshadow-bar/internal/ui"
)

func main() {
	args := os.Args[1:]

	// macOS 内置提权 TUN 辅助子命令：直接以 root 运行虚拟网卡与路由回滚看门狗
	if len(args) > 0 && args[0] == "tun-helper" {
		if err := tun.RunMacHelper(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "tun-helper 失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// 无参数、macOS LaunchServices 传入的 -psn_*，或显式 GUI 标志均启动桌面应用
	if isGUILaunch(args) {
		guiArgs := args
		if len(guiArgs) > 0 && (guiArgs[0] == "gui" || guiArgs[0] == "--gui" || strings.HasPrefix(guiArgs[0], "-psn_")) {
			guiArgs = guiArgs[1:]
		}
		if err := runGUI(guiArgs); err != nil {
			fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	attachParentConsole()
	if err := runCLI(args); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}

func isGUILaunch(args []string) bool {
	if len(args) == 0 {
		return true
	}
	first := args[0]
	switch first {
	case "gui", "--gui", "--hidden", "-hidden", "--show", "-show", "--relaunch", "-relaunch",
		"--enable-tun", "-enable-tun", "--wait-pid", "-wait-pid",
		"--no-connect", "-no-connect",
		"--no-auto-sysproxy", "-no-auto-sysproxy",
		"--config", "-config", "--core-lib", "-core-lib":
		return true
	}
	return strings.HasPrefix(first, "-psn_") ||
		strings.HasPrefix(first, "--wait-pid=") ||
		strings.HasPrefix(first, "-wait-pid=") ||
		strings.HasPrefix(first, "--config=") ||
		strings.HasPrefix(first, "-config=") ||
		strings.HasPrefix(first, "--core-lib=") ||
		strings.HasPrefix(first, "-core-lib=")
}

func runGUI(args []string) error {
	fs := flag.NewFlagSet("fsbar", flag.ContinueOnError)
	fs.Usage = func() {
		printHelp(os.Stderr)
	}
	cfgPath := fs.String("config", "", "指定 FlyShadow config.json 路径")
	libPath := fs.String("core-lib", "", "指定 FlyShadow 核心动态库路径")
	hidden := fs.Bool("hidden", true, "静默启动到菜单栏/系统托盘（默认开启）")
	show := fs.Bool("show", false, "启动时立即弹出主窗口")
	relaunch := fs.Bool("relaunch", false, "更新重启后弹出一次主窗口（失焦仍自动收起）")
	waitPID := fs.Int("wait-pid", 0, "等待指定旧进程退出后再初始化单实例锁（Windows 提权重启使用）")
	enableTun := fs.Bool("enable-tun", false, "启动并连接后自动开启 TUN 虚拟网卡模式")
	noConnect := fs.Bool("no-connect", false, "启动后不自动连接隧道")
	noAutoSys := fs.Bool("no-auto-sysproxy", false, "禁用启动连接后自动开启系统代理")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	// Windows UAC 提权重启场景：等待旧进程释放单实例锁后再继续初始化 UI
	if *waitPID > 0 {
		tun.WaitForProcessExit(*waitPID, 6*time.Second)
	}

	// 仅在使用默认主目录（未显式传入 --config 且未设置自定义目录环境变量）时，
	// 自动完成从旧目录 ~/.FlyShadow 到独立主目录 ~/.flyshadowbar 的无损复制迁移。
	var migReport config.MigrationReport
	var migErr error
	if strings.TrimSpace(*cfgPath) == "" {
		migReport, migErr = config.EnsureMigrated()
	}

	if logPath, err := config.DefaultLogPath(); err == nil && logPath != "" {
		if _, err := applog.Init(applog.Options{
			FilePath:     logPath,
			MirrorStderr: true,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "初始化程序日志失败 (%s): %v\n", logPath, err)
		} else {
			defer applog.Close()
			defer applog.RecoverAndLog("gui")
		}
	}

	if migErr != nil {
		log.Printf("[config] 兼容迁移旧目录失败（不影响继续启动）: %v", migErr)
	} else if migReport.CopiedConfig || migReport.CopiedPrefs {
		log.Printf("[config] 已从旧目录迁移至独立主目录 %s (config=%v prefs=%v)",
			migReport.TargetDir, migReport.CopiedConfig, migReport.CopiedPrefs)
	}

	startHidden := *hidden && !*show
	err := ui.Run(ui.RunOptions{
		ConfigPath:          *cfgPath,
		CoreLibPath:         *libPath,
		StartHidden:         startHidden,
		ShowOnceOnStart:     *relaunch,
		EnableTunOnStart:    *enableTun,
		NoConnect:           *noConnect,
		DisableAutoSysProxy: *noAutoSys,
	})
	if err != nil {
		log.Printf("[app] 启动或运行异常退出: %v", err)
	}
	return err
}
