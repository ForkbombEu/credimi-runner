package dashboard

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func dashboardTestConfig(dir string) config.Config {
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

func TestLoadConfigUsesTypedTOML(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), dashboardTestConfig(dir)); err != nil {
		t.Fatal(err)
	}
	runner, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runner.Path() != filepath.Join(dir, "config.toml") || runner.Get("CREDIMI_RUNNER_ID") != "acme/runner" {
		t.Fatalf("runner=%#v", runner.Snapshot())
	}
	if runner.Get("CREDIMI_DEVICE_1_SERIAL") != "one:5555" {
		t.Fatalf("devices=%#v", runner.Snapshot())
	}
}

func TestConfigApplyWritesTypedTOML(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), dashboardTestConfig(dir)); err != nil {
		t.Fatal(err)
	}
	runner, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Apply(map[string]string{"CREDIMI_RUNNER_DESCRIPTION": "updated"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.LoadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Runner.Description != "updated" {
		t.Fatalf("description=%q", reloaded.Runner.Description)
	}
}

func TestConfigHelpersAndFieldGroups(t *testing.T) {
	if maskSecret("short") != "short" {
		t.Fatal("short secret changed")
	}
	if !strings.Contains(maskSecret("123456789"), "•") {
		t.Fatal("long secret was not masked")
	}
	if boolStr(true) != "true" || boolStr(false) != "false" || !isTruthyFormValue("on") {
		t.Fatal("boolean conversion failed")
	}
	if len(GroupedFields()) == 0 {
		t.Fatal("field groups are empty")
	}
	if got := titleCase("internal admin"); got != "Internal admin" {
		t.Fatalf("title=%q", got)
	}
}

func TestConfigAuthMode(t *testing.T) {
	runner := &Config{values: map[string]string{"CREDIMI_USER_API_KEY": "key"}}
	if runner.AuthMode() != "user" {
		t.Fatal(runner.AuthMode())
	}
	runner.values["CREDIMI_INTERNAL_ADMIN_KEY"] = "admin"
	if runner.AuthMode() != "admin" {
		t.Fatal(runner.AuthMode())
	}
}
