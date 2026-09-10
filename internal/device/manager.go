package device

import (
	"context"
	"runtime"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

// Manager applies inventory changes in-process. It deliberately owns only
// validation and registry replacement; Temporal mobile business logic remains
// in Credimi activities.
type Manager struct {
	Registry *Registry
	Apply    func(context.Context, []config.DeviceConfig) error
}

func (m *Manager) Reconcile(ctx context.Context, devices []config.DeviceConfig) error {
	if m == nil || m.Registry == nil {
		return ErrInvalidDeviceID
	}
	if err := validateInventory(devices); err != nil {
		return err
	}
	if m.Apply != nil {
		if err := m.Apply(ctx, devices); err != nil {
			return err
		}
	}
	return m.Registry.Replace(devices)
}

func validateInventory(devices []config.DeviceConfig) error {
	cfg := config.Bootstrap()
	cfg.Runner.ID = "inventory/runner"
	cfg.Runner.Name, cfg.Runner.Organization = "runner", "inventory"
	cfg.Credimi.URL = "https://credimi.example"
	cfg.Credimi.AuthMode, cfg.Credimi.UserAPIKey = "user", "key"
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen, cfg.Server.DashboardListen = "127.0.0.1:8050", "127.0.0.1:8051"
	cfg.Server.ReadHeaderTimeout, cfg.Server.ShutdownTimeout = config.Duration(1), config.Duration(1)
	cfg.Exposure.Mode = "quick_tunnel"
	cfg.Storage.StateDir = "/tmp/credimi-runner"
	cfg.Storage.ArtifactRetention = config.Duration(1)
	cfg.Android.RunnerImage = "runner:latest"
	cfg.Android.PullPolicy, cfg.Android.Network = "never", "credimi-runner"
	cfg.Android.StateVolume, cfg.Android.ToolCacheVolume, cfg.Android.SDKVolume = "state", "tools", "sdk"
	// Device validation is platform-sensitive; callers that need a different
	// host platform should use config.ValidateForPlatform directly.
	cfg.Devices = devices
	return config.ValidateForPlatform(cfg, runtime.GOOS)
}
