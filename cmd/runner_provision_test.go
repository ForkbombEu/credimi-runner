package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/spf13/cobra"
)

type fakeContainerLauncherManager struct {
	started, stopped, closed int
	startErr                 error
}

func (m *fakeContainerLauncherManager) Start(context.Context) error {
	m.started++
	return m.startErr
}

func (m *fakeContainerLauncherManager) Stop(context.Context) error {
	m.stopped++
	return nil
}

func (m *fakeContainerLauncherManager) Close() error {
	m.closed++
	return nil
}

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

func TestHydrateTypedRuntimeEnvironmentUsesTOMLAsSource(t *testing.T) {
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen, cfg.Server.DashboardListen = "127.0.0.1:8050", "127.0.0.1:8051"
	cfg.Storage.StateDir, cfg.Storage.TempDir = filepath.Join(dir, "state"), filepath.Join(dir, "tmp")
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "credimi-runner:local", PullPolicy: "never", Network: "runner-net", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	cfg.Devices = []runnerconfig.DeviceConfig{{ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone:5555"}}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := hydrateTypedRuntimeEnvironment(dir); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"CREDIMI_RUNNER_ID": "acme/runner", "ANDROID_RUNNER_IMAGE": "credimi-runner:local", "CREDIMI_DEVICE_1_SERIAL": "phone:5555",
	} {
		if got := os.Getenv(key); got != want {
			t.Fatalf("%s=%q, want %q", key, got, want)
		}
	}
}

func TestConfigureInternalListenersUsesTypedAPIAddress(t *testing.T) {
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen = "127.0.0.1:9123"
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	oldHost, oldPort := host, port
	t.Cleanup(func() { host, port = oldHost, oldPort })
	if err := configureInternalListeners(dir); err != nil {
		t.Fatal(err)
	}
	if host != "0.0.0.0" || port != 9123 {
		t.Fatalf("internal listeners = %s:%d", host, port)
	}
}

func TestPrepareInternalRuntimeRefreshesConfigBeforeStartingServices(t *testing.T) {
	oldHost := host
	t.Cleanup(func() { host = oldHost })
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen, cfg.Server.DashboardListen = "127.0.0.1:8050", "127.0.0.1:8051"
	cfg.Storage.StateDir, cfg.Storage.TempDir = filepath.Join(dir, "state"), filepath.Join(dir, "tmp")
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "runner:local", PullPolicy: "never", Network: "network", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	cfg.Devices = []runnerconfig.DeviceConfig{{ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone"}}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "sdkmanager"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("ANDROID_SDK_ROOT", filepath.Join(dir, "sdk"))
	if err := prepareInternalRuntime(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if host != "0.0.0.0" || os.Getenv("CREDIMI_RUNNER_ID") != "acme/runner" {
		t.Fatalf("prepared runtime host=%q runner=%q", host, os.Getenv("CREDIMI_RUNNER_ID"))
	}
}

func TestRunPublicUsesContainerLauncherForFirstRun(t *testing.T) {
	oldConfigDir, oldConfigPath, oldOpen, oldSignal, oldFactory := dashboardConfigDir, configPath, dashboardOpen, dashboardSignalSource, newContainerLauncherManager
	t.Cleanup(func() {
		dashboardConfigDir, configPath, dashboardOpen, dashboardSignalSource, newContainerLauncherManager = oldConfigDir, oldConfigPath, oldOpen, oldSignal, oldFactory
	})
	dashboardConfigDir = t.TempDir()
	configPath = ""
	dashboardOpen = false
	dashboardSignalSource = func() (<-chan os.Signal, func()) {
		signals := make(chan os.Signal, 1)
		signals <- os.Interrupt
		return signals, func() {}
	}
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, _ string, values dashboardruntime.Values) containerLauncherManager {
		if values["ANDROID_RUNNER_IMAGE"] == "" {
			t.Error("container launcher received no normalized runtime values")
		}
		return manager
	}
	command := &cobra.Command{}
	command.SetContext(context.Background())
	if err := runPublic(command, nil); err != nil {
		t.Fatal(err)
	}
	if manager.started != 1 || manager.stopped != 1 || manager.closed != 1 {
		t.Fatalf("launcher lifecycle = started:%d stopped:%d closed:%d", manager.started, manager.stopped, manager.closed)
	}
}

func TestRunInternalRuntimePreparesTypedConfigBeforeStartingServer(t *testing.T) {
	oldConfigDir, oldConfigPath, oldDashboard, oldServer, oldHost := dashboardConfigDir, configPath, runInternalDashboardFunc, runInternalServerFunc, host
	t.Cleanup(func() {
		dashboardConfigDir, configPath, runInternalDashboardFunc, runInternalServerFunc, host = oldConfigDir, oldConfigPath, oldDashboard, oldServer, oldHost
	})
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen = "127.0.0.1:8050"
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "runner:local", PullPolicy: "never", SDKVolume: "sdk"}
	cfg.Devices = []runnerconfig.DeviceConfig{{ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone"}}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	sdkBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(sdkBin, "sdkmanager"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sdkBin)
	t.Setenv("ANDROID_SDK_ROOT", filepath.Join(dir, "sdk"))
	dashboardConfigDir, configPath = dir, ""
	serverStarted := make(chan struct{})
	runInternalDashboardFunc = func(cmd *cobra.Command, _ []string) error {
		<-cmd.Context().Done()
		return cmd.Context().Err()
	}
	runInternalServerFunc = func(*cobra.Command, []string) error {
		close(serverStarted)
		return nil
	}
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	command.SetContext(ctx)
	err := runInternalRuntime(command, nil)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-serverStarted:
	default:
		t.Fatal("internal server was not started after typed config preparation")
	}
	if host != oldHost {
		t.Fatalf("internal runtime did not restore host: got %q want %q", host, oldHost)
	}
}
