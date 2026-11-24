package engine

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// parseLsofClientMap 解析 `lsof -nP +c 0 -i :<port> -F pcn` 的字段输出，
// 排除本进程（代理服务端）后，返回 `源地址:源端口 -> 客户端进程名` 映射。
func parseLsofClientMap(out string, selfPID int) map[string]string {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	res := make(map[string]string)
	var curPID int
	var curCmd string

	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if len(line) < 2 {
			continue
		}
		tag, val := line[0], line[1:]
		switch tag {
		case 'p':
			pid, err := strconv.Atoi(val)
			if err == nil {
				curPID = pid
			} else {
				curPID = 0
			}
			curCmd = ""
		case 'c':
			curCmd = strings.TrimSpace(unescapeLsofCmd(val))
		case 'n':
			if curPID == 0 || curPID == selfPID || curCmd == "" {
				continue
			}
			// 仅匹配有方向箭头的连接端点：<本地客户端地址>-><代理服务端地址>
			arrow := strings.Index(val, "->")
			if arrow <= 0 {
				continue
			}
			src := normalizeEndpoint(val[:arrow])
			if src != "" {
				res[src] = curCmd
			}
		}
	}
	return res
}

// unescapeLsofCmd 还原 lsof 在部分系统版本中对空格等字符输出的两位十六进制转义（如 \x20）。
func unescapeLsofCmd(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// normalizeEndpoint 将 `127.0.0.1:59900`、`[::1]:59900` 或 `[::ffff:127.0.0.1]:59900` 统一规范化。
func normalizeEndpoint(ep string) string {
	ep = strings.TrimSpace(ep)
	if ep == "" {
		return ""
	}
	host, portStr, err := net.SplitHostPort(ep)
	if err != nil {
		return ep
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return ep
	}
	return formatConnSourceKey(host, port)
}

// formatConnSourceKey 构造用于客户端进程反查的规范化源地址键。
func formatConnSourceKey(addr string, port int) string {
	if port <= 0 {
		return ""
	}
	host := strings.TrimSpace(strings.Trim(addr, "[]"))
	if host == "" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			host = v4.String()
		} else {
			host = ip.String()
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// currentSelfProcessNames 收集当前进程可能被 lsof/核心库报告的名称（含 9 字符截断形式）。
func currentSelfProcessNames() []string {
	seen := make(map[string]struct{})
	add := func(name string) {
		n := strings.ToLower(strings.TrimSpace(name))
		if n == "" {
			return
		}
		seen[n] = struct{}{}
		if len(n) > 9 {
			seen[n[:9]] = struct{}{}
		}
	}
	add("FlyShadow")
	add("FlyShadow Bar")
	add("fsbar")
	if exe, err := os.Executable(); err == nil && exe != "" {
		base := filepath.Base(exe)
		add(base)
		add(strings.TrimSuffix(base, filepath.Ext(base)))
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// isSelfProcessName 判断进程名是否属于代理进程自身（核心库误将服务端端点识别为来源进程）。
func isSelfProcessName(name string, selfNames []string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	for _, self := range selfNames {
		if n == self {
			return true
		}
	}
	return false
}
