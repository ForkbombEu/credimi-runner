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
