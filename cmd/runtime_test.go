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

	snapshot, err := waitForLifecycleOperation(context.Background(), server.URL, "op-1")
	if err != nil || snapshot.Phase != controller.PhaseSucceeded || calls != 2 {
		t.Fatalf("wait result = %#v, calls=%d, err=%v", snapshot, calls, err)
	}
	if got := controllerBaseURL(controller.Metadata{ProbeURL: server.URL + "/internal/controller/identity"}); got != server.URL {
		t.Fatalf("controller base URL = %q", got)
	}
	if _, err := waitForLifecycleOperation(context.Background(), server.URL, ""); err == nil {
		t.Fatal("empty operation ID was accepted")
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
	if err := getLifecycleJSON(context.Background(), server.URL+"/ok", &payload); err != nil || !payload["ok"] {
		t.Fatalf("JSON helper = %#v, %v", payload, err)
	}
	if err := getLifecycleJSON(context.Background(), server.URL+"/error", &payload); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error helper = %v", err)
	}
	if err := openDashboardBrowser(""); err == nil {
		t.Fatal("empty dashboard URL was accepted")
	}
	if err := serviceNotRunningError(); err == nil || !strings.Contains(err.Error(), "service start") {
		t.Fatalf("service error = %v", err)
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
	metadata := controller.Metadata{Schema: 1, ControllerID: "controller", PID: os.Getpid(), StartedAt: time.Now(), ConfigDir: dir, ListenHost: "127.0.0.1", ListenPort: 8051, ProbeURL: server.URL + "/internal/controller/identity", ConfigFingerprint: "fingerprint", IdentityToken: "token"}
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
