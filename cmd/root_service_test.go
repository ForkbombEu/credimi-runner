package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
	"github.com/spf13/cobra"
)

type rootManagerFake struct{ started, stopped, restarted, logs int }

func (m *rootManagerFake) Start(context.Context) error   { m.started++; return nil }
func (m *rootManagerFake) Stop(context.Context) error    { m.stopped++; return nil }
func (m *rootManagerFake) Restart(context.Context) error { m.restarted++; return nil }
func (m *rootManagerFake) Status(context.Context) (servicemanager.Status, error) {
	return servicemanager.Status{Running: true, DashboardURL: "http://127.0.0.1:8051"}, nil
}
func (m *rootManagerFake) Logs(ctx context.Context, _ servicemanager.LogOptions) error {
	m.logs++
	<-ctx.Done()
	return ctx.Err()
}

type rootStoppedManager struct{ rootManagerFake }

func (m *rootStoppedManager) Status(context.Context) (servicemanager.Status, error) {
	return servicemanager.Status{Running: false, DashboardURL: "http://127.0.0.1:8051"}, nil
}

func TestRootStartsStoppedService(t *testing.T) {
	oldFactory, oldWait, oldOpen := serviceManagerFactory, waitForDashboardFunc, dashboardOpen
	t.Cleanup(func() { serviceManagerFactory, waitForDashboardFunc, dashboardOpen = oldFactory, oldWait, oldOpen })
	fake := &rootStoppedManager{}
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return fake }
	waitForDashboardFunc = func(context.Context, servicemanager.Manager) (string, error) { return "http://127.0.0.1:8051", nil }
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

func TestWaitForDashboardHonorsCancellation(t *testing.T) {
	old := waitForDashboardFunc
	t.Cleanup(func() { waitForDashboardFunc = old })
	waitForDashboardFunc = func(ctx context.Context, manager servicemanager.Manager) (string, error) {
		return old(ctx, &noURLManager{Manager: manager})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := waitForDashboardFunc(ctx, &rootManagerFake{})
	if err == nil {
		t.Fatal("expected cancellation error")
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
	oldFactory, oldWait, oldOpen := serviceManagerFactory, waitForDashboardFunc, dashboardOpen
	t.Cleanup(func() { serviceManagerFactory, waitForDashboardFunc, dashboardOpen = oldFactory, oldWait, oldOpen })
	fake := &rootManagerFake{}
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return fake }
	waitForDashboardFunc = func(context.Context, servicemanager.Manager) (string, error) { return "http://127.0.0.1:8051", nil }
	dashboardOpen = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	command := &cobra.Command{Use: "test"}
	command.SetContext(ctx)
	go func() { done <- runRoot(command, nil) }()
	cancel()
	<-done
	if fake.started != 0 || fake.stopped != 0 || fake.restarted != 0 || fake.logs != 1 {
		t.Fatalf("manager calls=%+v", fake)
	}
}

type rootStatusErrorManager struct{ rootManagerFake }

func (m *rootStatusErrorManager) Status(context.Context) (servicemanager.Status, error) {
	return servicemanager.Status{}, context.DeadlineExceeded
}

func TestRootStartsServiceOnceWhenStatusUnavailable(t *testing.T) {
	oldFactory, oldWait, oldOpen := serviceManagerFactory, waitForDashboardFunc, dashboardOpen
	t.Cleanup(func() { serviceManagerFactory, waitForDashboardFunc, dashboardOpen = oldFactory, oldWait, oldOpen })
	fake := &rootStatusErrorManager{}
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return fake }
	waitForDashboardFunc = func(context.Context, servicemanager.Manager) (string, error) {
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
	oldFactory, oldWait, oldOpen, oldImage, oldPolicy := serviceManagerFactory, waitForDashboardFunc, dashboardOpen, bootstrapImage, bootstrapPullPolicy
	t.Cleanup(func() {
		serviceManagerFactory, waitForDashboardFunc, dashboardOpen, bootstrapImage, bootstrapPullPolicy = oldFactory, oldWait, oldOpen, oldImage, oldPolicy
	})
	fake := &rootManagerFake{}
	var got servicemanager.BootstrapOptions
	serviceManagerFactory = func(_ string, options servicemanager.BootstrapOptions) servicemanager.Manager {
		got = options
		return fake
	}
	waitForDashboardFunc = func(context.Context, servicemanager.Manager) (string, error) { return "http://127.0.0.1:8051", nil }
	dashboardOpen = false
	bootstrapImage = "credimi-runner:local"
	bootstrapPullPolicy = "never"
	ctx, cancel := context.WithCancel(context.Background())
	command := &cobra.Command{Use: "test"}
	command.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- runRoot(command, nil) }()
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got.Image != bootstrapImage || got.PullPolicy != bootstrapPullPolicy {
		t.Fatalf("bootstrap options = %+v", got)
	}
}
