package runtime

import (
	"path/filepath"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func TestStoreUsesTypedTOMLForDeviceInventory(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		SchemaVersion: config.SchemaVersion,
		Runner:        config.RunnerConfig{ID: "acme/runner", Name: "Runner", Organization: "acme"},
		Credimi:       config.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"},
		Temporal:      config.TemporalConfig{Address: "temporal.example:7233"},
		Server:        config.ServerConfig{APIListen: "127.0.0.1:8050", DashboardListen: "127.0.0.1:8051", ReadHeaderTimeout: config.Duration(1), ShutdownTimeout: config.Duration(1)},
		Exposure:      config.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example"},
		Storage:       config.StorageConfig{StateDir: filepath.Join(dir, "state"), ArtifactRetention: config.Duration(1)},
		Android:       config.AndroidConfig{RunnerImage: "credimi-runner:local", PullPolicy: "never", Network: "runner-net", StateVolume: "state-volume", ToolCacheVolume: "tools-volume", SDKVolume: "sdk-volume", ADBKeysPath: filepath.Join(dir, "adb-keys")},
		Devices:       []config.DeviceConfig{{ID: "acme/runner/one", Name: "One", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "wifi", Serial: "one:5555"}}},
	}
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.Path != filepath.Join(dir, "config.toml") || store.Values["CREDIMI_DEVICE_1_SERIAL"] != "one:5555" || store.Values["ANDROID_RUNNER_IMAGE"] != "credimi-runner:local" || store.Values["ANDROID_PULL_POLICY"] != "never" || store.Values["ANDROID_NETWORK"] != "runner-net" {
		t.Fatalf("store=%#v", store)
	}
	parsed, err := store.RuntimeConfig()
	if err != nil || len(parsed.Devices) != 1 {
		t.Fatalf("runtime config=%#v err=%v", parsed, err)
	}
	parsed.Devices[0].Serial = "two:5555"
	parsed.Devices[0].Values["SERIAL"] = "two:5555"
	if err := store.SaveRuntimeConfig(parsed); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadStore(dir)
	if err != nil || reloaded.Values["CREDIMI_DEVICE_1_SERIAL"] != "two:5555" {
		t.Fatalf("reloaded=%#v err=%v", reloaded, err)
	}
}

func TestValuesWithRuntimeDevicesPreservesAuthoritativeSecondSerial(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), testTOMLConfig(dir)); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	second := DeviceRuntimeConfig{ID: "acme/runner/two", Name: "Two", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "SERIAL_TWO", WiFiIP: "192.0.2.2", WiFiPort: "5555", Values: Values{}}
	devices := append(first.Devices, second)
	values := ValuesWithRuntimeDevices(store.Snapshot(), devices)
	if values["CREDIMI_DEVICE_2_SERIAL"] != "SERIAL_TWO" {
		t.Fatalf("second serial values = %#v", values)
	}
	if values["CREDIMI_DEVICE_2_WIFI_IP"] != "192.0.2.2" || values["CREDIMI_DEVICE_2_WIFI_PORT"] != "5555" {
		t.Fatalf("second Wi-Fi values = %#v", values)
	}
	if err := store.SaveRuntimeConfig(RunnerRuntimeConfig{Host: store.Snapshot(), Devices: devices}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	config, err := reloaded.RuntimeConfig()
	if err != nil || len(config.Devices) != 2 || config.Devices[1].Serial != "SERIAL_TWO" {
		t.Fatalf("round-tripped devices = %#v err=%v", config.Devices, err)
	}
}
