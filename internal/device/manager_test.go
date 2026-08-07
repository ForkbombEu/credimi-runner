package device

import (
	"context"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func TestManagerReconcilesWithoutRestartingProcess(t *testing.T) {
	registry, err := NewRegistry([]config.DeviceConfig{{ID: "inventory/runner/one", Name: "One", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "wifi", Serial: "one"}}})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	manager := Manager{Registry: registry, Apply: func(context.Context, []config.DeviceConfig) error { called = true; return nil }}
	if err := manager.Reconcile(context.Background(), []config.DeviceConfig{{ID: "inventory/runner/two", Name: "Two", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "wifi", Serial: "two"}}}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected reconciliation callback")
	}
	if _, err := registry.Resolve("inventory/runner/one"); err == nil {
		t.Fatal("removed device remained registered")
	}
	if _, err := registry.Resolve("inventory/runner/two"); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRejectsMissingRegistryAndPropagatesApplyErrors(t *testing.T) {
	if err := (&Manager{}).Reconcile(context.Background(), nil); err == nil {
		t.Fatal("manager without registry accepted reconciliation")
	}
	registry, err := NewRegistry([]config.DeviceConfig{physical("inventory/runner/one", true)})
	if err != nil {
		t.Fatal(err)
	}
	want := context.Canceled
	manager := Manager{Registry: registry, Apply: func(context.Context, []config.DeviceConfig) error { return want }}
	if err := manager.Reconcile(context.Background(), []config.DeviceConfig{physical("inventory/runner/two", true)}); err != want {
		t.Fatalf("apply error = %v, want %v", err, want)
	}
}
