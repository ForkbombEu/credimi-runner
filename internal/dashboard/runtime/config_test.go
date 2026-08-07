package runtime

import (
	"path/filepath"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func testTOMLConfig(dir string) config.Config {
	return config.Config{
		SchemaVersion: config.SchemaVersion,
		Runner:        config.RunnerConfig{ID: "acme/runner", Name: "Runner", Organization: "acme"},
		Credimi:       config.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"},
		Temporal:      config.TemporalConfig{Address: "temporal.example:7233"},
		Server:        config.ServerConfig{APIListen: "127.0.0.1:8050", DashboardListen: "127.0.0.1:8051", ReadHeaderTimeout: config.Duration(1), ShutdownTimeout: config.Duration(1)},
		Exposure:      config.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example"},
		Storage:       config.StorageConfig{StateDir: filepath.Join(dir, "state"), ArtifactRetention: config.Duration(1)},
		Devices:       []config.DeviceConfig{{ID: "acme/runner/one", Name: "One", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "wifi", Serial: "one:5555"}}},
	}
}

func TestLoadStoreMissingTOML(t *testing.T) {
	store, err := LoadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store.Exists() || filepath.Base(store.Path) != "config.toml" {
		t.Fatalf("store=%#v", store)
	}
}

func TestStoreLoadsAndSavesTypedTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteFile(path, testTOMLConfig(dir)); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.Values["CREDIMI_RUNNER_ID"] != "acme/runner" || store.Values["CREDIMI_DEVICE_1_SERIAL"] != "one:5555" {
		t.Fatalf("values=%#v", store.Values)
	}
	inventory, err := store.RuntimeConfig()
	if err != nil || len(inventory.Devices) != 1 {
		t.Fatalf("inventory=%#v err=%v", inventory, err)
	}
	inventory.Devices[0].Serial = "two:5555"
	inventory.Devices[0].Values["SERIAL"] = "two:5555"
	if err := store.SaveRuntimeConfig(inventory); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Values["CREDIMI_DEVICE_1_SERIAL"] != "two:5555" {
		t.Fatalf("reloaded=%#v", reloaded.Values)
	}
}

func TestDefaultConfigDirHonorsOverride(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", "/tmp/runner-config")
	if got := DefaultConfigDir(); got != "/tmp/runner-config" {
		t.Fatalf("dir=%q", got)
	}
}

func TestRuntimeConfigRejectsInvalidInventory(t *testing.T) {
	_, err := ParseRuntimeConfig(Values{"CREDIMI_DEVICE_COUNT": "1", "CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_1_UNKNOWN": "bad"})
	if err == nil {
		t.Fatal("invalid inventory accepted")
	}
}

func TestValidateDeviceConstraintsExplainsConflicts(t *testing.T) {
	err := ValidateDeviceConstraints([]DeviceRuntimeConfig{{Name: "One", Type: "android_phone", Serial: "same"}, {Name: "Two", Type: "redroid", Serial: "same"}})
	if err == nil {
		t.Fatal("duplicate serial accepted")
	}
}
