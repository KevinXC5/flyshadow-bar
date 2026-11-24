package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// 提取全局标志 --json 与 --run-file（支持 --run-file=<path>）
	var jsonOut bool
	var runFile string
	var filtered []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
			jsonOut = true
		case args[i] == "--run-file":
			if i+1 >= len(args) {
				return errors.New("--run-file 缺少路径参数（用法: fsbar --run-file <路径> <子命令>）")
			}
			runFile = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--run-file="):
			runFile = strings.TrimPrefix(args[i], "--run-file=")
			if strings.TrimSpace(runFile) == "" {
				return errors.New("--run-file 路径不能为空")
			}
		default:
			filtered = append(filtered, args[i])
		}
	}
	if len(filtered) == 0 {
		printHelp(os.Stdout)
		return nil
	}

	cmd := strings.ToLower(filtered[0])
	rest := filtered[1:]

	switch cmd {
	case "help", "-h", "--help":
		if len(rest) > 0 {
			return printCommandHelp(os.Stdout, rest[0])
		}
		printHelp(os.Stdout)
		return nil
	case "version", "-v", "--version":
		if hasHelpFlag(rest) {
			return printCommandHelp(os.Stdout, "version")
		}
		return cmdVersion(jsonOut)
	case "update":
		if hasHelpFlag(rest) {
			return printCommandHelp(os.Stdout, "update")
		}
		return cmdUpdate(rest, jsonOut)
	}

	// 任意子命令携带 -h / --help 时直接输出该命令详尽帮助，无需连接运行中实例
	if hasHelpFlag(rest) {
		return printCommandHelp(os.Stdout, cmd)
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
			return errors.New("用法: fsbar node <关键词或ID>（运行 `fsbar help node` 查看详细说明）")
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
		return fmt.Errorf("未知子命令 %q（运行 `fsbar help` 查看全部可用命令与参数）", cmd)
	}
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
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
			return errors.New("用法: fsbar rule add <规则值> [--match <类型>] [--action <动作>] [--label <标签>]（运行 `fsbar help rule` 查看详细说明）")
		}
		domain := ""
		matchStr := "suffix"
		actionStr := "proxy"
		label := ""
		for i := 0; i < len(rest); i++ {
			switch {
			case rest[i] == "--match" || rest[i] == "-m":
				if i+1 >= len(rest) {
					return errors.New("--match 缺少匹配类型参数")
				}
				matchStr = rest[i+1]
				i++
			case strings.HasPrefix(rest[i], "--match="):
				matchStr = strings.TrimPrefix(rest[i], "--match=")
			case rest[i] == "--action" || rest[i] == "-a":
				if i+1 >= len(rest) {
					return errors.New("--action 缺少动作参数（proxy / direct / reject）")
				}
				actionStr = rest[i+1]
				i++
			case strings.HasPrefix(rest[i], "--action="):
				actionStr = strings.TrimPrefix(rest[i], "--action=")
			case rest[i] == "--label" || rest[i] == "-l":
				if i+1 >= len(rest) {
					return errors.New("--label 缺少分组标签名称")
				}
				label = rest[i+1]
				i++
			case strings.HasPrefix(rest[i], "--label="):
				label = strings.TrimPrefix(rest[i], "--label=")
			default:
				if domain == "" && !strings.HasPrefix(rest[i], "-") {
					domain = rest[i]
				}
			}
		}
		if domain == "" {
			return errors.New("请提供规则值（例如域名、CIDR 网段、端口或进程名）")
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
			return errors.New("用法: fsbar rule rm <规则ID>（可先运行 `fsbar rules` 查看规则 ID）")
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
			return errors.New("用法: fsbar rule toggle <规则ID>（可先运行 `fsbar rules` 查看规则 ID）")
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
		return fmt.Errorf("未知 rule 子命令 %q（支持: ls, add, toggle, rm；运行 `fsbar help rule` 查看详细说明）", sub)
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
	fmt.Printf("fsbar %s\n", version.Full())
	return nil
}

