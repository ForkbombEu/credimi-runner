package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
	"github.com/spf13/cobra"
)

type rootManagerFake struct{ started, stopped, restarted, logs int }

func (m *rootManagerFake) Start(context.Context) error   { m.started++; return nil }
func (m *rootManagerFake) Stop(context.Context) error    { m.stopped++; return nil }
func (m *rootManagerFake) Restart(context.Context) error { m.restarted++; return nil }
func (m *rootManagerFake) Enable(context.Context) error  { return nil }
func (m *rootManagerFake) Disable(context.Context) error { return nil }
func (m *rootManagerFake) Status(context.Context) (servicemanager.Status, error) {
	return servicemanager.Status{Running: true, DashboardURL: "http://127.0.0.1:8051"}, nil
}
func (m *rootManagerFake) Logs(ctx context.Context, _ servicemanager.LogOptions) error {
	m.logs++
	<-ctx.Done()
	return ctx.Err()
}

type rootStoppedManager struct{ rootManagerFake }

func isolateRootConfig(t *testing.T) {
	t.Helper()
	old := dashboardConfigDir
	dashboardConfigDir = t.TempDir()
	t.Cleanup(func() { dashboardConfigDir = old })
}

func (m *rootStoppedManager) Status(context.Context) (servicemanager.Status, error) {
	return servicemanager.Status{Running: false, DashboardURL: "http://127.0.0.1:8051"}, nil
}
func TestEffectiveConfigDirHonorsExplicitConfigPath(t *testing.T) {
	oldConfigPath, oldDashboardConfigDir := configPath, dashboardConfigDir
	t.Cleanup(func() { configPath, dashboardConfigDir = oldConfigPath, oldDashboardConfigDir })
	dashboardConfigDir = ""
	configPath = filepath.Join(t.TempDir(), "nested", "config.toml")
	if got, err := effectiveConfigDir(); err != nil || got != filepath.Dir(configPath) {
		t.Fatalf("effectiveConfigDir = %q, err=%v", got, err)
	}
}

func TestRootStartsStoppedService(t *testing.T) {
	isolateRootConfig(t)
	oldFactory, oldWait, oldOpen := serviceManagerFactory, waitForDashboardFunc, dashboardOpen
	t.Cleanup(func() { serviceManagerFactory, waitForDashboardFunc, dashboardOpen = oldFactory, oldWait, oldOpen })
	fake := &rootStoppedManager{}
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return fake }
	waitForDashboardFunc = func(context.Context) (string, error) { return "http://127.0.0.1:8051", nil }
	dashboardOpen = false
	ctx, cancel := context.WithCancel(context.Background())
	command := &cobra.Command{Use: "test"}
	command.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- runRoot(command, nil) }()
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if fake.started != 1 {
		t.Fatalf("service starts=%d", fake.started)
	}
}

func TestRootPrintsDashboardURLWhenBrowserOpens(t *testing.T) {
	isolateRootConfig(t)
	oldFactory, oldWait, oldOpen, oldBrowser := serviceManagerFactory, waitForDashboardFunc, dashboardOpen, openDashboardBrowserFunc
	t.Cleanup(func() {
		serviceManagerFactory, waitForDashboardFunc, dashboardOpen, openDashboardBrowserFunc = oldFactory, oldWait, oldOpen, oldBrowser
	})
	fake := &rootManagerFake{}
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return fake }
	waitForDashboardFunc = func(context.Context) (string, error) { return "http://127.0.0.1:8051", nil }
	dashboardOpen = true
	openDashboardBrowserFunc = func(string) error { return nil }
	t.Setenv("DISPLAY", ":0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := &cobra.Command{Use: "test"}
	command.SetContext(ctx)
	var output strings.Builder
	command.SetOut(&output)
	done := make(chan error, 1)
	go func() { done <- runRoot(command, nil) }()
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got := output.String(); got != "Dashboard: http://127.0.0.1:8051\n" {
		t.Fatalf("output=%q", got)
	}
}

