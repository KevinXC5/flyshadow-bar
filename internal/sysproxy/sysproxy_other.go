//go:build !darwin && !windows

package sysproxy

import "errors"

var errUnsupportedPlatform = errors.New("当前操作系统暂不支持系统代理管理")

type unsupportedManager struct{}

// New 返回未支持平台的占位实现。
func New() Manager {
	return &unsupportedManager{}
}

func (m *unsupportedManager) Enable(string, int) error {
	return errUnsupportedPlatform
}

func (m *unsupportedManager) Disable() error {
	return errUnsupportedPlatform
}

func (m *unsupportedManager) IsEnabled(string, int) (bool, error) {
	return false, errUnsupportedPlatform
}
