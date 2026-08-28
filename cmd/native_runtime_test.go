//go:build darwin

package cmd

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

func TestNativeRuntimeSupervisorRebindsRunnerListener(t *testing.T) {
	dir := t.TempDir()
	addressA := lifecycleFreeListenAddress(t)
	addressB := lifecycleFreeListenAddress(t)
	writeNativeRuntimeTestConfig(t, dir, addressA)
	supervisor := NewNativeRuntimeSupervisor(dir)
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	if err := supervisor.Reconcile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	dialNativeRuntime(t, addressA, true)
	writeNativeRuntimeTestConfig(t, dir, addressB)
	if err := supervisor.Reconcile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	dialNativeRuntime(t, addressA, false)
	dialNativeRuntime(t, addressB, true)
	if supervisor.Status(context.Background()).RunnerRunning {
		t.Fatal("stopped reconciliation started execution")
	}
}

func TestNativeRuntimeSupervisorStoppedGenerationUsesLatestConfigOnStart(t *testing.T) {
	dir := t.TempDir()
	addressA := lifecycleFreeListenAddress(t)
	addressB := lifecycleFreeListenAddress(t)
	writeNativeRuntimeTestConfig(t, dir, addressA)
	supervisor := NewNativeRuntimeSupervisor(dir)
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	if err := supervisor.Reconcile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	writeNativeRuntimeTestConfig(t, dir, addressB)
	if err := supervisor.Reconcile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	dialNativeRuntime(t, addressA, false)
	dialNativeRuntime(t, addressB, true)
}

func writeNativeRuntimeTestConfig(t *testing.T, dir, apiListen string) {
	t.Helper()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "test"}
	cfg.Temporal = runnerconfig.TemporalConfig{Address: "temporal.example:7233"}
	cfg.Server.APIListen = apiListen
	cfg.Server.DashboardListen = lifecycleFreeListenAddress(t)
	cfg.Exposure = runnerconfig.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example"}
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/no-device", Name: "No device", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "no_device"},
	}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
}

func dialNativeRuntime(t *testing.T, address string, wantOpen bool) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if wantOpen {
		if err != nil {
			t.Fatalf("dial %s: %v", address, err)
		}
		_ = connection.Close()
		return
	}
	if err == nil {
		_ = connection.Close()
		t.Fatalf("old listener %s remains open", address)
	}
}
