package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/launcher"
	"github.com/spf13/cobra"
)

type fakeContainerLauncherManager struct {
	mu                       sync.Mutex
	started, stopped, closed int
	recreated                int
	updated                  int
	startErr                 error
	configured               []dashboardruntime.Values
	verifiedURLs             []string
	composePath              string
	composeAtStop            string
}

func (m *fakeContainerLauncherManager) Start(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started++
	return m.startErr
}

func (m *fakeContainerLauncherManager) Stop(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped++
	if m.composePath != "" {
		if content, err := os.ReadFile(m.composePath); err == nil {
			m.composeAtStop = string(content)
		}
	}
	return nil
}

func (m *fakeContainerLauncherManager) RecreateRunner(context.Context, bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recreated++
	m.started++
	return nil
}

func (m *fakeContainerLauncherManager) UpdateImage(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updated++
	return nil
}

func (m *fakeContainerLauncherManager) Configure(values dashboardruntime.Values) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configured = append(m.configured, values)
}

func (m *fakeContainerLauncherManager) QuickTunnelURL(context.Context) (string, error) {
	return "https://example.trycloudflare.com", nil
}

func (m *fakeContainerLauncherManager) VerifyPublicURL(_ context.Context, publicURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verifiedURLs = append(m.verifiedURLs, publicURL)
	return nil
}

func (m *fakeContainerLauncherManager) Status(context.Context) dashboardruntime.RuntimeStatus {
	return dashboardruntime.RuntimeStatus{}
}

func (m *fakeContainerLauncherManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed++
	return nil
}

func (m *fakeContainerLauncherManager) snapshot() (int, int, int, []dashboardruntime.Values) {
	m.mu.Lock()
	defer m.mu.Unlock()
	configured := append([]dashboardruntime.Values(nil), m.configured...)
	return m.started, m.stopped, m.closed, configured
}

func (m *fakeContainerLauncherManager) composeAtStopSnapshot() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.composeAtStop
}

func (m *fakeContainerLauncherManager) updateCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updated
}

func (m *fakeContainerLauncherManager) verifiedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.verifiedURLs)
}

func (m *fakeContainerLauncherManager) recreatedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recreated
}

func TestRunContainerLauncherRestartRequiredRecreatesRunnerOnly(t *testing.T) {
	oldOpen, oldSignal, oldFactory := dashboardOpen, dashboardSignalSource, newContainerLauncherManager
	t.Cleanup(func() {
		dashboardOpen, dashboardSignalSource, newContainerLauncherManager = oldOpen, oldSignal, oldFactory
	})
	dashboardOpen = false
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal-a.example:7233"
	cfg.Server.APIListen = "127.0.0.1:19050"
	cfg.Exposure.Mode = "quick_tunnel"
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "published:stable", PullPolicy: "never", Network: "network", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone:5555"},
	}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := dashboard.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, configDir string, _ dashboardruntime.Values) containerLauncherManager {
		manager.composePath = filepath.Join(configDir, "docker-compose.yaml")
		return manager
	}
	signals := make(chan os.Signal, 1)
	dashboardSignalSource = func() (<-chan os.Signal, func()) { return signals, func() {} }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	done := make(chan error, 1)
	go func() { done <- runContainerLauncher(command, dir, loaded.Snapshot()) }()
	socket := filepath.Join(dir, "control.sock")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("container launcher exited before control socket: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("control socket was not created: %v", err)
	}

	cfg.Temporal.Address = "temporal-b.example:7233"
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := launcher.RequestReconcile(context.Background(), socket); err != nil {
		t.Fatal(err)
	}
	if manager.recreatedCount() != 1 {
		t.Fatalf("runner recreations = %d, want 1", manager.recreatedCount())
	}
	started, stopped, _, _ := manager.snapshot()
	if started != 2 || stopped != 0 {
		t.Fatalf("restart-required lifecycle = started %d stopped %d", started, stopped)
	}
	if got, err := launcher.ReadQuickTunnelURL(dir); err != nil || got != "https://example.trycloudflare.com" {
		t.Fatalf("quick tunnel URL changed during runner-only restart: %q, %v", got, err)
	}

	signals <- syscall.SIGTERM
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("container launcher did not stop")
	}
}

