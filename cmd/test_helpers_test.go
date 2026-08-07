package cmd

import (
	"net"
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
	writeTestTOMLConfigPortsURL(t, dir, "0.0.0.0:8050", "127.0.0.1:8051", credimiURL)
}

func writeTestTOMLConfigPortsURL(t *testing.T, dir, apiListen, dashboardListen, credimiURL string) {
	t.Helper()
	cfg := runnerconfig.Config{
		SchemaVersion: runnerconfig.SchemaVersion,
		Runner:        runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"},
		Credimi:       runnerconfig.CredimiConfig{URL: credimiURL, AuthMode: "user", UserAPIKey: "test"},
		Temporal:      runnerconfig.TemporalConfig{Address: "temporal.example:7233"},
		Server:        runnerconfig.ServerConfig{APIListen: apiListen, DashboardListen: dashboardListen},
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

func lifecycleFreeListenAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}
