//go:build windows

package cliinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	winEnvRegPath      = `Environment`
	winCmdShimFileName = "fsbar.cmd"
	hwndBroadcast      = 0xffff
	wmSettingChange    = 0x001a
	smtoAbortIfHung    = 0x0002
)

var (
	user32DLL             = windows.NewLazySystemDLL("user32.dll")
	procSendMessageTimout = user32DLL.NewProc("SendMessageTimeoutW")
)

func ensurePlatform(opts Options) (Status, error) {
	exe := filepath.Clean(opts.ExecutablePath)
	dir := strings.TrimSpace(opts.TargetDir)
	if dir == "" {
		dir = filepath.Dir(exe)
	} else {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Status{}, fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}

	shimPath := filepath.Join(dir, winCmdShimFileName)
	changed := false

	// 写入轻量 fsbar.cmd 包装器，使任意 CMD / PowerShell 均可通过 fsbar 调用安装目录内的可执行文件
	wantShim := buildWindowsCmdShim(dir, exe)
	oldShim, err := os.ReadFile(shimPath)
	if err != nil || string(oldShim) != wantShim {
		if werr := os.WriteFile(shimPath, []byte(wantShim), 0o644); werr != nil {
			return Status{}, fmt.Errorf("写入 %s 失败: %w", shimPath, werr)
		}
		changed = true
	}

	// 将所在目录追加到当前用户 HKCU\Environment\Path（若尚未存在）并广播环境变量变更
	pathAdded, err := ensureWindowsUserPATH(dir)
	if err != nil {
		return Status{
			Installed:  true,
			LinkPath:   shimPath,
			TargetPath: exe,
			InPATH:     false,
			Changed:    changed,
			Detail:     fmt.Sprintf("%s（更新用户 PATH 失败: %v）", shimPath, err),
		}, err
	}
	if pathAdded {
		changed = true
		broadcastEnvironmentChange()
	}

	return Status{
		Installed:  true,
		LinkPath:   shimPath,
		TargetPath: exe,
		InPATH:     true,
		Changed:    changed,
		Detail:     shimPath,
	}, nil
}

func detectPlatform(opts Options) Status {
	exe := filepath.Clean(opts.ExecutablePath)
	dir := filepath.Dir(exe)
	if strings.TrimSpace(opts.TargetDir) != "" {
		dir = filepath.Clean(opts.TargetDir)
	}
	shimPath := filepath.Join(dir, winCmdShimFileName)
	if _, err := os.Stat(shimPath); err != nil {
		return Status{
			Installed: false,
			Detail:    "尚未注册 fsbar 到用户 PATH",
		}
	}
	userPath := readWindowsUserPATH()
	inPath := pathListContainsDir(userPath, dir) || pathListContainsDir(os.Getenv("PATH"), dir)
	detail := shimPath
	if !inPath {
		detail = fmt.Sprintf("%s（尚未加入用户 PATH）", shimPath)
	}
	return Status{
		Installed:  true,
		LinkPath:   shimPath,
		TargetPath: exe,
		InPATH:     inPath,
		Detail:     detail,
	}
}

func buildWindowsCmdShim(shimDir, exePath string) string {
	// 同目录时优先使用 %~dp0 相对路径，移动安装目录后依然可用
	if strings.EqualFold(filepath.Clean(shimDir), filepath.Clean(filepath.Dir(exePath))) {
		return fmt.Sprintf("@echo off\r\n\"%%~dp0%s\" %%*\r\n", filepath.Base(exePath))
	}
	return fmt.Sprintf("@echo off\r\n\"%s\" %%*\r\n", exePath)
}

func readWindowsUserPATH() string {
	key, err := registry.OpenKey(registry.CURRENT_USER, winEnvRegPath, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	val, _, err := key.GetStringValue("Path")
	if err != nil {
		return ""
	}
	return val
}

func ensureWindowsUserPATH(dir string) (bool, error) {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, winEnvRegPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer key.Close()

	cur, valType, err := key.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return false, err
	}
	if pathListContainsDir(cur, dir) {
		return false, nil
	}

	trimmed := strings.TrimRight(cur, "; ")
	next := dir
	if trimmed != "" {
		next = trimmed + ";" + dir
	}

	// 保持 REG_EXPAND_SZ 以兼容 %USERPROFILE% 等变量引用
	if valType == registry.SZ && !strings.Contains(next, "%") {
		if err := key.SetStringValue("Path", next); err != nil {
			return false, err
		}
	} else {
		if err := key.SetExpandStringValue("Path", next); err != nil {
			return false, err
		}
	}
	return true, nil
}

func broadcastEnvironmentChange() {
	envPtr, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	_, _, _ = procSendMessageTimout.Call(
		uintptr(hwndBroadcast),
		uintptr(wmSettingChange),
		0,
		uintptr(unsafe.Pointer(envPtr)),
		uintptr(smtoAbortIfHung),
		500,
		uintptr(unsafe.Pointer(&result)),
	)
}
