package servicemanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

func TestWriteServiceSpecFingerprintAndFactory(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteServiceSpecFingerprint(dir, runnerconfig.Bootstrap()); err != nil {
		t.Fatal(err)
	}
	if ForCurrentPlatform(dir) == nil || ForCurrentPlatformWithBootstrap(dir, BootstrapOptions{}) == nil {
		t.Fatal("factory returned nil")
	}
}

func TestEquivalentListenerNormalizesLoopback(t *testing.T) {
	for _, tc := range []struct {
		wh, wp, ah, ap string
		want           bool
	}{{"127.0.0.1", "8051", "127.0.0.1", "8051", true}, {"localhost", "8051", "127.0.0.1", "8051", true}, {"127.0.0.1", "8051", "127.0.0.2", "8051", false}, {"127.0.0.1", "8051", "::1", "8051", false}, {"::1", "8051", "::1", "8051", true}, {"192.0.2.1", "8051", "192.0.2.1", "8051", true}, {"127.0.0.1", "9051", "127.0.0.1", "8051", false}} {
		if got := isEquivalentListener(tc.wh, tc.wp, tc.ah, tc.ap); got != tc.want {
			t.Fatalf("listener=%v want %v", got, tc.want)
		}
	}
}

func TestDesiredDashboardURLAndRuntimeState(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"controller_id": "c", "config_fingerprint": "f"})
	}))
	defer probe.Close()
	liveDir := t.TempDir()
	metadata := map[string]any{"schema": 1, "controller_id": "c", "config_dir": liveDir, "listen_port": 8051, "config_fingerprint": "f", "probe_url": probe.URL, "public_url": "http://127.0.0.1:9051", "identity_token": "id"}
	rawMetadata, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(liveDir, "controller.json"), rawMetadata, 0600); err != nil {
		t.Fatal(err)
	}
	if live, err := readLiveController(context.Background(), liveDir); err != nil || live.PublicURL != "http://127.0.0.1:9051" {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	if _, err := readLiveController(context.Background(), t.TempDir()); err == nil {
		t.Fatal("unexpected live controller success")
	}
	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, "controller.json"), []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLiveController(context.Background(), badDir); err == nil {
		t.Fatal("unexpected malformed controller success")
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