func TestExecutionRuntimeRunningReadsOnlyOperationalStates(t *testing.T) {
	dir := t.TempDir()
	if !executionRuntimeRunning(dir) {
		t.Fatal("missing runtime state should retain startup compatibility")
	}
	for _, state := range []string{"running", "starting", "restarting"} {
		if err := os.WriteFile(filepath.Join(dir, "runtime-state"), []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
		if !executionRuntimeRunning(dir) {
			t.Fatalf("state %q was not operational", state)
		}
	}
	for _, state := range []string{"stopped", "failed: adb missing", "paused"} {
		if err := os.WriteFile(filepath.Join(dir, "runtime-state"), []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
		if executionRuntimeRunning(dir) {
			t.Fatalf("state %q was operational", state)
		}
	}
}

func TestRuntimePlanHasQuickTunnelRequiresAutoTunnelService(t *testing.T) {
	for _, test := range []struct {
		name string
		plan dashboardruntime.RuntimePlan
		want bool
	}{
		{name: "auto tunnel", plan: dashboardruntime.RuntimePlan{ServiceMode: "auto", ComposeServices: []string{"runner", "caddy", "tunnel"}}, want: true},
		{name: "bootstrap runner only", plan: dashboardruntime.RuntimePlan{ServiceMode: "bootstrap", ComposeServices: []string{"runner"}}},
		{name: "auto without tunnel", plan: dashboardruntime.RuntimePlan{ServiceMode: "auto", ComposeServices: []string{"runner"}}},
		{name: "managed named tunnel", plan: dashboardruntime.RuntimePlan{ServiceMode: "cloudflare-managed", ComposeServices: []string{"runner", "caddy", "tunnel_named"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := runtimePlanHasQuickTunnel(test.plan); got != test.want {
				t.Fatalf("runtimePlanHasQuickTunnel(%#v) = %t, want %t", test.plan, got, test.want)
			}
		})
	}
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

func TestRunContainerLauncherCarriesBootstrapHostContext(t *testing.T) {
	oldImage, oldPolicy, oldOpen, oldSignal, oldFactory := bootstrapImage, bootstrapPullPolicy, dashboardOpen, dashboardSignalSource, newContainerLauncherManager
	t.Cleanup(func() {
		bootstrapImage, bootstrapPullPolicy, dashboardOpen, dashboardSignalSource, newContainerLauncherManager = oldImage, oldPolicy, oldOpen, oldSignal, oldFactory
	})
	bootstrapImage, bootstrapPullPolicy, dashboardOpen = "credimi-runner:local", "never", false
	var received dashboardruntime.Values
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, _ string, values dashboardruntime.Values) containerLauncherManager {
		received = values
		return manager
	}
	done := make(chan os.Signal, 1)
	done <- syscall.SIGTERM
	dashboardSignalSource = func() (<-chan os.Signal, func()) { return done, func() {} }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	if err := runContainerLauncher(command, t.TempDir(), map[string]string{"ANDROID_RUNNER_IMAGE": "credimi-runner:local", "ANDROID_PULL_POLICY": "never", dashboardruntime.BootstrapPhaseEnv: "true"}); err != nil {
		t.Fatal(err)
	}
	if manager.started != 1 || manager.stopped != 1 || manager.closed != 1 {
		t.Fatalf("launcher lifecycle = %#v", manager)
	}
	for _, key := range []string{dashboardruntime.ConfigOwnerUIDEnv, dashboardruntime.ConfigOwnerGIDEnv, dashboardruntime.HostHomeEnv, dashboardruntime.HostAndroidDirEnv, dashboardruntime.HostGoldenRootEnv} {
		if received[key] == "" {
			t.Fatalf("bootstrap context key %s was not propagated: %#v", key, received)
		}
	}
	if received[dashboardruntime.BootstrapHostNetworkEnv] != "true" {
		t.Fatalf("bootstrap host network = %q", received[dashboardruntime.BootstrapHostNetworkEnv])
	}
	if manager.verifiedCount() != 0 {
		t.Fatalf("bootstrap launcher attempted quick tunnel verification %d times", manager.verifiedCount())
	}
}

func TestRunContainerLauncherReplacesStaleSetupHandoffBeforeStartingRuntime(t *testing.T) {
	oldOpen, oldSignal, oldFactory := dashboardOpen, dashboardSignalSource, newContainerLauncherManager
	t.Cleanup(func() {
		dashboardOpen, dashboardSignalSource, newContainerLauncherManager = oldOpen, oldSignal, oldFactory
	})
	dashboardOpen = false
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen = "127.0.0.1:8050"
	cfg.Exposure.Mode = "manual"
	cfg.Exposure.PublicURL = "https://runner.example"
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "credimi-runner:local", PullPolicy: "never", Network: "network", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "usb-1"},
	}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "setup-pending"), []byte("pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "setup-operation"), []byte("reconcile-setup-old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := dashboard.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, _ string, _ dashboardruntime.Values) containerLauncherManager { return manager }
	signals := make(chan os.Signal, 1)
	dashboardSignalSource = func() (<-chan os.Signal, func()) { return signals, func() {} }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	done := make(chan error, 1)
	go func() { done <- runContainerLauncher(command, dir, loaded.Snapshot()) }()
	operationPath := filepath.Join(dir, "setup-operation")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(operationPath)
		if err == nil && strings.TrimSpace(string(raw)) != "reconcile-setup-old" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	raw, err := os.ReadFile(operationPath)
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(raw)), launcher.ReconcileSetup+"-") {
		t.Fatalf("fresh setup handoff = %q, %v", raw, err)
	}
	operationID := strings.TrimSpace(string(raw))
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result, statusErr := launcher.RequestOperationStatus(context.Background(), filepath.Join(dir, "control.sock"), operationID)
		if statusErr == nil && result.Phase == launcher.PhaseSucceeded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err := launcher.RequestOperationStatus(context.Background(), filepath.Join(dir, "control.sock"), operationID)
	if err != nil || result.Phase != launcher.PhaseSucceeded {
		select {
		case runErr := <-done:
			t.Fatalf("container launcher exited: %v; pending setup operation = %#v, %v", runErr, result, err)
		default:
		}
		t.Fatalf("pending setup operation = %#v, %v", result, err)
	}
	signals <- syscall.SIGTERM
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("container launcher did not stop")
	}
}

