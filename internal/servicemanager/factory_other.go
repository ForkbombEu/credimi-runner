//go:build !darwin && !windows

package servicemanager

import "os"

func ForCurrentPlatform(configDir string) Manager {
	return ForCurrentPlatformWithBootstrap(configDir, BootstrapOptions{})
}

func ForCurrentPlatformWithBootstrap(configDir string, bootstrap BootstrapOptions) Manager {
	binary, _ := os.Executable()
	return NewDockerManagerWithBootstrap(configDir, binary, bootstrap)
}
