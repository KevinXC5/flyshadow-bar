//go:build !darwin && !windows

package core

import (
	"fmt"
	"runtime"
)

// Open 在非 macOS/Windows 平台上返回不支持错误。
func Open(libPath string) (Client, error) {
	return nil, fmt.Errorf("平台 %s 暂未实现 FlyShadow 原生核心库加载", runtime.GOOS)
}
