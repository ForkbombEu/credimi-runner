//go:build darwin

package servicemanager

func ForCurrentPlatform(configDir string) Manager {
	return ForCurrentPlatformWithBootstrap(configDir, BootstrapOptions{})
}

func ForCurrentPlatformWithBootstrap(configDir string, _ BootstrapOptions) Manager {
	return &LaunchAgentManager{ConfigDir: configDir}
}
