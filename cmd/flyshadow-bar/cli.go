package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/control"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
	"github.com/KevinXC5/flyshadow-bar/internal/ui"
	"github.com/KevinXC5/flyshadow-bar/internal/updater"
	"github.com/KevinXC5/flyshadow-bar/internal/version"
)

func runCLI(args []string) error {
	// 提取全局标志 --json 与 --run-file
	var jsonOut bool
	var runFile string
	var filtered []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--run-file":
			if i+1 >= len(args) {
				return errors.New("--run-file 缺少路径参数")
			}
			runFile = args[i+1]
			i++
		default:
			filtered = append(filtered, args[i])
		}
	}
	if len(filtered) == 0 {
		printHelp()
		return nil
	}

	cmd := strings.ToLower(filtered[0])
	rest := filtered[1:]

	switch cmd {
	case "help", "-h", "--help":
		printHelp()
		return nil
	case "version", "-v", "--version":
		return cmdVersion(jsonOut)
	case "update":
		return cmdUpdate(rest, jsonOut)
	}

	// 以下子命令均通过本地控制服务操作正在运行的 GUI 实例
	cli, err := control.Discover(runFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch cmd {
	case "status":
		snap, err := cli.Status(ctx)
		if err != nil {
			return err
		}
		return printStatus(snap, jsonOut)

	case "ls", "nodes":
		nodes, err := cli.Nodes(ctx)
		if err != nil {
			return err
		}
		return printNodes(nodes, jsonOut)

	case "node", "use":
		if len(rest) == 0 {
			return errors.New("用法: flyshadow-bar node <关键词或ID>")
		}
		query := strings.Join(rest, " ")
		snap, err := cli.SelectNode(ctx, query)
		if err != nil {
			return err
		}
		if jsonOut {
			return writeIndentedJSON(snap)
		}
		if snap.CurrentNode != nil {
			fmt.Printf("已切换节点: %s (#%d) [%s]\n", snap.CurrentNode.DisplayName, snap.CurrentNode.ID, ui.StateLabel(snap.State))
		}
		return nil

	case "mode":
		if len(rest) == 0 {
			snap, err := cli.Status(ctx)
			if err != nil {
				return err
			}
			if jsonOut {
				return writeIndentedJSON(map[string]any{"mode": snap.Mode, "label": snap.ModeCode.Label()})
			}
			fmt.Printf("当前代理模式: %s (%s)\n", snap.Mode, snap.ModeCode.Label())
			return nil
		}
		snap, err := cli.SetMode(ctx, rest[0])
		if err != nil {
			return err
		}
		if jsonOut {
			return writeIndentedJSON(snap)
		}
		fmt.Printf("已切换代理模式: %s (%s)\n", snap.Mode, snap.ModeCode.Label())
		return nil

	case "udp":
		action := "toggle"
		if len(rest) > 0 {
			action = rest[0]
		}
		snap, err := cli.SetUDP(ctx, action)
		if err != nil {
			return err
		}
		if jsonOut {
			return writeIndentedJSON(snap)
		}
		fmt.Printf("UDP 加速 (QUIC): %s（当前节点生效: %s）\n", onOff(snap.UDPAccelerationEnabled), onOff(snap.EffectiveTransport.EnableQuic))
		return nil

	case "native-udp":
		action := "toggle"
		if len(rest) > 0 {
			action = rest[0]
		}
		snap, err := cli.SetNativeUDP(ctx, action)
		if err != nil {
			return err
		}
		if jsonOut {
			return writeIndentedJSON(snap)
		}
		fmt.Printf("原生 UDP (Native UDP): %s（当前节点生效: %s）\n", onOff(snap.NativeUDP), onOff(snap.EffectiveTransport.EnableNativeUDP))
		return nil

	case "sysproxy":
		action := "toggle"
		if len(rest) > 0 {
			action = rest[0]
		}
		snap, err := cli.SetSysProxy(ctx, action)
		if err != nil {
			return err
		}
		if jsonOut {
			return writeIndentedJSON(snap)
		}
		fmt.Printf("系统代理: %s（127.0.0.1:%d）\n", onOff(snap.SystemProxy), snap.ProxyPort)
		return nil

	case "ping":
		fmt.Fprintln(os.Stderr, "正在并发测速全部节点…")
		nodes, err := cli.Ping(ctx)
		if err != nil {
			return err
		}
		return printNodes(nodes, jsonOut)

	case "reload":
		snap, err := cli.Reload(ctx)
		if err != nil {
			return err
		}
		if jsonOut {
			return writeIndentedJSON(snap)
		}
		fmt.Printf("已重载配置（节点 %d 个，分流规则 %d 条）\n", len(snap.Nodes), snap.RuleCount)
		return nil

	case "rules":
		rules, err := cli.Rules(ctx)
		if err != nil {
			return err
		}
		return printRules(rules, jsonOut)

	case "rule":
		return cmdRule(ctx, cli, rest, jsonOut)

	case "conns", "connections":
		conns, err := cli.Connections(ctx)
		if err != nil {
			return err
		}
		return printConnections(conns, jsonOut)

	default:
		return fmt.Errorf("未知子命令 %q（运行 flyshadow-bar help 查看帮助）", cmd)
	}
}

