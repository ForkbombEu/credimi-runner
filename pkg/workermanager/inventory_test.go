package workermanager

import (
	"context"
	"testing"
	"time"
)

func TestRunnerRuntimeConfigValidatesDeviceOwnership(t *testing.T) {
	config := RunnerRuntimeConfig{RunnerID: "acme/runner", Devices: []DeviceRuntimeConfig{{ID: "acme/runner/a"}, {ID: "acme/runner/b"}}}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.Devices[1].ID = "acme/other"
	if err := config.Validate(); err == nil {
		t.Fatal("expected invalid device ownership")
	}
}

func TestDeviceGateSerializesOnlyMatchingDevice(t *testing.T) {
	gate := NewDeviceGate()
	releaseA, err := gate.Acquire(context.Background(), "acme/runner/a")
	if err != nil {
		t.Fatal(err)
	}
	otherDone := make(chan struct{})
	go func() {
		release, err := gate.Acquire(context.Background(), "acme/runner/b")
		if err == nil {
			release()
			close(otherDone)
		}
	}()
	select {
	case <-otherDone:
	case <-time.After(time.Second):
		t.Fatal("different devices should not block each other")
	}
	blocked := make(chan struct{})
	go func() {
		release, err := gate.Acquire(context.Background(), "acme/runner/a")
		if err == nil {
			release()
			close(blocked)
		}
	}()
	select {
	case <-blocked:
		t.Fatal("same device must be serialized")
	case <-time.After(25 * time.Millisecond):
	}
	releaseA()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("same device did not resume after release")
	}
}

func TestDeviceDispatcherBindsOneConfiguredDevice(t *testing.T) {
	dispatcher, err := NewDeviceDispatcher(RunnerRuntimeConfig{RunnerID: "acme/runner", Devices: []DeviceRuntimeConfig{{ID: "acme/runner/a", Serial: "serial-a", Enabled: true}, {ID: "acme/runner/b", Serial: "serial-b", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := dispatcher.Execute(context.Background(), "acme/runner/b", func(_ context.Context, device DeviceRuntimeConfig) error { got = device.Serial; return nil }); err != nil {
		t.Fatal(err)
	}
	if got != "serial-b" {
		t.Fatalf("bound serial = %q", got)
	}
	if err := dispatcher.Execute(context.Background(), "acme/runner/unknown", func(context.Context, DeviceRuntimeConfig) error { t.Fatal("unknown device executed"); return nil }); err == nil {
		t.Fatal("unknown device was accepted")
	}
}

func TestInventoryStoreScopesActivityEnvironmentPerDevice(t *testing.T) {
	store, err := NewInventoryStore(RunnerRuntimeConfig{
		RunnerID: "acme/runner",
		Host:     map[string]string{"TEMPORAL_ADDRESS": "temporal.example:7233", "SHARED": "host"},
		Devices: []DeviceRuntimeConfig{
			{ID: "acme/runner/a", Enabled: true, Values: map[string]string{"SERIAL": "a", "DEVICE_VALUE": "A"}},
			{ID: "acme/runner/b", Enabled: true, Values: map[string]string{"SERIAL": "b", "DEVICE_VALUE": "B"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	getA, err := store.Environment("acme/runner/a")
	if err != nil {
		t.Fatal(err)
	}
	getB, err := store.Environment("acme/runner/b")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 2)
	go func() { results <- getA("DEVICE_VALUE") + ":" + getA("SERIAL") + ":" + getA("SHARED") }()
	go func() { results <- getB("DEVICE_VALUE") + ":" + getB("SERIAL") + ":" + getB("SHARED") }()
	seen := map[string]bool{}
	for range 2 {
		seen[<-results] = true
	}
	if !seen["A:a:host"] || !seen["B:b:host"] {
		t.Fatalf("device-scoped environment leaked values: %#v", seen)
	}
	if err := store.Update(RunnerRuntimeConfig{RunnerID: "acme/runner", Devices: []DeviceRuntimeConfig{{ID: "acme/runner/c", Enabled: true, Values: map[string]string{"SERIAL": "c"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Environment("acme/runner/a"); err == nil {
		t.Fatal("removed device remained addressable after inventory update")
	}
}

func TestInventoryStoreUsesFallbacksAndRejectsInvalidUpdates(t *testing.T) {
	store, err := NewInventoryStore(RunnerRuntimeConfig{RunnerID: "acme/runner", Host: map[string]string{"HOST_VALUE": "host"}, Devices: []DeviceRuntimeConfig{{ID: "acme/runner/a", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	getter, err := store.Environment("acme/runner/a")
	if err != nil {
		t.Fatal(err)
	}
	if getter("HOST_VALUE") != "host" || getter("missing", "fallback") != "fallback" || getter("missing") != "" {
		t.Fatalf("environment fallback behavior is incorrect")
	}
	if _, err := store.Environment("acme/runner/missing"); err == nil {
		t.Fatal("unknown device environment lookup succeeded")
	}
	if err := store.Update(RunnerRuntimeConfig{RunnerID: "acme/runner"}); err == nil {
		t.Fatal("invalid inventory update succeeded")
	}
}
