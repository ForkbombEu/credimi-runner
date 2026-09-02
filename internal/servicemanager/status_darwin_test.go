//go:build darwin

package servicemanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	controller "github.com/forkbombeu/credimi-runner/internal/controller/identity"
)

func writeDarwinStatusConfig(t *testing.T, dir, listen string) {
	t.Helper()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal:7233"
	cfg.Server.DashboardListen = listen
	cfg.Server.APIListen = "127.0.0.1:8050"
	cfg.Server.ReadHeaderTimeout = runnerconfig.Duration(time.Second)
	cfg.Server.ShutdownTimeout = runnerconfig.Duration(time.Second)
	cfg.Exposure.Mode = "manual"
	cfg.Exposure.PublicURL = "https://runner.example"
	cfg.Storage.StateDir = filepath.Join(dir, "state")
	cfg.Storage.ArtifactRetention = runnerconfig.Duration(time.Hour)
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
}

func writeDarwinStatusMetadata(t *testing.T, dir string, metadata controller.Metadata) {
	t.Helper()
	metadata.Schema = 1
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchAgentStatusUsesEffectiveDashboardListener(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"controller_id":"runner","config_fingerprint":"fingerprint"}`))
	}))
	defer probe.Close()
	for _, tc := range []struct {
		name, desired, applied string
		port                   int
		wantRestart            bool
	}{
		{"same", "127.0.0.1:8051", "127.0.0.1", 8051, false},
		{"same custom port", "127.0.0.1:9051", "127.0.0.1", 9051, false},
		{"wildcard ipv4", "0.0.0.0:8051", "127.0.0.1", 8051, false},
		{"wildcard ipv6", "[::]:8051", "127.0.0.1", 8051, false},
		{"different host", "127.0.0.2:8051", "127.0.0.1", 8051, true},
		{"different port", "127.0.0.1:9051", "127.0.0.1", 8051, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeDarwinStatusConfig(t, dir, tc.desired)
			writeDarwinStatusMetadata(t, dir, controller.Metadata{
				ControllerID: "runner", ConfigDir: dir, ListenHost: tc.applied, ListenPort: tc.port,
				ProbeURL: probe.URL, PublicURL: "http://127.0.0.1:8051", ConfigFingerprint: "fingerprint", IdentityToken: "token",
			})
			m := &LaunchAgentManager{ConfigDir: dir, Run: func(context.Context, string, ...string) error { return nil }}
			status, err := m.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if status.ServiceRestartRequired != tc.wantRestart {
				t.Fatalf("restart required=%v want %v", status.ServiceRestartRequired, tc.wantRestart)
			}
		})
	}
}

func TestLaunchAgentStatusStoppedUsesDesiredDashboardURL(t *testing.T) {
	dir := t.TempDir()
	writeDarwinStatusConfig(t, dir, "127.0.0.1:9051")
	m := &LaunchAgentManager{ConfigDir: dir, Run: func(context.Context, string, ...string) error {
		return errors.New("not loaded")
	}}
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Running || status.ServiceRestartRequired || status.DashboardURL != "http://127.0.0.1:9051" {
		t.Fatalf("status=%+v", status)
	}
}

func TestLaunchAgentStatusUsesLiveControllerWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"controller_id":"runner","config_fingerprint":"fingerprint"}`))
	}))
	defer probe.Close()
	writeDarwinStatusMetadata(t, dir, controller.Metadata{
		ControllerID: "runner", ConfigDir: dir, ListenHost: "127.0.0.1", ListenPort: 9051,
		ProbeURL: probe.URL, PublicURL: "http://127.0.0.1:9051", ConfigFingerprint: "fingerprint", IdentityToken: "token",
	})
	m := &LaunchAgentManager{ConfigDir: dir, Run: func(context.Context, string, ...string) error { return nil }}
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.DashboardURL != "http://127.0.0.1:9051" {
		t.Fatalf("status=%+v", status)
	}
}
