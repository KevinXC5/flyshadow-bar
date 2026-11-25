//go:build windows

package ui

import (
	"os"
	"os/exec"
	"path/filepath"
)

// revealPathInFileManager 在 Windows 资源管理器中选中目标文件（若文件尚不存在则打开其所在目录）。
func revealPathInFileManager(path string) error {
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	if _, err := os.Stat(clean); err == nil {
		return exec.Command("explorer.exe", "/select,"+clean).Start()
	}
	dir := filepath.Dir(clean)
	_ = os.MkdirAll(dir, 0o755)
	return exec.Command("explorer.exe", dir).Start()
}