func cmdRule(ctx context.Context, cli *control.Client, args []string, jsonOut bool) error {
	if len(args) == 0 || args[0] == "ls" || args[0] == "list" {
		rules, err := cli.Rules(ctx)
		if err != nil {
			return err
		}
		return printRules(rules, jsonOut)
	}

	sub := strings.ToLower(args[0])
	rest := args[1:]

	switch sub {
	case "add":
		if len(rest) == 0 {
			return errors.New("用法: flyshadow-bar rule add <域名/规则值> [--match suffix|exact|keyword|cidr4|process] [--action proxy|direct|reject] [--label 标签]")
		}
		domain := ""
		matchStr := "suffix"
		actionStr := "proxy"
		label := ""
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "--match", "-m":
				if i+1 < len(rest) {
					matchStr = rest[i+1]
					i++
				}
			case "--action", "-a":
				if i+1 < len(rest) {
					actionStr = rest[i+1]
					i++
				}
			case "--label", "-l":
				if i+1 < len(rest) {
					label = rest[i+1]
					i++
				}
			default:
				if domain == "" && !strings.HasPrefix(rest[i], "-") {
					domain = rest[i]
				}
			}
		}
		if domain == "" {
			return errors.New("请提供规则值（域名/IP/关键词）")
		}
		matching, err := config.ParseRuleMatching(matchStr)
		if err != nil {
			return err
		}
		proxyType, err := config.ParseRuleAction(actionStr)
		if err != nil {
			return err
		}
		added, err := cli.AddRule(ctx, config.DomainRule{
			Domain:    domain,
			Matching:  matching,
			ProxyType: proxyType,
			Label:     label,
			Enable:    config.IntPtr(1),
		})
		if err != nil {
			return err
		}
		if jsonOut {
			return writeIndentedJSON(added)
		}
		fmt.Printf("已添加规则 #%d: [%s] %s -> %s（已热生效）\n",
			added.ID, config.RuleMatchingLabel(added.Matching), added.Domain, config.RuleActionLabel(added.ProxyType))
		return nil

	case "rm", "del", "delete":
		if len(rest) == 0 {
			return errors.New("用法: flyshadow-bar rule rm <规则ID>")
		}
		id, err := strconv.Atoi(rest[0])
		if err != nil || id <= 0 {
			return fmt.Errorf("无效的规则 ID: %q", rest[0])
		}
		if err := cli.DeleteRule(ctx, id); err != nil {
			return err
		}
		if jsonOut {
			return writeIndentedJSON(map[string]any{"deleted": id})
		}
		fmt.Printf("已删除规则 #%d（已热生效）\n", id)
		return nil

	case "toggle":
		if len(rest) == 0 {
			return errors.New("用法: flyshadow-bar rule toggle <规则ID>")
		}
		id, err := strconv.Atoi(rest[0])
		if err != nil || id <= 0 {
			return fmt.Errorf("无效的规则 ID: %q", rest[0])
		}
		enabled, err := cli.ToggleRule(ctx, id)
		if err != nil {
			return err
		}
		if jsonOut {
			return writeIndentedJSON(map[string]any{"id": id, "enabled": enabled})
		}
		fmt.Printf("规则 #%d 已%s（已热生效）\n", id, onOff(enabled))
		return nil

	default:
		return fmt.Errorf("未知 rule 子命令 %q（支持: ls, add, rm, toggle）", sub)
	}
}

