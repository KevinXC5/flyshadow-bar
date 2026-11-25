//go:build darwin

package ui

import (
	"os"
	"os/exec"
	"path/filepath"
)

// revealPathInFileManager 在 Finder 中高亮显示目标文件（若文件尚不存在则打开其所在目录）。
func revealPathInFileManager(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return exec.Command("/usr/bin/open", "-R", path).Start()
	}
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0o755)
	return exec.Command("/usr/bin/open", dir).Start()
}
