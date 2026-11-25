//go:build !windows

package core

func discoverWindowsRegistryInstallDirs() []string {
	return nil
}
