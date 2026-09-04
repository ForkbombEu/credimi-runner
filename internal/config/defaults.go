package config

import (
	"path/filepath"
	"time"
)

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
	if cfg.Storage.TempDir == "" {
		cfg.Storage.TempDir = filepath.Join(cfg.Storage.StateDir, "tmp")
	}
	if cfg.Android.RunnerImage == "" {
		cfg.Android.RunnerImage = "ghcr.io/forkbombeu/credimi-runner:latest"
	}
	if cfg.Android.PullPolicy == "" {
		cfg.Android.PullPolicy = "if-not-present"
	}
	if cfg.Android.Network == "" {
		cfg.Android.Network = "credimi-runner"
	}
	if cfg.Android.StateVolume == "" {
		cfg.Android.StateVolume = "credimi-runner-state"
	}
	if cfg.Android.ToolCacheVolume == "" {
		cfg.Android.ToolCacheVolume = "credimi-runner-tools"
	}
	if cfg.Android.SDKVolume == "" {
		cfg.Android.SDKVolume = "credimi-runner-sdk"
	}
	return nil
}