func TestRunContainerLauncherClearsStaleQuickTunnelURLBeforeStart(t *testing.T) {
	oldOpen, oldSignal, oldFactory := dashboardOpen, dashboardSignalSource, newContainerLauncherManager
	t.Cleanup(func() {
		dashboardOpen, dashboardSignalSource, newContainerLauncherManager = oldOpen, oldSignal, oldFactory
	})
	dashboardOpen = false
	dir := t.TempDir()
	if err := launcher.WriteQuickTunnelURL(dir, "https://stale.trycloudflare.com"); err != nil {
		t.Fatal(err)
	}
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, _ string, _ dashboardruntime.Values) containerLauncherManager { return manager }
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	dashboardSignalSource = func() (<-chan os.Signal, func()) { return signals, func() {} }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	if err := runContainerLauncher(command, dir, map[string]string{"CREDIMI_SERVICE_MODE": "manual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "quick-tunnel-url")); !os.IsNotExist(err) {
		t.Fatalf("stale quick tunnel URL survived launcher start: %v", err)
	}
}

func TestRunContainerLauncherRuntimeStartRestartsOuterRuntime(t *testing.T) {
	oldOpen, oldSignal, oldFactory := dashboardOpen, dashboardSignalSource, newContainerLauncherManager
	t.Cleanup(func() {
		dashboardOpen, dashboardSignalSource, newContainerLauncherManager = oldOpen, oldSignal, oldFactory
	})
	dashboardOpen = false
	dir := t.TempDir()
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, _ string, _ dashboardruntime.Values) containerLauncherManager { return manager }
	signals := make(chan os.Signal, 1)
	dashboardSignalSource = func() (<-chan os.Signal, func()) { return signals, func() {} }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runContainerLauncher(command, dir, map[string]string{"CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://runner.example"})
	}()
	socket := filepath.Join(dir, "control.sock")
	waitForPath(t, socket)
	handle, err := launcher.RequestRuntimeActionAsync(context.Background(), socket, "start")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := launcher.RequestOperationStatus(context.Background(), socket, handle.ID)
		if statusErr == nil && status.Phase == launcher.PhaseSucceeded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, err := launcher.RequestOperationStatus(context.Background(), socket, handle.ID)
	if err != nil || status.Phase != launcher.PhaseSucceeded {
		t.Fatalf("outer runtime start status = %#v, err=%v", status, err)
	}
	started, _, _, _ := manager.snapshot()
	if started < 2 {
		t.Fatalf("outer runtime start did not restart manager: starts=%d", started)
	}
	signals <- syscall.SIGTERM
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("container launcher did not stop")
	}
}

