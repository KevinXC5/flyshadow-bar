//go:build !darwin && !windows

package ui

import (
	"os"
	"os/exec"
	"path/filepath"
)

func revealPathInFileManager(path string) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0o755)
	return exec.Command("xdg-open", dir).Start()
}
