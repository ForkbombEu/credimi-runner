package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
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
		Devices:       []config.DeviceConfig{{ID: "acme/runner/one", Name: "One", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "usb", Serial: "one"}}},
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
	if runner.Get("CREDIMI_DEVICE_1_SERIAL") != "one" {
		t.Fatalf("devices=%#v", runner.Snapshot())
	}
}

func TestLoadConfigUsesEphemeralBootstrapValuesOnlyBeforeTOMLExists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dashboardruntime.BootstrapImageEnv, "credimi-runner:local")
	t.Setenv(dashboardruntime.BootstrapPullPolicyEnv, "never")
	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Get("ANDROID_RUNNER_IMAGE") != "credimi-runner:local" || loaded.Get("ANDROID_PULL_POLICY") != "never" {
		t.Fatalf("bootstrap values = %#v", loaded.Snapshot())
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("bootstrap load persisted config: %v", err)
	}
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), dashboardTestConfig(dir)); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Get("ANDROID_RUNNER_IMAGE") == "credimi-runner:local" || loaded.Get("ANDROID_PULL_POLICY") == "never" {
		t.Fatalf("bootstrap values overrode typed TOML: %#v", loaded.Snapshot())
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

func TestConfigCompatibilityRoundTripsManualPublicPort(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), dashboardTestConfig(dir)); err != nil {
		t.Fatal(err)
	}
	runner, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	values := runner.Snapshot()
	values["RUNNER_PUBLIC_PORT"] = "8050"
	typed, err := dashboardruntime.TypedConfigFromValues(dashboardruntime.Values(values))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), typed); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	compatibility := dashboardruntime.ValuesFromTypedConfig(loaded)
	if compatibility["RUNNER_PUBLIC_PORT"] != "8050" {
		t.Fatalf("compatibility public port = %q", compatibility["RUNNER_PUBLIC_PORT"])
	}
}

func TestConfigApplyPersistsManualPublicPortEdit(t *testing.T) {
	dir := t.TempDir()
	cfg := dashboardTestConfig(dir)
	cfg.Exposure.PublicPort = "8050"
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	runner, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Apply(map[string]string{"RUNNER_PUBLIC_PORT": "9000"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Get("RUNNER_PUBLIC_PORT") != "9000" {
		t.Fatalf("reloaded manual public port = %q", reloaded.Get("RUNNER_PUBLIC_PORT"))
	}
}

func TestValidatePortRange(t *testing.T) {
	for _, value := range []string{"abc", "0", "65536", "99999", "00000"} {
		if errs := Validate(map[string]string{"RUNNER_PUBLIC_PORT": value}); errs["RUNNER_PUBLIC_PORT"] == "" {
			t.Fatalf("invalid public port %q accepted", value)
		}
	}
	for _, value := range []string{"1", "80", "8050", "65535", ""} {
		if errs := Validate(map[string]string{"RUNNER_PUBLIC_PORT": value}); errs["RUNNER_PUBLIC_PORT"] != "" {
			t.Fatalf("valid public port %q rejected: %v", value, errs)
		}
	}
}

func TestValidateManualPublicURL(t *testing.T) {
	for _, value := range []string{"runner.example", "ftp://runner.example", "https:///missing-host"} {
		if errs := Validate(map[string]string{"RUNNER_PUBLIC_URL": value}); errs["RUNNER_PUBLIC_URL"] == "" {
			t.Fatalf("invalid manual public URL %q accepted", value)
		}
	}
	for _, value := range []string{"http://runner.example", "https://runner.example/path"} {
		if errs := Validate(map[string]string{"RUNNER_PUBLIC_URL": value}); errs["RUNNER_PUBLIC_URL"] != "" {
			t.Fatalf("valid manual public URL %q rejected: %v", value, errs)
		}
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

func TestConfigBoolAndConfigDirUseCompatibilityDefaults(t *testing.T) {
	cfg := &Config{values: map[string]string{"enabled": "yes", "disabled": "false"}}
	if !cfg.Bool("enabled") || cfg.Bool("disabled") || cfg.Bool("missing") {
		t.Fatalf("boolean compatibility values were not interpreted correctly")
	}
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", t.TempDir())
	if ConfigDir() == "" {
		t.Fatal("ConfigDir returned empty path")
	}
}

func TestConfigCompatibilityFormattingHelpers(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{"", ""}, {"plain", "plain"}, {"with space", `"with space"`}, {`with "quote"`, "\"with \\\"quote\\\"\""},
	} {
		if got := quote(tc.input); got != tc.want {
			t.Fatalf("quote(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	keys := sortedKeys(map[string]string{"z": "1", "a": "2", "m": "3"})
	if strings.Join(keys, ",") != "a,m,z" {
		t.Fatalf("sorted keys = %v", keys)
	}
}

func TestTruthyCompatibilityValues(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "on", " On "} {
		if !isTruthyFormValue(value) {
			t.Fatalf("truthy value %q rejected", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "off", "maybe"} {
		if isTruthyFormValue(value) {
			t.Fatalf("falsey value %q accepted", value)
		}
	}
}
