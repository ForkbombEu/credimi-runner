package cmd

import (
	"context"
	"encoding/json"
	"github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/controller"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControllerClientAuthentication(t *testing.T) {
	for _, tc := range []struct{ name, token, want string }{{"none", "", ""}, {"bearer", "secret", "Bearer secret"}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != tc.want {
					t.Fatalf("authorization=%q", got)
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			client := &controllerClient{baseURL: server.URL, token: tc.token, client: server.Client()}
			var response map[string]bool
			if err := client.postJSON(context.Background(), "/runtime", &response); err != nil || !response["ok"] {
				t.Fatalf("response=%v err=%v", response, err)
			}
			if err := client.getJSON(context.Background(), "/operations/1", &response); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNewControllerClientLoadsCurrentDashboardToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/probe" {
			_ = json.NewEncoder(w).Encode(map[string]string{"controller_id": "c", "config_fingerprint": "f"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer server.Close()
	dir := t.TempDir()
	cfg := config.Bootstrap()
	cfg.Runner.ID = "org/runner"
	cfg.Runner.Name = "runner"
	cfg.Runner.Organization = "org"
	cfg.Credimi.URL = "https://example.test"
	cfg.Credimi.UserAPIKey = "user-key"
	cfg.Temporal.Address = "temporal:7233"
	cfg.Server.ReadHeaderTimeout = config.Duration(time.Second)
	cfg.Server.ShutdownTimeout = config.Duration(time.Second)
	cfg.Storage.StateDir = filepath.Join(dir, "state")
	cfg.Storage.ArtifactRetention = config.Duration(time.Hour)
	cfg.Server.DashboardToken = "secret"
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	metadata := controller.Metadata{Schema: 1, ControllerID: "c", ConfigDir: dir, ListenPort: 8051, ProbeURL: server.URL + "/probe", PublicURL: server.URL, ConfigFingerprint: "f", IdentityToken: "id"}
	raw, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := newControllerClient(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]bool
	if err := client.getJSON(context.Background(), "/status", &out); err != nil || !out["ok"] {
		t.Fatalf("out=%v err=%v", out, err)
	}
}
