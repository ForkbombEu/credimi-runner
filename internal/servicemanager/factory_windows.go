//go:build windows

package servicemanager

func ForCurrentPlatform(configDir string) Manager { return LaunchAgentManager{ConfigDir: configDir} }
