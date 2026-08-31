//go:build darwin

package servicemanager

import "os"

func ForCurrentPlatform(configDir string) Manager {
	return ForCurrentPlatformWithBootstrap(configDir, BootstrapOptions{})
}

func ForCurrentPlatformWithBootstrap(configDir string, _ BootstrapOptions) Manager {
	binary, _ := os.Executable()
	return &LaunchAgentManager{ConfigDir: configDir, BinaryPath: binary}
}
