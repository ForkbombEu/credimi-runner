package server

import (
	"path/filepath"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestManagedWorkflowRootFromEnvironment(t *testing.T) {
	t.Run("uses configured temp directory", func(t *testing.T) {
		t.Setenv("CREDIMI_DIR", "")
		t.Setenv("CREDIMI_TEMP_DIR", "/tmp/credimi-runner-tmp/")
		deps := Deps{}

		deps.WithDefaults()

		require.Equal(t, "/tmp/credimi-runner-tmp/workflows", deps.ManagedWorkflowRoot)
	})

	t.Run("matches credimi directory precedence", func(t *testing.T) {
		t.Setenv("CREDIMI_DIR", "/custom/credimi")
		t.Setenv("CREDIMI_TEMP_DIR", "/tmp/credimi-runner-tmp")

		require.Equal(t, "/custom/credimi/workflows", managedWorkflowRootFromEnvironment())
	})

	t.Run("keeps docker default", func(t *testing.T) {
		t.Setenv("CREDIMI_DIR", "")
		t.Setenv("CREDIMI_TEMP_DIR", " ")

		require.Equal(t, filepath.Join(defaultCredimiRoot, "workflows"), managedWorkflowRootFromEnvironment())
	})

	t.Run("does not override injected root", func(t *testing.T) {
		t.Setenv("CREDIMI_TEMP_DIR", "/tmp/credimi-runner-tmp")
		deps := Deps{ManagedWorkflowRoot: "test-workflows"}

		deps.WithDefaults()

		require.Equal(t, "test-workflows", deps.ManagedWorkflowRoot)
	})
}

func TestNewRunnerServiceWithDepsLoadsConfiguredRuntimeWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", dir)
	cfg := runnerconfig.Bootstrap()
	cfg.SchemaVersion = runnerconfig.SchemaVersion
	cfg.Runner.ID = "acme/runner"
	cfg.Runner.Name = "Runner"
	cfg.Runner.Organization = "acme"
	cfg.Credimi.URL = "https://credimi.example"
	cfg.Credimi.UserAPIKey = "key"
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.ReadHeaderTimeout = runnerconfig.Duration(1)
	cfg.Server.ShutdownTimeout = runnerconfig.Duration(1)
	cfg.Storage.StateDir = filepath.Join(dir, "state")
	cfg.Storage.ArtifactRetention = runnerconfig.Duration(1)
	cfg.Android.RunnerImage = "runner:latest"
	cfg.Android.PullPolicy = "never"
	cfg.Android.Network = "runner-net"
	cfg.Android.StateVolume = "state"
	cfg.Android.ToolCacheVolume = "tools"
	cfg.Android.SDKVolume = "sdk"
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "usb-1"},
	}}
	configPath := filepath.Join(dir, "config.toml")
	require.NoError(t, runnerconfig.WriteFile(configPath, cfg))
	_, err := dashboardruntime.RuntimeConfigFromEnvironment()
	require.NoError(t, err)
	service := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{}, Deps{})
	require.NotNil(t, service.Deps.RuntimeConfig)
	require.Equal(t, "acme/runner", service.Deps.RuntimeConfig.Host["CREDIMI_RUNNER_ID"])
}

func TestNewRunnerServiceWithDepsReportsMissingConfiguredRuntimeOnLoad(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", t.TempDir())
	service := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{}, Deps{})
	require.Nil(t, service.Deps.RuntimeConfig)
	require.NotNil(t, service.Deps.RuntimeConfigLoader)
	_, err := service.Deps.RuntimeConfigLoader()
	require.Error(t, err)
}

func TestNewRunnerServiceWithDepsPrefersExplicitSnapshotLoader(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", dir)
	fileConfig := runnerconfig.Bootstrap()
	fileConfig.SchemaVersion = runnerconfig.SchemaVersion
	fileConfig.Runner = runnerconfig.RunnerConfig{ID: "org/file-config", Name: "file-config", Organization: "org"}
	fileConfig.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	fileConfig.Temporal.Address = "temporal:7233"
	fileConfig.Devices = []runnerconfig.DeviceConfig{{ID: "org/file-config/device", Name: "File", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "no_device"}}}
	require.NoError(t, runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), fileConfig))

	want := dashboardruntime.RunnerRuntimeConfig{
		Host:    dashboardruntime.Values{"CREDIMI_RUNNER_ID": "org/snapshot-config"},
		Devices: []dashboardruntime.DeviceRuntimeConfig{{ID: "org/snapshot-device", Name: "Snapshot", Type: "android_phone", Enabled: true}},
	}
	service := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{}, Deps{
		RuntimeConfigLoader: func() (dashboardruntime.RunnerRuntimeConfig, error) { return want, nil },
	})
	got, err := service.currentRuntimeConfig()
	require.NoError(t, err)
	require.Equal(t, want, got)
}
