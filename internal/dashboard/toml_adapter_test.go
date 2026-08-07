package dashboard

import (
	"path/filepath"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func TestConfigRoundTripsTypedTOMLWithoutDotEnv(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		SchemaVersion: config.SchemaVersion,
		Runner:        config.RunnerConfig{ID: "acme/runner", Name: "Runner", Organization: "acme"},
		Credimi:       config.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "secret"},
		Temporal:      config.TemporalConfig{Address: "temporal.example:7233"},
		Server:        config.ServerConfig{APIListen: "127.0.0.1:8050", DashboardListen: "127.0.0.1:8051", ReadHeaderTimeout: config.Duration(1), ShutdownTimeout: config.Duration(1)},
		Exposure:      config.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example"},
		Storage:       config.StorageConfig{StateDir: filepath.Join(dir, "state"), ArtifactRetention: config.Duration(1)},
		Devices:       []config.DeviceConfig{{ID: "acme/runner/pixel", Name: "Pixel", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "wifi", Serial: "pixel:5555"}}},
	}
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Path() != path || loaded.Get("CREDIMI_RUNNER_ID") != "acme/runner" || loaded.Get("CREDIMI_DEVICE_1_SERIAL") != "pixel:5555" {
		t.Fatalf("loaded dashboard values = %#v", loaded.Snapshot())
	}
	if loaded.AuthMode() != "user" {
		t.Fatalf("auth mode = %q", loaded.AuthMode())
	}
}