func printRules(rules []config.DomainRule, jsonOut bool) error {
	if jsonOut {
		return writeIndentedJSON(rules)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\t状态\t匹配类型\t规则值\t动作\t标签")
	for _, r := range rules {
		lbl := r.Label
		if strings.TrimSpace(lbl) == "" {
			lbl = "默认"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, onOff(r.IsEnabled()), config.RuleMatchingLabel(r.Matching), r.Domain, config.RuleActionLabel(r.ProxyType), lbl)
	}
	return tw.Flush()
}

func printConnections(conns []core.ConnectionInfo, jsonOut bool) error {
	if jsonOut {
		return writeIndentedJSON(conns)
	}
	if len(conns) == 0 {
		fmt.Println("当前没有活跃连接")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "协议\t目标\t路由\t进程\t上传\t下载")
	for _, c := range conns {
		proc := c.ProcessName
		if proc == "" {
			proc = "-"
		}
		fmt.Fprintf(tw, "%s\t%s:%d\t%s\t%s\t%s\t%s\n",
			c.Protocol, c.TargetAddr, c.TargetPort, c.ProxyType, proc,
			ui.FormatBytes(c.UploadTrafficTotal), ui.FormatBytes(c.DownloadTrafficTotal))
	}
	return tw.Flush()
}

func cmdVersion(jsonOut bool) error {
	info := map[string]string{
		"version":    version.Version,
		"commit":     version.Commit,
		"buildDate":  version.BuildDate,
		"githubRepo": version.GithubRepo,
	}
	if jsonOut {
		return writeIndentedJSON(info)
	}
	fmt.Printf("flyshadow-bar %s\n", version.Full())
	return nil
}

func cmdUpdate(args []string, jsonOut bool) error {
	download := false
	repoOverride := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--download", "-d":
			download = true
		case "--repo":
			if i+1 < len(args) {
				repoOverride = args[i+1]
				i++
			}
		}
	}
	if repoOverride == "" {
		repoOverride = config.LoadAppPrefs("").UpdateRepo
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := updater.Check(ctx, repoOverride, version.Version)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeIndentedJSON(res)
	}

	if !res.HasUpdate {
		fmt.Printf("当前已是最新版本 (%s)\n", res.CurrentVersion)
		return nil
	}

	fmt.Printf("发现新版本: %s -> %s\n", res.CurrentVersion, res.LatestVersion)
	if res.ReleaseURL != "" {
		fmt.Printf("发布页面: %s\n", res.ReleaseURL)
	}
	if res.AssetURL != "" {
		fmt.Printf("适配安装包: %s\n", res.AssetName)
	}
	if !download {
		fmt.Println("提示: 可执行 `flyshadow-bar update --download` 下载最新安装包到当前目录。")
		return nil
	}
	if res.AssetURL == "" {
		return errors.New("该版本未附带适配当前平台的安装包，请前往发布页面手动下载")
	}

	dest := filepath.Join(".", res.AssetName)
	fmt.Printf("正在下载 %s …\n", res.AssetName)
	dlCtx, dlCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer dlCancel()
	if err := updater.DownloadAsset(dlCtx, res.AssetURL, dest, nil); err != nil {
		return err
	}
	fmt.Printf("下载完成: %s\n", dest)
	return nil
}