func TestRunContainerLauncherRuntimeStopKeepsControlPlaneAndClearsURL(t *testing.T) {
	oldOpen, oldSignal, oldFactory := dashboardOpen, dashboardSignalSource, newContainerLauncherManager
	t.Cleanup(func() {
		dashboardOpen, dashboardSignalSource, newContainerLauncherManager = oldOpen, oldSignal, oldFactory
	})
	dashboardOpen = false
	dir := t.TempDir()
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, _ string, _ dashboardruntime.Values) containerLauncherManager { return manager }
	signals := make(chan os.Signal, 1)
	dashboardSignalSource = func() (<-chan os.Signal, func()) { return signals, func() {} }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runContainerLauncher(command, dir, map[string]string{"CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://runner.example"})
	}()
	socket := filepath.Join(dir, "control.sock")
	waitForPath(t, socket)
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if raw, err := os.ReadFile(filepath.Join(dir, "runtime-control")); err == nil && strings.TrimSpace(string(raw)) == "stop" {
				_ = writeRuntimeState(dir, "stopped")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	handle, err := launcher.RequestRuntimeActionAsync(context.Background(), socket, "stop")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := launcher.RequestOperationStatus(context.Background(), socket, handle.ID)
		if statusErr == nil && status.Phase == launcher.PhaseSucceeded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, err := launcher.RequestOperationStatus(context.Background(), socket, handle.ID)
	if err != nil || status.Phase != launcher.PhaseSucceeded {
		t.Fatalf("outer runtime stop status = %#v, err=%v", status, err)
	}
	if stopped, _, _, _ := manager.snapshot(); stopped < 1 {
		t.Fatalf("outer runtime stop did not stop exposure services: stops=%d", stopped)
	}
	if _, err := launcher.ReadQuickTunnelURL(dir); err == nil {
		t.Fatal("quick tunnel state survived explicit stop")
	}
	signals <- syscall.SIGTERM
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("container launcher did not stop")
	}
}

func TestRunContainerLauncherReconcilesLatestTOMLThroughLauncher(t *testing.T) {
	oldImage, oldPolicy, oldOpen, oldSignal, oldFactory := bootstrapImage, bootstrapPullPolicy, dashboardOpen, dashboardSignalSource, newContainerLauncherManager
	t.Cleanup(func() {
		bootstrapImage, bootstrapPullPolicy, dashboardOpen, dashboardSignalSource, newContainerLauncherManager = oldImage, oldPolicy, oldOpen, oldSignal, oldFactory
	})
	bootstrapImage, bootstrapPullPolicy, dashboardOpen = "credimi-runner:local", "never", false
	dir := t.TempDir()
	signals := make(chan os.Signal, 1)
	dashboardSignalSource = func() (<-chan os.Signal, func()) { return signals, func() {} }
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, configDir string, _ dashboardruntime.Values) containerLauncherManager {
		manager.composePath = filepath.Join(configDir, "docker-compose.yaml")
		return manager
	}
	command := &cobra.Command{}
	command.SetContext(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- runContainerLauncher(command, dir, map[string]string{
			"ANDROID_RUNNER_IMAGE": "credimi-runner:local",
			"ANDROID_PULL_POLICY":  "never",
		})
	}()
	socket := filepath.Join(dir, "control.sock")
	waitForPath(t, socket)

	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen = "127.0.0.1:19050"
	cfg.Exposure.Mode = "quick_tunnel"
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "published:stable", PullPolicy: "if-not-present", Network: "network", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone:5555"},
	}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := dashboard.LoadConfig(dir)
	if err != nil {
		t.Fatalf("saved TOML cannot be loaded for reconciliation: %v", err)
	}
	if _, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(hostBootstrapContext(false).Apply(loaded.Snapshot())), "linux"); err != nil {
		t.Fatalf("saved TOML cannot be normalized for reconciliation: %v", err)
	}
	if err := launcher.RequestReconcile(context.Background(), socket); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		started, stopped, _, configured := manager.snapshot()
		if len(configured) == 1 && started >= 2 && stopped >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	started, stopped, _, configured := manager.snapshot()
	if len(configured) != 1 || started < 2 || stopped < 1 {
		t.Fatalf("reconciliation state: started=%d stopped=%d configured=%d", started, stopped, len(configured))
	}
	if got := configured[0]["ANDROID_RUNNER_IMAGE"]; got != "published:stable" {
		t.Fatalf("reconciliation used bootstrap image instead of TOML: %q", got)
	}
	if manager.verifiedCount() == 0 {
		t.Fatalf("quick tunnel URL was not verified: %#v", manager.verifiedURLs)
	}
	composeAtStop := manager.composeAtStopSnapshot()
	if !strings.Contains(composeAtStop, "  caddy:\n") || !strings.Contains(composeAtStop, "  tunnel:\n") {
		t.Fatalf("final Compose topology was not written before bootstrap stop:\n%s", composeAtStop)
	}
	if got, err := launcher.ReadQuickTunnelURL(dir); err != nil || got != "https://example.trycloudflare.com" {
		t.Fatalf("launcher did not publish the reconciled quick tunnel URL: %q, %v", got, err)
	}
	signals <- syscall.SIGTERM
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("container launcher did not stop")
	}
}

