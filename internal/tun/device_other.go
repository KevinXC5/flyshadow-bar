//go:build !darwin && !windows

package tun

// IsAdmin 在非 macOS/Windows 平台返回 false。
func IsAdmin() bool {
	return false
}

// NeedsElevationRestart 在其他平台返回 false。
func NeedsElevationRestart() bool {
	return false
}

// RelaunchAsAdmin 在其他平台返回不支持错误。
func RelaunchAsAdmin() error {
	return ErrUnsupported
}

// RunMacHelper 仅在 macOS 上支持。
func RunMacHelper([]string) error {
	return ErrUnsupported
}

func openPlatformDevice(DeviceConfig) (PacketDevice, error) {
	return nil, ErrUnsupported
}