func printStatus(snap *engine.Snapshot, jsonOut bool) error {
	if jsonOut {
		return writeIndentedJSON(snap)
	}
	nodeName := "未选择"
	if snap.CurrentNode != nil {
		nodeName = fmt.Sprintf("%s (#%d)", snap.CurrentNode.DisplayName, snap.CurrentNode.ID)
	}
	fmt.Printf("状态:       %s\n", ui.StateLabel(snap.State))
	fmt.Printf("当前节点:   %s\n", nodeName)
	fmt.Printf("代理模式:   %s (%s)\n", snap.Mode, snap.ModeCode.Label())
	fmt.Printf("本地代理:   127.0.0.1:%d (局域网共享: %s)\n", snap.ProxyPort, onOff(snap.AllowLAN))
	fmt.Printf("系统代理:   %s\n", onOff(snap.SystemProxy))
	fmt.Printf("UDP 加速:   %s（当前节点生效: %s）\n", onOff(snap.UDPAccelerationEnabled), onOff(snap.EffectiveTransport.EnableQuic))
	fmt.Printf("原生 UDP:   %s（当前节点生效: %s）\n", onOff(snap.NativeUDP), onOff(snap.EffectiveTransport.EnableNativeUDP))
	fmt.Printf("实时速率:   ↑ %s   ↓ %s\n", ui.FormatSpeed(snap.UploadSpeedBps), ui.FormatSpeed(snap.DownloadSpeedBps))
	if snap.TunnelPingMs > 0 {
		fmt.Printf("隧道延迟:   %d ms\n", snap.TunnelPingMs)
	}
	fmt.Printf("核心版本:   %s（规则 %d 条，GeoIP: %s）\n", snap.CoreVersion, snap.RuleCount, onOff(snap.GeoIPLoaded))
	if snap.LastError != "" {
		fmt.Printf("最近错误:   %s\n", snap.LastError)
	}
	return nil
}

func printNodes(nodes []engine.NodeSnapshot, jsonOut bool) error {
	if jsonOut {
		return writeIndentedJSON(nodes)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "选中\tID\t名称\t协议标签\t延迟")
	for _, n := range nodes {
		mark := " "
		if n.Selected {
			mark = "●"
		}
		name := n.DisplayName
		if n.Starred {
			name = "★ " + name
		}
		var tags []string
		if n.EnableQuic {
			tags = append(tags, "QUIC")
		}
		if n.NativeUDP {
			tags = append(tags, "UDP")
		}
		tagStr := "-"
		if len(tags) > 0 {
			tagStr = strings.Join(tags, ",")
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", mark, n.ID, name, tagStr, ui.FormatDelay(n.DelayMs, n.Testing))
	}
	return tw.Flush()
}

func writeIndentedJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func onOff(v bool) string {
	if v {
		return "开"
	}
	return "关"
}

func printHelp() {
	fmt.Print(`flyshadow-bar — 轻量跨平台 FlyShadow 菜单栏客户端与内置 CLI

用法:
  flyshadow-bar                         启动 GUI 与系统托盘
  flyshadow-bar [--hidden]              静默启动到托盘
  flyshadow-bar <子命令> [参数] [--json]  通过本地控制接口操控运行中的实例

子命令:
  status                                查看连接状态、当前节点、模式、UDP、系统代理与实时速率
  ls | nodes                            列出所有节点（ID、名称、QUIC/UDP 标签、延迟、当前选中）
  node | use <关键词或ID>                热切换节点并保存配置
  mode <rule|global|direct>             切换代理模式
  udp <on|off|toggle>                   切换 UDP 加速 (QUIC) 并热重连
  native-udp <on|off|toggle>            切换原生 UDP 并热重连
  sysproxy <on|off|toggle>              开关系统代理
  ping                                  并发测速所有节点并按延迟排序展示
  reload                                重新读取 ~/.FlyShadow/config.json 并刷新规则
  rules | rule ls                       列出所有自定义规则
  rule add <域名> [选项]                 快速添加自定义规则并热生效（--match/--action/--label）
  rule toggle <ID>                      启用或禁用指定自定义规则
  rule rm <ID>                          删除指定自定义规则
  conns                                 查看当前活跃连接列表
  version                               显示客户端版本信息
  update [--download] [--repo owner/r]  检查 GitHub Releases 更新（--download 下载安装包）
  help                                  显示帮助信息
`)
}