func TestRunContainerLauncherReportsComposeWriteFailureDuringReconcile(t *testing.T) {
	oldImage, oldPolicy, oldOpen, oldSignal, oldFactory, oldWrite := bootstrapImage, bootstrapPullPolicy, dashboardOpen, dashboardSignalSource, newContainerLauncherManager, writeComposeFileForOS
	t.Cleanup(func() {
		bootstrapImage, bootstrapPullPolicy, dashboardOpen, dashboardSignalSource, newContainerLauncherManager, writeComposeFileForOS = oldImage, oldPolicy, oldOpen, oldSignal, oldFactory, oldWrite
	})
	bootstrapImage, bootstrapPullPolicy, dashboardOpen = "credimi-runner:local", "never", false
	want := errors.New("compose file is not writable")
	writeComposeFileForOS = func(string, dashboardruntime.Values, string) error { return want }
	dir := t.TempDir()
	signals := make(chan os.Signal, 1)
	dashboardSignalSource = func() (<-chan os.Signal, func()) { return signals, func() {} }
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, _ string, _ dashboardruntime.Values) containerLauncherManager { return manager }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- runContainerLauncher(command, dir, map[string]string{
			"ANDROID_RUNNER_IMAGE": "credimi-runner:local",
			"ANDROID_PULL_POLICY":  "never",
		})
	}()
	socket := filepath.Join(dir, "control.sock")
	waitForPath(t, socket)

	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen = "127.0.0.1:19050"
	cfg.Exposure.Mode = "quick_tunnel"
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "published:stable", PullPolicy: "if-not-present", Network: "network", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone:5555"},
	}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	err := launcher.RequestReconcile(context.Background(), socket)
	if err == nil || !strings.Contains(err.Error(), "write compose file for configuration reconciliation") || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("compose write failure = %v", err)
	}
	signals <- syscall.SIGTERM
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("container launcher did not stop")
	}
}

func TestRunContainerLauncherUpgradeRefreshesQuickTunnelState(t *testing.T) {
	oldOpen, oldSignal, oldFactory := dashboardOpen, dashboardSignalSource, newContainerLauncherManager
	t.Cleanup(func() {
		dashboardOpen, dashboardSignalSource, newContainerLauncherManager = oldOpen, oldSignal, oldFactory
	})
	dashboardOpen = false
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Exposure.Mode = "quick_tunnel"
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "published:stable", PullPolicy: "if-not-present", Network: "network", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone:5555"},
	}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := dashboard.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 1)
	dashboardSignalSource = func() (<-chan os.Signal, func()) { return signals, func() {} }
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, _ string, _ dashboardruntime.Values) containerLauncherManager { return manager }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- runContainerLauncher(command, dir, loaded.Snapshot()) }()
	socket := filepath.Join(dir, "control.sock")
	waitForPath(t, socket)
	if err := launcher.RequestUpgrade(context.Background(), socket); err != nil {
		t.Fatal(err)
	}
	if manager.updateCount() != 1 {
		t.Fatalf("image upgrades = %d, want 1", manager.updateCount())
	}
	if got, err := launcher.ReadQuickTunnelURL(dir); err != nil || got != "https://example.trycloudflare.com" {
		t.Fatalf("upgrade did not refresh quick tunnel URL: %q, %v", got, err)
	}
	signals <- syscall.SIGTERM
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("container launcher did not stop")
	}
}

