//go:build !darwin

package servicemanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

type upgradeRunner struct {
	calls  [][]string
	output string
}

func (r *upgradeRunner) Run(_ context.Context, _ string, args []string, _ []string) error {
	r.calls = append(r.calls, args)
	return nil
}
func (r *upgradeRunner) Output(context.Context, string, []string, []string) ([]byte, error) {
	return []byte(r.output), nil
}
func TestDockerUpgradeImageUsesAppliedComposeAndRunningIntent(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		wantUp       bool
	}{{"running", "container-id\n", true}, {"stopped", "\n", false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			r := &upgradeRunner{output: tc.output}
			m := NewDockerManager(dir, "")
			m.Runner = r
			m.LoadConfig = func() (runnerconfig.Config, error) { return runnerconfig.Bootstrap(), nil }
			if err := m.UpgradeImage(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if len(r.calls) < 1 || !strings.Contains(strings.Join(r.calls[0], " "), "pull runner") {
				t.Fatalf("calls=%v", r.calls)
			}
			if tc.wantUp && !strings.Contains(strings.Join(r.calls[len(r.calls)-1], " "), "force-recreate") {
				t.Fatalf("recreate calls=%v", r.calls)
			}
			if !tc.wantUp && len(r.calls) != 1 {
				t.Fatalf("stopped calls=%v", r.calls)
			}
		})
	}
}

func TestDockerStatusHasDefaultDashboardURLWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	r := &upgradeRunner{}
	m := NewDockerManager(dir, "")
	m.Runner = r
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.DashboardURL != "http://127.0.0.1:8051" {
		t.Fatalf("dashboard URL=%q", status.DashboardURL)
	}
}

func TestDockerStatusStoppedDashboardURLUsesFinalNetworkMode(t *testing.T) {
	for _, tc := range []struct {
		name, listen, network, want string
	}{
		{"bridge explicit address", "192.0.2.10:9051", "credimi-custom", "http://127.0.0.1:9051"},
		{"bridge alternate loopback", "127.0.0.2:9051", "bridge", "http://127.0.0.1:9051"},
		{"bridge ipv6 loopback", "[::1]:9051", "bridge", "http://127.0.0.1:9051"},
		{"host alternate loopback", "127.0.0.2:9051", "host", "http://127.0.0.2:9051"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := runnerconfig.Bootstrap()
			cfg.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
			cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
			cfg.Temporal.Address = "temporal:7233"
			cfg.Server.DashboardListen = tc.listen
			cfg.Server.APIListen = "127.0.0.1:8050"
			cfg.Server.ReadHeaderTimeout = runnerconfig.Duration(time.Second)
			cfg.Server.ShutdownTimeout = runnerconfig.Duration(time.Second)
			cfg.Exposure.Mode = "manual"
			cfg.Exposure.PublicURL = "https://runner.example"
			cfg.Storage.StateDir = filepath.Join(dir, "state")
			cfg.Storage.ArtifactRetention = runnerconfig.Duration(time.Hour)
			cfg.Android.Network = tc.network
			if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
				t.Fatal(err)
			}
			m := NewDockerManager(dir, "")
			m.Runner = &upgradeRunner{}
			status, err := m.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if status.Running {
				t.Fatal("expected stopped service")
			}
			if status.DashboardURL != tc.want {
				t.Fatalf("dashboard URL=%q want %q", status.DashboardURL, tc.want)
			}
		})
	}
}
