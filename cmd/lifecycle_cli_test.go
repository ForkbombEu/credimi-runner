package cmd

import (
	"bytes"
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
	"github.com/forkbombeu/credimi-runner/internal/lifecyclelog"
	"github.com/spf13/cobra"
)

func TestLifecycleCLIStatusAndRuntimeAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/internal/controller/identity":
			if request.Header.Get("X-Credimi-Controller-Token") != "test-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"controller_id":"controller-1","config_fingerprint":"fingerprint"}`))
		case "/api/controller/status":
			_, _ = w.Write([]byte(`{"runtime":{"runner_running":true},"operation":{"id":"op-1"}}`))
		case "/api/controller/runtime/start", "/api/controller/runtime/stop":
			_, _ = w.Write([]byte(`{"id":"op-2","message":"operation queued"}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	setLifecycleConfigDir(t, dir)
	writeLifecycleMetadata(t, dir, server.URL+"/internal/controller/identity", 42)

	command, output := lifecycleTestCommand()
	if err := runLifecycleStatus(command, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Dashboard: running") || !strings.Contains(output.String(), "Lifecycle:") {
		t.Fatalf("status output = %q", output.String())
	}

	output.Reset()
	if err := runLifecycleRuntimeAction(command, "start"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Runner start requested (operation op-2)") {
		t.Fatalf("runtime action output = %q", output.String())
	}

	output.Reset()
	if err := runLifecycleRuntimeAction(command, "stop"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Runner stop requested (operation op-2)") {
		t.Fatalf("stop output = %q", output.String())
	}
}

func TestLifecycleCLIStoppedDashboardAndLogs(t *testing.T) {
	dir := t.TempDir()
	setLifecycleConfigDir(t, dir)
	command, output := lifecycleTestCommand()
	if err := runLifecycleStatus(command, nil); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Dashboard: stopped\n" {
		t.Fatalf("status output = %q", output.String())
	}

	event := lifecyclelog.Event{Schema: lifecyclelog.SchemaVersion, Timestamp: time.Now().UTC(), Level: lifecyclelog.LevelInfo, Event: "runtime.started", Message: "runner started"}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lifecycleLogPath(), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	lifecycleLogLines = 1
	output.Reset()
	if err := runLifecycleLogTail(command, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "runtime.started") {
		t.Fatalf("tail output = %q", output.String())
	}

	output.Reset()
	lifecycleLogOutput = filepath.Join(dir, "report.md")
	if err := runLifecycleLogExport(command, nil); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(lifecycleLogOutput)
	if err != nil || !strings.Contains(string(report), "runtime.started") {
		t.Fatalf("report = %q err=%v", report, err)
	}
}

func TestLifecycleCLIDashboardOpenAndStopSafety(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/internal/controller/identity" {
			http.NotFound(w, request)
			return
		}
		_, _ = w.Write([]byte(`{"controller_id":"controller-1","config_fingerprint":"fingerprint"}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	setLifecycleConfigDir(t, dir)
	writeLifecycleMetadata(t, dir, server.URL+"/internal/controller/identity", 1)
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	command, output := lifecycleTestCommand()
	if err := runLifecycleDashboardOpen(command, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "no local graphical display") {
		t.Fatalf("open output = %q", output.String())
	}
	if err := runLifecycleDashboardStop(command, nil); err == nil || !strings.Contains(err.Error(), "invalid dashboard PID") {
		t.Fatalf("stop error = %v", err)
	}
}

func TestLifecycleJSONHelpersAndBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/bad" {
			http.Error(w, "bad", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	ctx := context.Background()
	var payload map[string]bool
	if err := getLifecycleJSON(ctx, server.URL+"/good", &payload); err != nil || !payload["ok"] {
		t.Fatalf("GET payload=%#v err=%v", payload, err)
	}
	if err := postLifecycleJSON(ctx, server.URL+"/good", &payload); err != nil || !payload["ok"] {
		t.Fatalf("POST payload=%#v err=%v", payload, err)
	}
	if err := getLifecycleJSON(ctx, server.URL+"/bad", &payload); err == nil {
		t.Fatal("expected GET failure")
	}
	if err := postLifecycleJSON(ctx, server.URL+"/bad", &payload); err == nil {
		t.Fatal("expected POST failure")
	}
	if got := controllerBaseURL(controller.Metadata{ProbeURL: "http://127.0.0.1:8051/internal/controller/identity"}); got != "http://127.0.0.1:8051" {
		t.Fatalf("controllerBaseURL = %q", got)
	}
}

func TestLifecycleCLIReportsMissingAndStaleControllers(t *testing.T) {
	dir := t.TempDir()
	setLifecycleConfigDir(t, dir)
	command, output := lifecycleTestCommand()
	for name, run := range map[string]func() error{
		"runtime action": func() error { return runLifecycleRuntimeAction(command, "start") },
		"dashboard open": func() error { return runLifecycleDashboardOpen(command, nil) },
		"dashboard stop": func() error { return runLifecycleDashboardStop(command, nil) },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "dashboard is not running") {
			t.Fatalf("%s error = %v", name, err)
		}
	}
	if err := runLifecycleLogTail(command, nil); err == nil {
		t.Fatal("expected missing lifecycle log error")
	}
	if err := runLifecycleLogExport(command, nil); err == nil {
		t.Fatal("expected missing lifecycle report error")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "stale", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	writeLifecycleMetadata(t, dir, server.URL+"/internal/controller/identity", 42)
	command, output = lifecycleTestCommand()
	if err := runLifecycleStatus(command, nil); err != nil || !strings.Contains(output.String(), "stale") {
		t.Fatalf("stale status error=%v output=%q", err, output.String())
	}
}

func TestReopenExistingDashboardAndDisplayHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Credimi-Controller-Token") != "test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"controller_id":"controller-1","config_fingerprint":"fingerprint"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	writeLifecycleMetadata(t, dir, server.URL, 42)
	originalOpen := dashboardOpen
	dashboardOpen = false
	t.Cleanup(func() { dashboardOpen = originalOpen })
	command, output := lifecycleTestCommand()
	if err := reopenExistingDashboard(command, dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "already running") {
		t.Fatalf("reopen output = %q", output.String())
	}
	if err := reopenExistingDashboard(command, t.TempDir()); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("missing metadata error = %v", err)
	}
	if got := dashboardDisplayURL("127.0.0.1", 8051); got != "http://127.0.0.1:8051" {
		t.Fatalf("dashboardDisplayURL = %q", got)
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if dashboardCanOpenBrowser() {
		t.Fatal("headless Linux environment should not open a browser")
	}
	if err := openDashboardBrowser(""); err == nil {
		t.Fatal("expected empty dashboard URL error")
	}
}

func lifecycleTestCommand() (*cobra.Command, *bytes.Buffer) {
	command := &cobra.Command{}
	command.SetContext(context.Background())
	output := &bytes.Buffer{}
	command.SetOut(output)
	return command, output
}

func setLifecycleConfigDir(t *testing.T, dir string) {
	t.Helper()
	originalDir, originalLines, originalOutput := dashboardConfigDir, lifecycleLogLines, lifecycleLogOutput
	dashboardConfigDir = dir
	lifecycleLogLines = 100
	lifecycleLogOutput = ""
	t.Cleanup(func() {
		dashboardConfigDir, lifecycleLogLines, lifecycleLogOutput = originalDir, originalLines, originalOutput
	})
}

func writeLifecycleMetadata(t *testing.T, dir, probeURL string, pid int) {
	t.Helper()
	metadata := controller.Metadata{Schema: 1, ControllerID: "controller-1", PID: pid, ListenPort: 8051, ProbeURL: probeURL, PublicURL: "http://dashboard.example:8051", ConfigFingerprint: "fingerprint", IdentityToken: "test-token"}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
