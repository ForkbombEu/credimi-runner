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