func cmdUpdate(args []string, jsonOut bool) error {
	download := false
	repoOverride := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--download" || args[i] == "-d":
			download = true
		case args[i] == "--repo":
			if i+1 >= len(args) {
				return errors.New("--repo 缺少仓库参数（格式: owner/repo）")
			}
			repoOverride = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--repo="):
			repoOverride = strings.TrimPrefix(args[i], "--repo=")
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
		fmt.Println("提示: 可执行 `fsbar update --download` 下载最新安装包到当前目录，或在桌面端「设置」页一键增量更新。")
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

func printHelp(w io.Writer) {
	fmt.Fprint(w, `fsbar — 轻量跨平台 FlyShadow Bar 桌面端与内置命令行工具

用法:
  fsbar [GUI 启动选项]                      启动常驻桌面窗口与菜单栏/系统托盘
  fsbar [全局选项] <子命令> [参数...]       通过本地回环接口操控正在运行的实例
  fsbar help <子命令>                       查看指定子命令的详细参数与示例
  fsbar <子命令> --help                     同上，无需实例运行即可查看

GUI 启动选项（直接启动桌面端时生效）:
  --gui                                     显式以 GUI 桌面模式启动（默认静默驻留菜单栏/托盘）
  --hidden                                  静默启动到菜单栏/系统托盘（默认行为）
  --show                                    启动时立即弹出主窗口（默认仅点击托盘图标时弹出）
  --config <路径>                           指定 FlyShadow config.json 文件路径
  --core-lib <路径>                         显式指定原生核心库路径（.dylib / .dll）
  --no-connect                              启动后仅监听本地代理，不自动连接隧道
  --no-auto-sysproxy                        禁止启动建连后自动开启系统代理

CLI 全局选项（可放在任意位置，适用于所有子命令）:
  --json                                    以格式化 JSON 输出结果，便于脚本或 jq 解析
  --run-file <路径>                         指定运行态发现文件路径（默认 ~/.FlyShadow/flyshadow-bar.run.json）

状态与节点控制:
  status                                    查看连接状态、当前节点、代理模式、端口、传输协议与实时速率
  ls | nodes                                列出全部订阅节点（ID、名称、收藏、QUIC/UDP 标签、延迟）
  node | use <关键词或ID>                   按节点 ID、精确名称或关键词热切换节点并保存配置
  ping                                      并发测速全部节点并按延迟结果刷新列表

代理与传输控制:
  mode [rule|global|direct]                 不带参数查询当前代理模式；带参数切换并保存（支持中英文）
  udp [on|off|toggle]                       开关 UDP 加速（QUIC）并热重连，省略参数默认 toggle
  native-udp [on|off|toggle]                开关原生 UDP 转发并热重连，省略参数默认 toggle
  sysproxy [on|off|toggle]                  开关操作系统代理，省略参数默认 toggle
  reload                                    重新读取 ~/.FlyShadow/config.json 并热刷新规则与配置

自定义分流规则（立即热生效，不断开当前隧道）:
  rules | rule ls                           列出所有自定义分流规则（ID、启停、匹配类型、规则值、动作、标签）
  rule add <规则值> [选项]                  新增自定义规则（支持 --match / --action / --label）
  rule toggle <规则ID>                      启用或禁用指定 ID 的自定义规则
  rule rm <规则ID>                          删除指定 ID 的自定义规则（别名: del / delete）

连接监控与维护:
  conns | connections                       查看核心库当前实时活跃连接（协议、目标、路由、进程与流量）
  version                                   输出 fsbar 版本号、Commit 与构建时间（别名: -v / --version）
  update [--download] [--repo <owner/r>]    检查 GitHub Releases 最新版本，加 --download 下载安装包
  help [子命令]                             显示总览帮助或单个子命令的完整说明

常用环境变量:
  FLYSHADOW_CONFIG_DIR                      自定义配置目录（默认 ~/.FlyShadow）
  FLYSHADOW_CONFIG_PATH                     自定义 config.json 完整路径
  FLYSHADOW_CORE_LIB                        指定原生核心动态库路径（优先级高于自动查找）
  FLYSHADOW_GEOIP                           指定 Country-only-cn-private.mmdb 路径
  FLYSHADOW_BAR_RUN_FILE                    覆盖运行态发现文件路径
  FLYSHADOW_UPDATE_URL                      指定镜像/本地测试更新源目录（仍强制 Ed25519 公钥验签）

常用示例:
  fsbar status                              查看运行状态与实时速率
  fsbar ls --json                           以 JSON 输出全部节点
  fsbar use 香港                            切换到名称包含「香港」的节点
  fsbar mode global                         切换为全局代理模式
  fsbar udp off                             关闭 UDP 加速并热重连
  fsbar rule add openai.com -m suffix -a proxy -l ai
                                            添加域名后缀代理规则并立即热生效
  fsbar conns                               查看当前活跃连接
  fsbar help rule                           查看 rule 子命令的全部 13 种匹配类型与示例
`)
}

func printCommandHelp(w io.Writer, raw string) error {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "status":
		fmt.Fprint(w, `用法:
  fsbar status [--json] [--run-file <路径>]

说明:
  查询运行中 FlyShadow Bar 实例的完整状态，包括连接状态、当前选中节点、
  代理模式、本地混合代理端口、局域网共享、系统代理状态、UDP 加速（QUIC）
  与原生 UDP 实际生效情况、实时上下行速率、隧道延迟、核心版本与规则总数。

选项:
  --json               输出完整 Snapshot JSON 结构
  --run-file <路径>    指定实例的运行态发现文件

示例:
  fsbar status
  fsbar status --json | jq '.currentNode.displayName'
`)
	case "ls", "nodes":
		fmt.Fprint(w, `用法:
  fsbar ls [--json] [--run-file <路径>]
  fsbar nodes [--json] [--run-file <路径>]

说明:
  列出当前订阅的全部可用节点，包含选中标记（●）、节点 ID、收藏标记（★）、
  展示名称、支持的传输协议标签（QUIC / UDP）以及最近一次测速延迟。

选项:
  --json               以 JSON 数组输出脱敏节点列表（不含节点密码）
  --run-file <路径>    指定实例的运行态发现文件

示例:
  fsbar ls
  fsbar nodes --json
`)
	case "node", "use":
		fmt.Fprint(w, `用法:
  fsbar node <关键词或ID> [--json]
  fsbar use <关键词或ID> [--json]

说明:
  热切换到指定节点并无损写回 config.json。

参数:
  <关键词或ID>         支持以下匹配优先级：
                       1. 精确数字节点 ID（如 12）
                       2. 完整节点名称或中文名称（不区分大小写）
                       3. 名称关键词模糊匹配（如 香港、日本、US）
                       名称包含空格时可直接传入多个词或用引号包裹。

示例:
  fsbar use 12
  fsbar use 香港
  fsbar node "HK18 10Gbps" --json
`)
	case "mode":
		fmt.Fprint(w, `用法:
  fsbar mode [rule|global|direct] [--json]

说明:
  不带参数时输出当前代理模式；传入模式名称后立即切换并无损保存到 config.json。

可选模式:
  rule   | rules  | 规则 | 3    规则分流模式（按 GeoIP 与分流规则自动判断直连/代理）
  global | 全局   | 2           全局代理模式（全部流量走代理隧道）
  direct | 直连   | 0           全局直连模式（全部流量直连，不经代理）

示例:
  fsbar mode
  fsbar mode rule
  fsbar mode global
  fsbar mode direct --json
`)
	case "udp":
		fmt.Fprint(w, `用法:
  fsbar udp [on|off|toggle] [--json]

说明:
  开关全局「UDP 加速（QUIC）」设置并持久化。若隧道当前处于连接状态，
  会自动热重连使新传输层参数生效。实际是否启用 QUIC 还取决于当前节点是否支持 QUIC。

参数:
  on      开启 UDP 加速（QUIC）
  off     关闭 UDP 加速（改走纯 TCP）
  toggle  取反切换（省略参数时的默认动作）

示例:
  fsbar udp on
  fsbar udp off
  fsbar udp
`)
	case "native-udp":
		fmt.Fprint(w, `用法:
  fsbar native-udp [on|off|toggle] [--json]

说明:
  开关全局「原生 UDP（Native UDP）」转发设置并持久化；已连接时自动热重连生效。

参数:
  on      开启原生 UDP
  off     关闭原生 UDP
  toggle  取反切换（省略参数时的默认动作）

示例:
  fsbar native-udp on
  fsbar native-udp off
  fsbar native-udp toggle
`)
	case "sysproxy":
		fmt.Fprint(w, `用法:
  fsbar sysproxy [on|off|toggle] [--json]

说明:
  一键开启或关闭操作系统代理（macOS 通过 networksetup 配置 HTTP/HTTPS/SOCKS，
  Windows 通过注册表与 WinINet 立即刷新）。应用退出时会自动清理系统代理。

参数:
  on      开启系统代理，指向本地混合代理端口
  off     关闭系统代理
  toggle  取反切换（省略参数时的默认动作）

示例:
  fsbar sysproxy on
  fsbar sysproxy off
  fsbar sysproxy
`)
	case "ping":
		fmt.Fprint(w, `用法:
  fsbar ping [--json] [--run-file <路径>]

说明:
  并发测试全部订阅节点的隧道握手延迟，完成后输出最新节点与延迟列表。
  进度提示输出至 stderr，方便配合 --json 重定向 stdout 解析。

示例:
  fsbar ping
  fsbar ping --json | jq 'sort_by(.delayMs)'
`)
	case "reload":
		fmt.Fprint(w, `用法:
  fsbar reload [--json] [--run-file <路径>]

说明:
  重新从磁盘加载 ~/.FlyShadow/config.json，热刷新分流规则、代理监听端口、
  认证配置与节点信息，无需重启客户端。

示例:
  fsbar reload
  fsbar reload --json
`)
	case "rules", "rule":
		fmt.Fprint(w, `用法:
  fsbar rules [--json]
  fsbar rule ls [--json]
  fsbar rule add <规则值> [--match <类型>] [--action <动作>] [--label <标签>] [--json]
  fsbar rule toggle <规则ID> [--json]
  fsbar rule rm <规则ID> [--json]

说明:
  管理官方 ~/.FlyShadow/config.json 中的自定义分流规则（customRuleList）。
  所有增删改与启停操作都会无损保存并立即热重载核心规则，不会中断当前隧道连接。

子命令:
  ls | list                列出全部自定义规则及其 ID、启停状态、匹配类型、动作与标签
  add <规则值> [选项]      新增一条启用状态的自定义规则
  toggle <规则ID>          切换指定规则的启用/禁用状态
  rm | del | delete <ID>   删除指定 ID 的自定义规则

add 选项:
  -m, --match <类型>       匹配类型（默认: suffix），支持全部 13 种官方类型：
                           exact (0)      域名完全匹配
                           suffix (1)     域名后缀匹配（默认）
                           keyword (2)    域名关键词匹配
                           cidr4 (3)      目标 IPv4 CIDR
                           cidr6 (4)      目标 IPv6 CIDR
                           src-cidr4 (5)  源 IPv4 CIDR
                           geoip (6)      GeoIP 国家码（如 CN）
                           dst-port (7)   目标端口
                           src-port (8)   源端口
                           process (9)    进程名称
                           all (10)       匹配全部流量
                           classical (11) Clash Classical 规则集
                           domainset (12) Clash Domain 规则集
  -a, --action <动作>      分流动作（默认: proxy）：
                           proxy  (2)     走代理隧道
                           direct (0)     直连不走代理
                           reject (1)     拒绝连接
  -l, --label <标签>       归属的规则分组标签（留空则归入默认分组）

示例:
  fsbar rules
  fsbar rule add openai.com --match suffix --action proxy --label ai
  fsbar rule add 192.168.0.0/16 -m cidr4 -a direct
  fsbar rule add curl -m process -a proxy
  fsbar rule toggle 1
  fsbar rule rm 1
`)
	case "conns", "connections":
		fmt.Fprint(w, `用法:
  fsbar conns [--json] [--run-file <路径>]
  fsbar connections [--json] [--run-file <路径>]

说明:
  查询底层核心库当前的实时活跃连接列表（按活跃时间降序排列），
  展示协议、目标地址与端口、路由结果（Direct / Proxy / Reject）、
  来源进程名以及该连接累计上传/下载字节数。

示例:
  fsbar conns
  fsbar conns --json | jq '.[] | {target: .target_addr, route: .proxy_type}'
`)
	case "version", "-v", "--version":
		fmt.Fprint(w, `用法:
  fsbar version [--json]

说明:
  显示当前 fsbar 的语义化版本号、Git Commit、构建时间及更新仓库地址，
  不需要运行中的桌面端实例。

示例:
  fsbar version
  fsbar version --json
`)
	case "update":
		fmt.Fprint(w, `用法:
  fsbar update [--download] [--repo <owner/repo>] [--json]

说明:
  查询 GitHub Releases 最新正式版本并与当前版本进行 SemVer 比较。
  桌面正式版推荐使用应用内「设置 → 增量更新」，仅下载差异补丁（.delta）并验签重启；
  终端用户可使用本命令检查更新或直接下载适配当前平台架构的安装包。

选项:
  -d, --download           若发现新版本，自动下载适配当前系统与 CPU 架构的安装包到当前目录
  --repo <owner/repo>      临时指定检查更新的 GitHub 仓库（默认 KevinXC5/flyshadow-bar）
  --json                   以 JSON 格式输出版本检查结果与下载地址

示例:
  fsbar update
  fsbar update --download
  fsbar update --json
`)
	case "help":
		printHelp(w)
	default:
		return fmt.Errorf("未找到子命令 %q 的帮助（运行 `fsbar help` 查看全部子命令）", raw)
	}
	return nil
}
