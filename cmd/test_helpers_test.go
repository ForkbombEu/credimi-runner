package cmd

import (
	"os"
	"path/filepath"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

func restoreEnv(t *testing.T, name string) {
	t.Helper()
	value, present := os.LookupEnv(name)
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func writeTestTOMLConfig(t *testing.T, dir string) {
	writeTestTOMLConfigURL(t, dir, "https://credimi.example")
}

func writeTestTOMLConfigURL(t *testing.T, dir, credimiURL string) {
	t.Helper()
	cfg := runnerconfig.Config{
		SchemaVersion: runnerconfig.SchemaVersion,
		Runner:        runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"},
		Credimi:       runnerconfig.CredimiConfig{URL: credimiURL, AuthMode: "user", UserAPIKey: "test"},
		Temporal:      runnerconfig.TemporalConfig{Address: "temporal.example:7233"},
		Exposure:      runnerconfig.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example"},
		Devices: []runnerconfig.DeviceConfig{{
			ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
			AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "usb-1"},
		}},
	}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
}