func TestWaitForDashboardHonorsCancellation(t *testing.T) {
	old := waitForDashboardFunc
	t.Cleanup(func() { waitForDashboardFunc = old })
	waitForDashboardFunc = func(ctx context.Context) (string, error) {
		return old(ctx)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := waitForDashboardFunc(ctx)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestDashboardBrowserAvailabilityAndEmptyURL(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" && dashboardCanOpenBrowser() {
		t.Fatal("headless Linux unexpectedly supports browser opening")
	}
	t.Setenv("DISPLAY", ":0")
	if !dashboardCanOpenBrowser() {
		t.Fatal("DISPLAY should enable browser opening")
	}
	if err := openDashboardBrowser(""); err == nil {
		t.Fatal("empty dashboard URL accepted")
	}
}

func TestDashboardCommandRequiresRunningService(t *testing.T) {
	oldDir := dashboardConfigDir
	dashboardConfigDir = t.TempDir()
	t.Cleanup(func() { dashboardConfigDir = oldDir })
	command := &cobra.Command{Use: "dashboard"}
	command.SetContext(context.Background())
	if err := runDashboardCommand(command, nil); err == nil {
		t.Fatal("expected service-not-running error")
	}
}

type noURLManager struct{ servicemanager.Manager }

func (m *noURLManager) Status(context.Context) (servicemanager.Status, error) {
	return servicemanager.Status{}, nil
}

func TestRootCtrlCOnlyStopsLogFollower(t *testing.T) {
	isolateRootConfig(t)
	oldFactory, oldWait, oldOpen := serviceManagerFactory, waitForDashboardFunc, dashboardOpen
	t.Cleanup(func() { serviceManagerFactory, waitForDashboardFunc, dashboardOpen = oldFactory, oldWait, oldOpen })
	fake := &rootManagerFake{}
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return fake }
	waitForDashboardFunc = func(context.Context) (string, error) { return "http://127.0.0.1:8051", nil }
	dashboardOpen = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	command := &cobra.Command{Use: "test"}
	command.SetContext(ctx)
	go func() { done <- runRoot(command, nil) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("root cancellation error = %v", err)
	}
	if fake.started != 0 || fake.stopped != 0 || fake.restarted != 0 || fake.logs != 1 {
		t.Fatalf("manager calls=%+v", fake)
	}
}

type rootStatusErrorManager struct{ rootManagerFake }

func (m *rootStatusErrorManager) Status(context.Context) (servicemanager.Status, error) {
	return servicemanager.Status{}, context.DeadlineExceeded
}

func TestRootStartsServiceOnceWhenStatusUnavailable(t *testing.T) {
	isolateRootConfig(t)
	oldFactory, oldWait, oldOpen := serviceManagerFactory, waitForDashboardFunc, dashboardOpen
	t.Cleanup(func() { serviceManagerFactory, waitForDashboardFunc, dashboardOpen = oldFactory, oldWait, oldOpen })
	fake := &rootStatusErrorManager{}
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return fake }
	waitForDashboardFunc = func(context.Context) (string, error) {
		return "http://127.0.0.1:8051", nil
	}
	dashboardOpen = false
	ctx, cancel := context.WithCancel(context.Background())
	command := &cobra.Command{Use: "test"}
	command.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- runRoot(command, nil) }()
	// The fake log follower exits as soon as the root context is canceled.
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if fake.started != 1 {
		t.Fatalf("service started %d times", fake.started)
	}
}

func TestRootPassesBootstrapOptionsToServiceManager(t *testing.T) {
	isolateRootConfig(t)
	oldFactory, oldWait, oldOpen, oldImage, oldPolicy, oldDashboardListen := serviceManagerFactory, waitForDashboardFunc, dashboardOpen, bootstrapImage, bootstrapPullPolicy, dashboardListen
	t.Cleanup(func() {
		serviceManagerFactory, waitForDashboardFunc, dashboardOpen, bootstrapImage, bootstrapPullPolicy, dashboardListen = oldFactory, oldWait, oldOpen, oldImage, oldPolicy, oldDashboardListen
	})
	fake := &rootManagerFake{}
	var got servicemanager.BootstrapOptions
	serviceManagerFactory = func(_ string, options servicemanager.BootstrapOptions) servicemanager.Manager {
		got = options
		return fake
	}
	waitForDashboardFunc = func(context.Context) (string, error) { return "http://127.0.0.1:8051", nil }
	dashboardOpen = false
	bootstrapImage = "credimi-runner:local"
	bootstrapPullPolicy = "never"
	dashboardListen = "192.0.2.10:8051"
	ctx, cancel := context.WithCancel(context.Background())
	command := &cobra.Command{Use: "test"}
	command.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- runRoot(command, nil) }()
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got.Image != bootstrapImage || got.PullPolicy != bootstrapPullPolicy || got.DashboardListen != dashboardListen {
		t.Fatalf("bootstrap options = %+v", got)
	}
}
