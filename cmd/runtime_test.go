package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/spf13/cobra"
)

func TestRuntimeCommandUsesControllerOperationAPI(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/controller/operations/op-1" {
			t.Fatalf("operation path = %s", r.URL.Path)
		}
		phase := controller.PhaseRunning
		if calls > 1 {
			phase = controller.PhaseSucceeded
		}
		_ = json.NewEncoder(w).Encode(controller.Snapshot{ID: "op-1", Phase: phase})
	}))
	defer server.Close()

	client := &controllerClient{baseURL: server.URL, client: http.DefaultClient}
	snapshot, err := waitForLifecycleOperation(context.Background(), client, "op-1")
	if err != nil || snapshot.Phase != controller.PhaseSucceeded || calls != 2 {
		t.Fatalf("wait result = %#v, calls=%d, err=%v", snapshot, calls, err)
	}
	if _, err := waitForLifecycleOperation(context.Background(), client, ""); err == nil {
		t.Fatal("empty operation ID was accepted")
	}
}

func TestLifecycleFailureMessageFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name, errText, message, want string
	}{
		{"error", "failed", "message", "failed"},
		{"message", "", "message", "message"},
		{"generic", "", "", "runtime operation did not succeed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := lifecycleFailureMessage(controller.Snapshot{Error: tc.errText, Message: tc.message})
			if got != tc.want {
				t.Fatalf("message=%q want %q", got, tc.want)
			}
		})
	}
}

func TestRuntimeCommandHTTPHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			http.Error(w, "failed", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	var payload map[string]bool
	client := &controllerClient{baseURL: server.URL, client: http.DefaultClient}
	if err := client.getJSON(context.Background(), "/ok", &payload); err != nil || !payload["ok"] {
		t.Fatalf("JSON helper = %#v, %v", payload, err)
	}
	if err := client.getJSON(context.Background(), "/error", &payload); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error helper = %v", err)
	}
	if err := openDashboardBrowser(""); err == nil {
		t.Fatal("empty dashboard URL was accepted")
	}
	if err := serviceNotRunningError(); err == nil || !strings.Contains(err.Error(), "service start") {
		t.Fatalf("service error = %v", err)
	}
}

func TestDashboardCommandUsesPublishedPublicURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Credimi-Controller-Token") != "token" {
			t.Fatalf("identity token missing")
		}
		_, _ = w.Write([]byte(`{"controller_id":"controller","config_fingerprint":"fingerprint"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	metadata := controller.Metadata{Schema: 1, ControllerID: "controller", ConfigDir: dir, ListenHost: "127.0.0.1", ListenPort: 8051, ProbeURL: server.URL + "/identity", PublicURL: "http://published.example:9051/", ConfigFingerprint: "fingerprint", IdentityToken: "token"}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDir, oldOpen := dashboardConfigDir, dashboardOpen
	dashboardConfigDir, dashboardOpen = dir, false
	t.Cleanup(func() { dashboardConfigDir, dashboardOpen = oldDir, oldOpen })
	command := &cobra.Command{Use: "dashboard"}
	command.SetContext(context.Background())
	var output strings.Builder
	command.SetOut(&output)
	if err := runDashboardCommand(command, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "http://published.example:9051") {
		t.Fatalf("output=%q", output.String())
	}
}

func TestRuntimeCommandCallsRunningDashboard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/controller/identity":
			_ = json.NewEncoder(w).Encode(map[string]string{"controller_id": "controller", "config_fingerprint": "fingerprint"})
		case "/api/controller/runtime/start":
			_ = json.NewEncoder(w).Encode(controller.Snapshot{ID: "op-1", Phase: controller.PhaseQueued})
		case "/api/controller/operations/op-1":
			_ = json.NewEncoder(w).Encode(controller.Snapshot{ID: "op-1", Phase: controller.PhaseSucceeded})
		case "/api/controller/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"runtime": map[string]string{"actual": "running"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	metadata := controller.Metadata{Schema: 1, ControllerID: "controller", PID: os.Getpid(), StartedAt: time.Now(), ConfigDir: dir, ListenHost: "127.0.0.1", ListenPort: 8051, ProbeURL: server.URL + "/internal/controller/identity", PublicURL: server.URL, ConfigFingerprint: "fingerprint", IdentityToken: "token"}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDir := dashboardConfigDir
	dashboardConfigDir = dir
	t.Cleanup(func() { dashboardConfigDir = oldDir })
	command := &cobra.Command{Use: "runtime"}
	command.SetContext(context.Background())
	var output strings.Builder
	command.SetOut(&output)
	if err := runRuntimeAPIAction(command, "start"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Runtime start successfully") {
		t.Fatalf("runtime output = %q", output.String())
	}
	statusCommand := &cobra.Command{Use: "status"}
	statusCommand.SetContext(context.Background())
	statusCommand.SetOut(&output)
	if err := runRuntimeStatus(statusCommand, nil); err != nil {
		t.Fatal(err)
	}
}
