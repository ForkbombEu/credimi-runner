package workermanager

import (
	"testing"
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

func TestRunnerRuntimeConfigScopesActivityEnvironmentPerDevice(t *testing.T) {
	config := RunnerRuntimeConfig{
		RunnerID: "acme/runner",
		Host:     map[string]string{"TEMPORAL_ADDRESS": "temporal.example:7233", "SHARED": "host"},
		Devices: []DeviceRuntimeConfig{
			{ID: "acme/runner/a", Enabled: true, Values: map[string]string{"SERIAL": "a", "DEVICE_VALUE": "A"}},
			{ID: "acme/runner/b", Enabled: true, Values: map[string]string{"SERIAL": "b", "DEVICE_VALUE": "B"}},
		},
	}
	getA, err := config.Environment("acme/runner/a")
	if err != nil {
		t.Fatal(err)
	}
	getB, err := config.Environment("acme/runner/b")
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
}

func TestRunnerRuntimeConfigEnvironmentUsesFallbacksAndRejectsInvalidDevices(t *testing.T) {
	config := RunnerRuntimeConfig{RunnerID: "acme/runner", Host: map[string]string{"HOST_VALUE": "host"}, Devices: []DeviceRuntimeConfig{{ID: "acme/runner/a", Enabled: true}}}
	getter, err := config.Environment("acme/runner/a")
	if err != nil {
		t.Fatal(err)
	}
	if getter("HOST_VALUE") != "host" || getter("missing", "fallback") != "fallback" || getter("missing") != "" {
		t.Fatalf("environment fallback behavior is incorrect")
	}
	if _, err := config.Environment("acme/runner/missing"); err == nil {
		t.Fatal("unknown device environment lookup succeeded")
	}
	config.Devices[0].Enabled = false
	if _, err := config.Environment("acme/runner/a"); err == nil {
		t.Fatal("disabled device environment lookup succeeded")
	}
}

func TestRunnerRuntimeConfigEnvironmentPreservesEmptyDeviceOverrides(t *testing.T) {
	t.Setenv("AVDCTL_SSH_TARGET", "stale-host")
	t.Setenv("AVDCTL_SUDO", "true")
	config := RunnerRuntimeConfig{
		RunnerID: "acme/runner",
		Devices: []DeviceRuntimeConfig{
			{ID: "acme/runner/a", Enabled: true, Values: map[string]string{"AVDCTL_SSH_TARGET": "", "AVDCTL_SUDO": "false"}},
			{ID: "acme/runner/b", Enabled: true, Values: map[string]string{"AVDCTL_SSH_TARGET": "bob@host-b"}},
		},
	}
	getA, err := config.Environment("acme/runner/a")
	if err != nil {
		t.Fatal(err)
	}
	getB, err := config.Environment("acme/runner/b")
	if err != nil {
		t.Fatal(err)
	}
	if got := getA("AVDCTL_SSH_TARGET"); got != "" {
		t.Fatalf("empty device SSH target fell through to %q", got)
	}
	if got := getA("AVDCTL_SUDO"); got != "false" {
		t.Fatalf("explicit device sudo value = %q, want false", got)
	}
	if got := getB("AVDCTL_SSH_TARGET"); got != "bob@host-b" {
		t.Fatalf("second device SSH target = %q", got)
	}
}
