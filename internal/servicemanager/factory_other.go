//go:build !darwin && !windows

package servicemanager

import "os"

func ForCurrentPlatform(configDir string) Manager {
	binary, _ := os.Executable()
	return NewDockerManager(configDir, binary)
}
