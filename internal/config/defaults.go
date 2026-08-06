package config

import "time"

func ApplyDefaults(cfg *Config) error {
	if cfg.Server.APIListen == "" {
		cfg.Server.APIListen = "0.0.0.0:8050"
	}
	if cfg.Server.DashboardListen == "" {
		cfg.Server.DashboardListen = "127.0.0.1:8051"
	}
	if cfg.Server.ReadHeaderTimeout == 0 {
		cfg.Server.ReadHeaderTimeout = Duration(time.Minute)
	}
	if cfg.Server.ShutdownTimeout == 0 {
		cfg.Server.ShutdownTimeout = Duration(30 * time.Second)
	}
	if cfg.Exposure.Mode == "" {
		cfg.Exposure.Mode = "manual"
	}
	if cfg.Storage.ArtifactRetention == 0 {
		cfg.Storage.ArtifactRetention = Duration(168 * time.Hour)
	}
	if cfg.Storage.StateDir == "" {
		stateDir, err := DefaultStateDir()
		if err != nil {
			return err
		}
		cfg.Storage.StateDir = stateDir
	}
	if cfg.Android.AgentImage == "" {
		cfg.Android.AgentImage = "ghcr.io/forkbombeu/credimi-runner-agent:latest"
	}
	if cfg.Android.AgentPullPolicy == "" {
		cfg.Android.AgentPullPolicy = "if-not-present"
	}
	if cfg.Android.AgentHostPort == 0 {
		cfg.Android.AgentHostPort = 8060
	}
	if cfg.Android.AgentContainerPort == 0 {
		cfg.Android.AgentContainerPort = 8060
	}
	if cfg.Android.AgentNetwork == "" {
		cfg.Android.AgentNetwork = "credimi-runner"
	}
	if cfg.Android.CommonDataVolume == "" {
		cfg.Android.CommonDataVolume = "credimi-runner-agent-data"
	}
	if cfg.Android.ToolCacheVolume == "" {
		cfg.Android.ToolCacheVolume = "credimi-runner-agent-tools"
	}
	return nil
}