func TestRunContainerLauncherReturnsManagerStartError(t *testing.T) {
	oldFactory := newContainerLauncherManager
	t.Cleanup(func() { newContainerLauncherManager = oldFactory })
	want := errors.New("docker daemon unavailable")
	manager := &fakeContainerLauncherManager{startErr: want}
	newContainerLauncherManager = func(_ string, _ string, _ dashboardruntime.Values) containerLauncherManager { return manager }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	err := runContainerLauncher(command, t.TempDir(), map[string]string{
		"ANDROID_RUNNER_IMAGE": "credimi-runner:local",
		"ANDROID_PULL_POLICY":  "never",
	})
	if !errors.Is(err, want) || manager.closed != 1 {
		t.Fatalf("launcher start error = %v, manager = %#v", err, manager)
	}
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	waitForCondition(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func TestProvisionInternalRuntimeUsesAuthoritativeEmulatorReadiness(t *testing.T) {
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Server.APIListen, cfg.Server.DashboardListen = "127.0.0.1:8050", "127.0.0.1:8051"
	cfg.Storage.StateDir, cfg.Storage.ArtifactRetention = filepath.Join(dir, "state"), runnerconfig.Duration(1)
	cfg.Android = runnerconfig.AndroidConfig{RunnerImage: "runner", PullPolicy: "never", Network: "network", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	cfg.Devices = []runnerconfig.DeviceConfig{{ID: "acme/runner/emulator", Name: "Emulator", Type: runnerconfig.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{AVDName: "credimi", BaseName: "credimi", GoldenSource: "/avd-golden/credimi-golden", APILevel: 35, ABI: "x86_64", MemoryMB: 2048, Cores: 2, SystemImage: "system-images;android-35;google_apis;x86_64"}}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	previousEnsure := ensureEmulatorRuntime
	var gotRoot, gotAVDName string
	ensureEmulatorRuntime = func(_ context.Context, got runnerconfig.Config, gotGOOS, root string, _ androidtools.EmulatorProgress) error {
		gotRoot = root
		if gotGOOS != "linux" {
			t.Fatalf("GOOS = %q", gotGOOS)
		}
		gotAVDName = got.Devices[0].AndroidEmulator.AVDName
		return errors.New("base AVD image is unavailable")
	}
	t.Cleanup(func() { ensureEmulatorRuntime = previousEnsure })
	sdkRoot := filepath.Join(dir, "sdk")
	if err := provisionInternalRuntimeAtForOS(context.Background(), dir, sdkRoot, "linux"); err == nil || !strings.Contains(err.Error(), "base AVD image is unavailable") {
		t.Fatalf("provisioning error = %v", err)
	}
	if gotRoot != sdkRoot || gotAVDName != "credimi" {
		t.Fatalf("emulator readiness input root=%q avd=%q", gotRoot, gotAVDName)
	}
}

func TestHydrateTypedRuntimeEnvironmentUsesTOMLAsSource(t *testing.T) {
	t.Setenv("CREDIMI_DEVICE_1_SERIAL", "")
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
		"CREDIMI_RUNNER_ID": "acme/runner", "ANDROID_RUNNER_IMAGE": "credimi-runner:local",
	} {
		if got := os.Getenv(key); got != want {
			t.Fatalf("%s=%q, want %q", key, got, want)
		}
	}
	if got := os.Getenv("CREDIMI_DEVICE_1_SERIAL"); got == "phone:5555" {
		t.Fatal("device-specific serial was hydrated into process-global environment")
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
	if err := os.WriteFile(filepath.Join(bin, "sdkmanager"), []byte("#!/bin/sh\n/usr/bin/mkdir -p \"$ANDROID_SDK_ROOT/platform-tools\"\n/usr/bin/touch \"$ANDROID_SDK_ROOT/platform-tools/adb\"\n"), 0o700); err != nil {
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

func TestApplyBootstrapValuesUsesLocalImageAndValidatesPolicy(t *testing.T) {
	oldImage, oldPolicy := bootstrapImage, bootstrapPullPolicy
	t.Cleanup(func() { bootstrapImage, bootstrapPullPolicy = oldImage, oldPolicy })
	bootstrapImage = "credimi-runner:local"
	bootstrapPullPolicy = "never"
	values := map[string]string{"ANDROID_RUNNER_IMAGE": "published", "ANDROID_PULL_POLICY": "always"}
	if err := applyBootstrapValues(values); err != nil {
		t.Fatal(err)
	}
	if values["ANDROID_RUNNER_IMAGE"] != "credimi-runner:local" || values["ANDROID_PULL_POLICY"] != "never" {
		t.Fatalf("bootstrap values = %#v", values)
	}
	bootstrapPullPolicy = "sometimes"
	if err := applyBootstrapValues(values); err == nil || !strings.Contains(err.Error(), "invalid bootstrap pull policy") {
		t.Fatalf("invalid policy error = %v", err)
	}
}

func TestEffectiveConfigDirPrefersExplicitDirectoryAndConfigPath(t *testing.T) {
	oldDir, oldPath := dashboardConfigDir, configPath
	t.Cleanup(func() { dashboardConfigDir, configPath = oldDir, oldPath })
	dashboardConfigDir = ""
	configPath = filepath.Join(t.TempDir(), "config.toml")
	if got, want := effectiveConfigDir(), filepath.Dir(configPath); got != want {
		t.Fatalf("config path directory = %q, want %q", got, want)
	}
	dashboardConfigDir = filepath.Join(t.TempDir(), "runner")
	if got := effectiveConfigDir(); got != dashboardConfigDir {
		t.Fatalf("explicit config directory = %q, want %q", got, dashboardConfigDir)
	}
}

func TestRunPublicRejectsInvalidBootstrapPolicyBeforeStartingDocker(t *testing.T) {
	oldDir, oldImage, oldPolicy := dashboardConfigDir, bootstrapImage, bootstrapPullPolicy
	t.Cleanup(func() { dashboardConfigDir, bootstrapImage, bootstrapPullPolicy = oldDir, oldImage, oldPolicy })
	dashboardConfigDir = t.TempDir()
	bootstrapImage, bootstrapPullPolicy = "credimi-runner:local", "sometimes"
	command := &cobra.Command{}
	command.SetContext(context.Background())
	err := runPublicForOS(command, nil, "linux")
	if err == nil || !strings.Contains(err.Error(), "invalid bootstrap pull policy") {
		t.Fatalf("invalid bootstrap policy error = %v", err)
	}
}

func TestReadActiveMobileActivitiesReadsLauncherGuardState(t *testing.T) {
	dir := t.TempDir()
	if readActiveMobileActivities(dir) {
		t.Fatal("missing activity state reported as busy")
	}
	if err := os.WriteFile(filepath.Join(dir, "active-mobile-activities"), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !readActiveMobileActivities(dir) {
		t.Fatal("active activity state was not reported as busy")
	}
	if err := os.WriteFile(filepath.Join(dir, "active-mobile-activities"), []byte("not-a-count\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if readActiveMobileActivities(dir) {
		t.Fatal("invalid activity state reported as busy")
	}
}

func TestRuntimeValuesFromConfigNormalizesTypedValues(t *testing.T) {
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner.ID, cfg.Runner.Name, cfg.Runner.Organization = "acme/runner", "runner", "acme"
	cfg.Credimi.URL = "https://credimi.example"
	cfg.Credimi.AuthMode, cfg.Credimi.UserAPIKey = "user", "key"
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Android.RunnerImage = "credimi-runner:local"
	cfg.Devices = []runnerconfig.DeviceConfig{{ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone:5555"}}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	values, err := runtimeValuesFromConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if values["ANDROID_RUNNER_IMAGE"] != "credimi-runner:local" || values["CREDIMI_RUNNER_ID"] != "acme/runner" {
		t.Fatalf("normalized runtime values = %#v", values)
	}
}

func TestRunPublicUsesTypedImageAfterSetupEvenWithBootstrapFlags(t *testing.T) {
	oldDir, oldImage, oldPolicy, oldOpen, oldSignals, oldFactory := dashboardConfigDir, bootstrapImage, bootstrapPullPolicy, dashboardOpen, dashboardSignalSource, newContainerLauncherManager
	t.Cleanup(func() {
		dashboardConfigDir, bootstrapImage, bootstrapPullPolicy, dashboardOpen, dashboardSignalSource, newContainerLauncherManager = oldDir, oldImage, oldPolicy, oldOpen, oldSignals, oldFactory
	})
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner.ID, cfg.Runner.Name, cfg.Runner.Organization = "acme/runner", "runner", "acme"
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Android.RunnerImage, cfg.Android.PullPolicy = "published:stable", "if-not-present"
	cfg.Devices = []runnerconfig.DeviceConfig{{ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone:5555"}}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	dashboardConfigDir, bootstrapImage, bootstrapPullPolicy = dir, "credimi-runner:local", "never"
	dashboardOpen = false
	dashboardSignalSource = func() (<-chan os.Signal, func()) {
		c := make(chan os.Signal, 1)
		c <- os.Interrupt
		return c, func() {}
	}
	manager := &fakeContainerLauncherManager{}
	newContainerLauncherManager = func(_ string, _ string, values dashboardruntime.Values) containerLauncherManager {
		if values["ANDROID_RUNNER_IMAGE"] != "published:stable" || values["ANDROID_PULL_POLICY"] != "if-not-present" {
			t.Fatalf("bootstrap flags overrode TOML: %#v", values)
		}
		return manager
	}
	command := &cobra.Command{}
	command.SetContext(context.Background())
	if err := runPublicForOS(command, nil, "linux"); err != nil {
		t.Fatal(err)
	}
}

func TestRunPublicUsesNativeApplicationOnMacFirstRun(t *testing.T) {
	oldConfigDir, oldConfigPath, oldDashboard := dashboardConfigDir, configPath, runInternalDashboardFunc
	t.Cleanup(func() {
		dashboardConfigDir, configPath, runInternalDashboardFunc = oldConfigDir, oldConfigPath, oldDashboard
	})
	dashboardConfigDir = t.TempDir()
	configPath = ""
	want := errors.New("native application selected")
	runInternalDashboardFunc = func(*cobra.Command, []string) error { return want }
	command := &cobra.Command{}
	command.SetContext(context.Background())
	if err := runPublicForOS(command, nil, "darwin"); !errors.Is(err, want) {
		t.Fatalf("macOS startup error = %v, want native application path", err)
	}
}

func TestRunInternalRuntimePreparesTypedConfigBeforeStartingServer(t *testing.T) {
	oldConfigDir, oldConfigPath, oldDashboard, oldServer, oldHost, oldEnsure := dashboardConfigDir, configPath, runInternalDashboardFunc, runInternalServerFunc, host, ensureEmulatorRuntime
	t.Cleanup(func() {
		dashboardConfigDir, configPath, runInternalDashboardFunc, runInternalServerFunc, host, ensureEmulatorRuntime = oldConfigDir, oldConfigPath, oldDashboard, oldServer, oldHost, oldEnsure
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
	t.Setenv("ANDROID_SDK_ROOT", filepath.Join(dir, "sdk"))
	dashboardConfigDir, configPath = dir, ""
	prepared := false
	ensureEmulatorRuntime = func(context.Context, runnerconfig.Config, string, string, androidtools.EmulatorProgress) error {
		prepared = true
		return nil
	}
	serverStarted := make(chan struct{})
	dashboardStopped := make(chan struct{})
	runInternalDashboardFunc = func(cmd *cobra.Command, _ []string) error {
		defer close(dashboardStopped)
		if !prepared {
			t.Error("dashboard started before typed runtime preparation")
		}
		<-cmd.Context().Done()
		return cmd.Context().Err()
	}
	runInternalServerFunc = func(cmd *cobra.Command, _ []string) error {
		close(serverStarted)
		<-cmd.Context().Done()
		return cmd.Context().Err()
	}
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- runApplicationRuntime(command, nil) }()
	select {
	case <-serverStarted:
	case <-time.After(time.Second):
		t.Fatal("internal server was not started after typed config preparation")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("internal runtime error = %v", err)
	}
	select {
	case <-dashboardStopped:
	case <-time.After(time.Second):
		t.Fatal("dashboard did not stop with the runtime")
	}
	if host != oldHost {
		t.Fatalf("internal runtime did not restore host: got %q want %q", host, oldHost)
	}
}
