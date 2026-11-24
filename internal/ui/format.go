package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/engine"
)

// FormatBytes 将字节数格式化为人类可读大小（如 12.4 GB）。
func FormatBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/(1024*1024*1024))
	}
}

// FormatExpireDate 将毫秒时间戳格式化为 YYYY-MM-DD。
func FormatExpireDate(ms int64) string {
	if ms <= 0 {
		return "未知"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02")
}

// FormatSpeed 将每秒字节数格式化为人类可读速率（如 12.4 KB/s）。
func FormatSpeed(bps int64) string {
	if bps < 0 {
		bps = 0
	}
	switch {
	case bps < 1024:
		return fmt.Sprintf("%d B/s", bps)
	case bps < 1024*1024:
		return fmt.Sprintf("%.1f KB/s", float64(bps)/1024)
	case bps < 1024*1024*1024:
		return fmt.Sprintf("%.2f MB/s", float64(bps)/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GB/s", float64(bps)/(1024*1024*1024))
	}
}

// FormatCompactSpeed 返回固定 5 字符宽度的紧凑速率（配合 MonospacedTitle 防止菜单栏抖动）。
func FormatCompactSpeed(bps int64) string {
	if bps < 0 {
		bps = 0
	}
	var s string
	switch {
	case bps < 1000:
		s = fmt.Sprintf("%4dB", bps)
	case bps < 1024*10:
		s = fmt.Sprintf("%4.1fK", float64(bps)/1024)
	case bps < 1024*1000:
		s = fmt.Sprintf("%4.0fK", float64(bps)/1024)
	case bps < 1024*1024*10:
		s = fmt.Sprintf("%4.1fM", float64(bps)/(1024*1024))
	default:
		s = fmt.Sprintf("%4.0fM", float64(bps)/(1024*1024))
	}
	return strings.ReplaceAll(s, " ", "\u2007")
}

// FormatDelay 返回节点延迟展示文案。
func FormatDelay(delayMs int64, testing bool) string {
	if testing {
		return "测速中…"
	}
	if delayMs < 0 {
		return "超时"
	}
	if delayMs == 0 {
		return "--"
	}
	return fmt.Sprintf("%d ms", delayMs)
}

// DelayBadgeColor 返回适合菜单 Badge 的延迟语义色（CSS Hex）。
func DelayBadgeColor(delayMs int64) string {
	switch {
	case delayMs <= 0:
		return "#94a3b8"
	case delayMs < 120:
		return "#10b981"
	case delayMs < 280:
		return "#f59e0b"
	default:
		return "#ef4444"
	}
}

// StateLabel 返回隧道状态的简体中文描述。
func StateLabel(state engine.TunnelState) string {
	switch state {
	case engine.StateConnected:
		return "已连接"
	case engine.StateConnecting:
		return "连接中"
	case engine.StateError:
		return "连接异常"
	default:
		return "未连接"
	}
}
