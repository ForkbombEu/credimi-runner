package servicemanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

type upgradeRunner struct {
	calls     [][]string
	output    string
	outputErr error
	runErr    error
}

func (r *upgradeRunner) Run(_ context.Context, _ string, args []string, _ []string) error {
	r.calls = append(r.calls, args)
	return r.runErr
}
func (r *upgradeRunner) Output(context.Context, string, []string, []string) ([]byte, error) {
	return []byte(r.output), r.outputErr
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

func TestWriteServiceSpecFingerprintAndFactory(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteServiceSpecFingerprint(dir, runnerconfig.Bootstrap()); err != nil {
		t.Fatal(err)
	}
	if ForCurrentPlatform(dir) == nil || ForCurrentPlatformWithBootstrap(dir, BootstrapOptions{}) == nil {
		t.Fatal("factory returned nil")
	}
}

func TestDesiredDashboardURLAndRuntimeState(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"controller_id": "c", "config_fingerprint": "f"})
	}))
	defer probe.Close()
	liveDir := t.TempDir()
	metadata := map[string]any{"controller_id": "c", "config_fingerprint": "f", "probe_url": probe.URL, "public_url": "http://127.0.0.1:9051", "identity_token": "id"}
	rawMetadata, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(liveDir, "controller.json"), rawMetadata, 0600); err != nil {
		t.Fatal(err)
	}
	if got := liveDashboardURL(context.Background(), liveDir); got != "http://127.0.0.1:9051" {
		t.Fatalf("live URL=%q", got)
	}
	if got := liveDashboardURL(context.Background(), t.TempDir()); got != "" {
		t.Fatalf("unexpected live URL %q", got)
	}
	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, "controller.json"), []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := liveDashboardURL(context.Background(), badDir); got != "" {
		t.Fatalf("unexpected malformed URL %q", got)
	}
	for _, tc := range []struct{ listen, want string }{{"", "http://127.0.0.1:8051"}, {"127.0.0.1:9051", "http://127.0.0.1:9051"}, {"0.0.0.0:9051", "http://127.0.0.1:9051"}, {"[::]:9051", "http://127.0.0.1:9051"}} {
		if got := desiredDashboardURL(runnerconfig.Config{Server: runnerconfig.ServerConfig{DashboardListen: tc.listen}}); got != tc.want {
			t.Fatalf("%q: %q", tc.listen, got)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime-state.json"), []byte(`{"desired":"running","actual":"failed","last_error":"offline"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var status Status
	populateRuntimeState(dir, &status)
	if status.RuntimeDesired != "running" || status.RuntimeActual != "failed" || status.RuntimeError != "offline" {
		t.Fatalf("status=%+v", status)
	}
}
