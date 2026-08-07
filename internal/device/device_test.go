package device

import (
	"errors"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func physical(id string, enabled bool) config.DeviceConfig {
	return config.DeviceConfig{
		ID: id, Name: id, Type: config.DeviceAndroidPhysical, Enabled: enabled,
		AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "wifi", Serial: id + ":5555"},
	}
}

func TestRegistryLookupSnapshotAndBoundaryNormalization(t *testing.T) {
	registry, err := NewRegistry([]config.DeviceConfig{physical("acme/runner/b", true), physical("acme/runner/a", true)})
	if err != nil {
		t.Fatal(err)
	}
	listed := registry.List()
	if got := []string{listed[0].ID, listed[1].ID}; got[0] != "acme/runner/a" || got[1] != "acme/runner/b" {
		t.Fatalf("list order = %v", got)
	}
	if _, err := registry.Resolve("/acme/runner/a"); !errors.Is(err, UnknownDeviceError{}) {
		var unknown UnknownDeviceError
		if !errors.As(err, &unknown) {
			t.Fatalf("strict lookup error = %v", err)
		}
	}
	if got := NormalizeBoundaryID("/acme/runner/a"); got != "acme/runner/a" {
		t.Fatalf("normalized ID = %q", got)
	}

	running, err := registry.Resolve("acme/runner/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Replace([]config.DeviceConfig{physical("acme/runner/b", true)}); err != nil {
		t.Fatal(err)
	}
	if running.ID != "acme/runner/a" || running.AndroidPhysical.Serial != "acme/runner/a:5555" {
		t.Fatalf("operation snapshot mutated: %#v", running)
	}
	if _, err := registry.Resolve("acme/runner/a"); err == nil {
		t.Fatal("removed device resolved")
	}
}

func TestRegistryRejectsDuplicatesUnknownAndDisabled(t *testing.T) {
	if _, err := NewRegistry([]config.DeviceConfig{physical("same", true), physical("same", true)}); err == nil {
		t.Fatal("duplicate inventory accepted")
	}
	registry, err := NewRegistry([]config.DeviceConfig{physical("disabled", false)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ResolveEnabled("missing"); err == nil {
		t.Fatal("unknown device accepted")
	} else {
		var unknown UnknownDeviceError
		if !errors.As(err, &unknown) {
			t.Fatalf("error = %v", err)
		}
	}
	if _, err := registry.ResolveEnabled("disabled"); err == nil {
		t.Fatal("disabled device accepted")
	} else {
		var disabled DisabledDeviceError
		if !errors.As(err, &disabled) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestRegistryReturnsIndependentDeviceCopies(t *testing.T) {
	registry, err := NewRegistry([]config.DeviceConfig{physical("acme/runner/device", true)})
	if err != nil {
		t.Fatal(err)
	}
	device, err := registry.Resolve("acme/runner/device")
	if err != nil {
		t.Fatal(err)
	}
	device.AndroidPhysical.Serial = "mutated"
	listed := registry.List()
	listed[0].AndroidPhysical.Serial = "also-mutated"
	actual, err := registry.Resolve("acme/runner/device")
	if err != nil {
		t.Fatal(err)
	}
	if actual.AndroidPhysical.Serial != "acme/runner/device:5555" {
		t.Fatalf("registry leaked mutable snapshot: %#v", actual)
	}
}

func TestEmptyRegistryIsSafeAndBoundaryNormalizationIsMinimal(t *testing.T) {
	var registry Registry
	if got := registry.List(); got != nil {
		t.Fatalf("empty registry list = %#v", got)
	}
	if _, err := registry.Resolve("missing"); err == nil {
		t.Fatal("nil registry resolved a device")
	}
	if got := NormalizeBoundaryID("acme/runner/device"); got != "acme/runner/device" {
		t.Fatalf("unchanged ID = %q", got)
	}
}
