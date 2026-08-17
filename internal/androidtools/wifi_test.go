package androidtools

import (
	"context"
	"errors"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

func TestConnectConfiguredWiFiDevices(t *testing.T) {
	original := adbConnect
	t.Cleanup(func() { adbConnect = original })
	var endpoints []string
	adbConnect = func(_ context.Context, endpoint string) (string, error) {
		endpoints = append(endpoints, endpoint)
		return "connected", nil
	}
	cfg := runnerconfig.Config{Devices: []runnerconfig.DeviceConfig{
		{ID: "runner/wifi", Enabled: true, Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.0.2.10", WiFiPort: "5555"}},
		{ID: "runner/wifi-ipv6", Enabled: true, Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "2001:db8::10", WiFiPort: "5560"}},
		{ID: "runner/usb", Enabled: true, Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "usb-1"}},
		{ID: "runner/no-device", Enabled: true, Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "no_device"}},
		{ID: "runner/disabled", Enabled: false, Type: runnerconfig.DeviceAndroidPhysical, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.0.2.11", WiFiPort: "5555"}},
		{ID: "runner/redroid", Enabled: true, Type: runnerconfig.DeviceRedroid, Redroid: &runnerconfig.RedroidConfig{Host: "192.0.2.12", ADBPort: 5555}},
	}}
	if err := ConnectConfiguredWiFiDevices(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.10:5555", "[2001:db8::10]:5560"}
	if strings.Join(endpoints, ",") != strings.Join(want, ",") {
		t.Fatalf("adb connect endpoints = %v, want %v", endpoints, want)
	}
}

func TestConnectConfiguredWiFiDevicesReportsFailure(t *testing.T) {
	original := adbConnect
	t.Cleanup(func() { adbConnect = original })
	adbConnect = func(_ context.Context, endpoint string) (string, error) {
		return "failed to connect to " + endpoint, errors.New("adb failed")
	}
	cfg := runnerconfig.Config{Devices: []runnerconfig.DeviceConfig{{
		ID: "runner/wifi", Enabled: true, Type: runnerconfig.DeviceAndroidPhysical,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.0.2.20", WiFiPort: "5555"},
	}}}
	err := ConnectConfiguredWiFiDevices(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), `runner/wifi`) || !strings.Contains(err.Error(), "192.0.2.20:5555") {
		t.Fatalf("connect error = %v", err)
	}
}
