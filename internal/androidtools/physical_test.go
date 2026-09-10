package androidtools

import (
	"context"
	"errors"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

func TestValidateConfiguredUSBDevices(t *testing.T) {
	original := adbGetState
	t.Cleanup(func() { adbGetState = original })
	var serials []string
	adbGetState = func(_ context.Context, serial string) (string, error) {
		serials = append(serials, serial)
		return "device\n", nil
	}
	cfg := runnerconfig.Config{Devices: []runnerconfig.DeviceConfig{
		{ID: "runner/usb", Enabled: true, Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "SERIAL123"}},
		{ID: "runner/wifi", Enabled: true, Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.0.2.10"}},
	}}
	if err := ValidateConfiguredUSBDevices(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(serials) != 1 || serials[0] != "SERIAL123" {
		t.Fatalf("validated serials = %v", serials)
	}
}

func TestValidateConfiguredUSBDevicesRejectsUnavailableDevice(t *testing.T) {
	original := adbGetState
	t.Cleanup(func() { adbGetState = original })
	adbGetState = func(context.Context, string) (string, error) { return "", errors.New("device unauthorized") }
	cfg := runnerconfig.Config{Devices: []runnerconfig.DeviceConfig{{ID: "runner/usb", Enabled: true, Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "SERIAL123"}}}}
	if err := ValidateConfiguredUSBDevices(context.Background(), cfg); err == nil {
		t.Fatal("unavailable USB device unexpectedly validated")
	}
}

func TestValidateConfiguredUSBDevicesSkipsDisabledAndNonUSB(t *testing.T) {
	original := adbGetState
	t.Cleanup(func() { adbGetState = original })
	adbGetState = func(context.Context, string) (string, error) { t.Fatal("unexpected adb state probe"); return "", nil }
	cfg := runnerconfig.Config{Devices: []runnerconfig.DeviceConfig{
		{ID: "runner/disabled", Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "disabled"}},
		{ID: "runner/none", Enabled: true, Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "no_device"}},
	}}
	if err := ValidateConfiguredUSBDevices(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConfiguredPhysicalDevicesConnectsWiFiBeforeStateValidation(t *testing.T) {
	oldConnect, oldState := adbConnect, adbGetState
	t.Cleanup(func() { adbConnect, adbGetState = oldConnect, oldState })
	var calls []string
	adbConnect = func(_ context.Context, endpoint string) (string, error) {
		calls = append(calls, "connect "+endpoint)
		return "connected", nil
	}
	adbGetState = func(_ context.Context, serial string) (string, error) {
		calls = append(calls, "state "+serial)
		return "device", nil
	}
	cfg := runnerconfig.Config{Devices: []runnerconfig.DeviceConfig{{ID: "wifi", Enabled: true, Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.0.2.10", WiFiPort: "5555"}}}}
	if err := ValidateConfiguredPhysicalDevices(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(calls, ","), "connect 192.0.2.10:5555,state 192.0.2.10:5555"; got != want {
		t.Fatalf("calls=%q want=%q", got, want)
	}
}
