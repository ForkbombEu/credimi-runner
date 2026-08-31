//go:build darwin

package servicemanager

import "os"

func ForCurrentPlatform(configDir string) Manager {
	binary, _ := os.Executable()
	return &LaunchAgentManager{ConfigDir: configDir, BinaryPath: binary}
}
