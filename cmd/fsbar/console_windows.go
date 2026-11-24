//go:build windows

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

const attachParentProcess = ^uint32(0)

// attachParentConsole 使以 -H=windowsgui 编译的单二进制在 Windows 终端执行 CLI 子命令时，
// 能附着到父进程控制台并正常输出 stdout / stderr；若输出已被重定向到管道或文件，则保留原句柄。
func attachParentConsole() {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procAttach := kernel32.NewProc("AttachConsole")
	r, _, _ := procAttach.Call(uintptr(attachParentProcess))
	if r == 0 {
		return
	}

	if !hasValidStdHandle(windows.STD_OUTPUT_HANDLE) {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stdout = f
			syscall.Stdout = syscall.Handle(f.Fd())
		}
	}
	if !hasValidStdHandle(windows.STD_ERROR_HANDLE) {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stderr = f
			syscall.Stderr = syscall.Handle(f.Fd())
		}
	}
}

func hasValidStdHandle(which uint32) bool {
	h, err := windows.GetStdHandle(which)
	if err != nil || h == 0 || h == windows.InvalidHandle {
		return false
	}
	t, err := windows.GetFileType(h)
	return err == nil && t != windows.FILE_TYPE_UNKNOWN
}
