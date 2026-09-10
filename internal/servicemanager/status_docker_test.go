//go:build !darwin

package servicemanager

import (
	"context"
	"encoding/json"
	"errors"
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

type noDockerCallsRunner struct{ calls int }

type serviceMatchRunner struct {
	id          string
	label       string
	environment string
	psErr       error
	inspectErr  error
}

func (r *noDockerCallsRunner) Run(context.Context, string, []string, []string) error {
	r.calls++
	return errors.New("unexpected Docker call")
}
func (r *noDockerCallsRunner) Output(context.Context, string, []string, []string) ([]byte, error) {
	r.calls++
	return nil, errors.New("unexpected Docker call")
}

func (r *upgradeRunner) Run(_ context.Context, _ string, args []string, _ []string) error {
	r.calls = append(r.calls, args)
	return nil
}
func (r *upgradeRunner) Output(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "image inspect") {
		return []byte(`["ghcr.io/forkbombeu/credimi-runner@sha256:test"]`), nil
	}
	if strings.Contains(joined, "inspect --format") {
		return []byte("sha256:local\n"), nil
	}
	return []byte(r.output), nil
}

func (r *serviceMatchRunner) Run(context.Context, string, []string, []string) error { return nil }
func (r *serviceMatchRunner) Output(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "ps -q runner") {
		return []byte(r.id + "\n"), r.psErr
	}
	if strings.Contains(joined, "service-fingerprint") {
		return []byte(r.label + "\n" + r.environment), r.inspectErr
	}
	return nil, nil
}

func TestDockerStatusSurfacesRunningServiceInspectionErrors(t *testing.T) {
	for _, tc := range []struct {
		name, environment string
	}{
		{"inspect error", ""},
		{"malformed environment", "not-json"},
		{"partial applied metadata", `["CREDIMI_APPLIED_SERVICE_NEEDS_USB=true"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := runnerconfig.Bootstrap()
			cfg.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
			cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
			cfg.Temporal.Address = "temporal:7233"
			if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
				t.Fatal(err)
			}
			runner := &serviceMatchRunner{id: "container", environment: tc.environment}
			if tc.name == "inspect error" {
				runner.inspectErr = errors.New("inspect failed")
			}
			m := NewDockerManager(dir, "")
			m.Runner = runner
			if _, err := m.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "inspect running service metadata") {
				t.Fatalf("status error=%v", err)
			}
		})
	}
}

func TestDockerStatusAcceptsBootstrapComposeWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &serviceMatchRunner{id: "bootstrap-container"}
	m := NewDockerManager(dir, "")
	m.Runner = runner
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.ServiceRestartRequired {
		t.Fatalf("bootstrap status=%+v", status)
	}
}

func TestDockerStatusUsesCapabilityCompatibilityForRetainedResources(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := testHost("/home/alice")
	applied := runnerconfig.Bootstrap()
	applied.Android.Network = "host"
	applied.Devices = []runnerconfig.DeviceConfig{
		{Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "A"}},
		{Type: runnerconfig.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{BaseName: "credimi"}},
	}
	desired := applied
	desired.Devices = desired.Devices[:1]
	spec, err := BuildServiceSpec(applied, host)
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]string, 0, len(spec.Environment))
	for key, value := range spec.Environment {
		entries = append(entries, key+"="+value)
	}
	envJSON, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewDockerManager(dir, "")
	manager.LoadConfig = func() (runnerconfig.Config, error) { return desired, nil }
	manager.Runner = &serviceMatchRunner{id: "container", environment: string(envJSON)}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.ServiceRestartRequired {
		t.Fatal("retained emulator capability incorrectly requires service replacement")
	}
}

func TestDockerServiceMatchesExplicitConfig(t *testing.T) {
	cfg := runnerconfig.Bootstrap()
	cfg.Credimi.URL = "http://203.0.113.10:8090"
	cfg.Temporal.Address = "203.0.113.11:7233"
	for _, tc := range []struct {
		name              string
		id, label         string
		psErr, inspectErr error
		wantMatch         bool
		wantErr           error
	}{
		{name: "no running service"},
		{name: "fingerprint mismatch", id: "container", label: "wrong"},
		{name: "fingerprint match", id: "container", wantMatch: true},
		{name: "ps error", psErr: errors.New("ps failed"), wantErr: errors.New("ps failed")},
		{name: "inspect error", id: "container", inspectErr: errors.New("inspect failed"), wantErr: errors.New("inspect failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			manager := NewDockerManager(dir, "")
			host, err := ResolveHostContext(dir)
			if err != nil {
				t.Fatal(err)
			}
			host = ResolveServiceHostContext(cfg, host)
			spec, err := BuildServiceSpec(cfg, host)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantMatch {
				tc.label = spec.Fingerprint()
			}
			runner := &serviceMatchRunner{id: tc.id, label: tc.label, psErr: tc.psErr, inspectErr: tc.inspectErr}
			manager.Runner = runner
			got, gotErr := manager.ServiceMatchesConfig(context.Background(), cfg)
			if tc.wantErr != nil {
				if gotErr == nil || !strings.Contains(gotErr.Error(), tc.wantErr.Error()) {
					t.Fatalf("error=%v, want %v", gotErr, tc.wantErr)
				}
				return
			}
			if gotErr != nil || got != tc.wantMatch {
				t.Fatalf("match=%v error=%v, want %v", got, gotErr, tc.wantMatch)
			}
		})
	}
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

func TestDockerPreCreationStatusAndStopMakeNoDockerCalls(t *testing.T) {
	tests := []struct {
		name, network, dashboard, wantURL string
		writeConfig                       bool
		malformed                         bool
	}{
		{"no config", "", "", "http://127.0.0.1:8051", false, false},
		{"bridge config", "bridge", "192.0.2.10:9051", "http://127.0.0.1:9051", true, false},
		{"host config", "host", "127.0.0.2:9051", "http://127.0.0.2:9051", true, false},
		{"malformed config", "", "", "", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.writeConfig {
				if tc.malformed {
					if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not valid = ["), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					cfg := runnerconfig.Bootstrap()
					cfg.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
					cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
					cfg.Temporal.Address = "temporal:7233"
					cfg.Server.DashboardListen = tc.dashboard
					cfg.Android.Network = tc.network
					cfg.Storage.StateDir = filepath.Join(dir, "state")
					cfg.Storage.ArtifactRetention = runnerconfig.Duration(time.Hour)
					if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
						t.Fatal(err)
					}
				}
			}
			runner := &noDockerCallsRunner{}
			m := NewDockerManager(dir, "")
			m.Runner = runner
			status, err := m.Status(context.Background())
			if tc.malformed {
				if err == nil {
					t.Fatal("malformed config unexpectedly succeeded")
				}
			} else if err != nil || status.Running || status.DashboardURL != tc.wantURL {
				t.Fatalf("status=%+v err=%v want URL %q", status, err, tc.wantURL)
			}
			if runner.calls != 0 {
				t.Fatalf("Docker calls=%d", runner.calls)
			}
			if err := m.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if runner.calls != 0 {
				t.Fatalf("Docker calls after Stop=%d", runner.calls)
			}
		})
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
