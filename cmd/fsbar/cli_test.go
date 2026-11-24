package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestIsGUILaunch(t *testing.T) {
	guiCases := [][]string{
		nil,
		{},
		{"--gui"},
		{"--hidden"},
		{"--show"},
		{"--config", "/tmp/c.json"},
		{"--config=/tmp/c.json"},
		{"--core-lib=/tmp/lib.dylib"},
		{"--no-connect"},
		{"--no-auto-sysproxy"},
		{"-psn_0_12345"},
	}
	for _, args := range guiCases {
		if !isGUILaunch(args) {
			t.Errorf("isGUILaunch(%v) = false, want true", args)
		}
	}

	cliCases := [][]string{
		{"help"},
		{"--help"},
		{"-h"},
		{"status"},
		{"ls"},
		{"use", "香港"},
		{"rule", "add", "example.com"},
		{"version"},
	}
	for _, args := range cliCases {
		if isGUILaunch(args) {
			t.Errorf("isGUILaunch(%v) = true, want false", args)
		}
	}
}

func TestPrintHelpAndCommandHelp(t *testing.T) {
	var top bytes.Buffer
	printHelp(&top)
	topStr := top.String()
	for _, want := range []string{"fsbar", "--hidden", "--json", "--run-file", "FLYSHADOW_CONFIG_PATH", "rule add", "fsbar help"} {
		if !strings.Contains(topStr, want) {
			t.Errorf("顶层 help 缺少关键字 %q", want)
		}
	}

	cmds := []string{
		"status", "ls", "nodes", "node", "use", "mode", "udp", "native-udp",
		"sysproxy", "ping", "reload", "rules", "rule", "conns", "connections",
		"version", "update", "help",
	}
	for _, c := range cmds {
		var buf bytes.Buffer
		if err := printCommandHelp(&buf, c); err != nil {
			t.Errorf("printCommandHelp(%q) 返回错误: %v", c, err)
			continue
		}
		if !strings.Contains(buf.String(), "fsbar") {
			t.Errorf("子命令 %q 的 help 应包含 fsbar 用法", c)
		}
	}

	var unknown bytes.Buffer
	if err := printCommandHelp(&unknown, "nonexistent"); err == nil {
		t.Fatalf("未知子命令 help 应返回错误")
	}
}
