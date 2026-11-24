package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/KevinXC5/flyshadow-bar/internal/ui"
)

func main() {
	args := os.Args[1:]

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
	case "gui", "--gui", "--hidden", "-hidden", "--no-connect", "-no-connect",
		"--no-auto-sysproxy", "-no-auto-sysproxy",
		"--config", "-config", "--core-lib", "-core-lib":
		return true
	}
	return strings.HasPrefix(first, "-psn_") ||
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
	hidden := fs.Bool("hidden", false, "启动后隐藏主窗口，仅驻留托盘")
	noConnect := fs.Bool("no-connect", false, "启动后不自动连接隧道")
	noAutoSys := fs.Bool("no-auto-sysproxy", false, "禁用启动连接后自动开启系统代理")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	return ui.Run(ui.RunOptions{
		ConfigPath:          *cfgPath,
		CoreLibPath:         *libPath,
		StartHidden:         *hidden,
		NoConnect:           *noConnect,
		DisableAutoSysProxy: *noAutoSys,
	})
}
