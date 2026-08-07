package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

func TestProvisionInternalRuntimeToleratesMissingOrEmptyConfig(t *testing.T) {
	if err := provisionInternalRuntimeAt(context.Background(), t.TempDir(), filepath.Join(t.TempDir(), "sdk")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen, cfg.Server.DashboardListen = "127.0.0.1:8050", "127.0.0.1:8051"
	cfg.Storage.StateDir, cfg.Storage.ArtifactRetention = filepath.Join(dir, "state"), runnerconfig.Duration(1)
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "runner", PullPolicy: "never", Network: "network", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := provisionInternalRuntime(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionInternalRuntimeReportsMissingSDKManager(t *testing.T) {
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen, cfg.Server.DashboardListen = "127.0.0.1:8050", "127.0.0.1:8051"
	cfg.Storage.StateDir, cfg.Storage.ArtifactRetention = filepath.Join(dir, "state"), runnerconfig.Duration(1)
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "runner", PullPolicy: "never", Network: "network", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	cfg.Devices = []runnerconfig.DeviceConfig{{ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone"}}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if err := provisionInternalRuntimeAt(context.Background(), dir, filepath.Join(dir, "sdk")); err == nil || !strings.Contains(err.Error(), "sdkmanager is unavailable") {
		t.Fatalf("provisioning error = %v", err)
	}
}
