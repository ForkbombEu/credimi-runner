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
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
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
		case "/api/controller/operations/op-2":
			_, _ = w.Write([]byte(`{"id":"op-2","phase":"succeeded"}`))
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
	if output.String() != "Runner started successfully.\n" {
		t.Fatalf("runtime action output = %q", output.String())
	}

	output.Reset()
	if err := runLifecycleRuntimeAction(command, "stop"); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Runner stopped successfully.\n" {
		t.Fatalf("stop output = %q", output.String())
	}
}

func TestLifecycleRuntimeActionReportsOperationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/internal/controller/identity":
			_, _ = w.Write([]byte(`{"controller_id":"controller-1","config_fingerprint":"fingerprint"}`))
		case "/api/controller/runtime/start":
			_, _ = w.Write([]byte(`{"id":"op-2"}`))
		case "/api/controller/operations/op-2":
			_, _ = w.Write([]byte(`{"id":"op-2","phase":"failed","error":"device unavailable"}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	setLifecycleConfigDir(t, dir)
	writeLifecycleMetadata(t, dir, server.URL+"/internal/controller/identity", 42)
	command, _ := lifecycleTestCommand()
	err := runLifecycleRuntimeAction(command, "start")
	if err == nil || !strings.Contains(err.Error(), "runner start failed: device unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestLifecycleActionPastTense(t *testing.T) {
	for action, want := range map[string]string{"start": "started", "stop": "stopped", "restart": "restarted"} {
		if got := lifecycleActionPastTense(action); got != want {
			t.Fatalf("%s = %q, want %q", action, got, want)
		}
	}
}

func TestLifecycleCLIStoppedDashboardAndLogs(t *testing.T) {
	dir := t.TempDir()
	setLifecycleConfigDir(t, dir)
	command, output := lifecycleTestCommand()
	if err := runLifecycleStatus(command, nil); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Dashboard: stopped\nRunner: not configured\n" {
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
	if err := runLifecycleRuntimeAction(command, "start"); err == nil || !strings.Contains(err.Error(), "runner configuration is missing") {
		t.Fatalf("runtime action error = %v", err)
	}
	for name, run := range map[string]func() error{
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
	if err := runLifecycleStatus(command, nil); err != nil || !strings.Contains(output.String(), "Dashboard: unavailable") {
		t.Fatalf("stale status error=%v output=%q", err, output.String())
	}
}

type lifecycleDirectFakeManager struct {
	starts, stops, restarts int
	status                  dashboardruntime.RuntimeStatus
}

func (f *lifecycleDirectFakeManager) Start(context.Context) error   { f.starts++; return nil }
func (f *lifecycleDirectFakeManager) Stop(context.Context) error    { f.stops++; return nil }
func (f *lifecycleDirectFakeManager) Restart(context.Context) error { f.restarts++; return nil }
func (f *lifecycleDirectFakeManager) UpdateImage(context.Context) error {
	return nil
}
func (f *lifecycleDirectFakeManager) Configure(dashboardruntime.Values) {}
func (f *lifecycleDirectFakeManager) SetPublicURL(string)               {}
func (f *lifecycleDirectFakeManager) Status(context.Context) dashboardruntime.RuntimeStatus {
	return f.status
}
func (f *lifecycleDirectFakeManager) Logs(context.Context, int) ([]dashboardruntime.LogLine, error) {
	return nil, nil
}

func TestLifecycleCLIDirectRuntimeControlWithoutDashboard(t *testing.T) {
	dir := t.TempDir()
	setLifecycleConfigDir(t, dir)
	registered := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { registered++; w.WriteHeader(http.StatusOK) }))
	defer api.Close()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("CREDIMI_URL="+api.URL+"\nCREDIMI_USER_API_KEY=test\nCREDIMI_RUNNER_ID=acme/runner\nCREDIMI_RUNNER_BACKEND=host\nCREDIMI_SERVICE_MODE=manual\nRUNNER_PUBLIC_URL=https://runner.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := &lifecycleDirectFakeManager{status: dashboardruntime.RuntimeStatus{RunnerRunning: true}}
	originalExecutable, originalFactory := lifecycleRuntimeExecutable, lifecycleRuntimeManagerFactory
	originalReady := lifecycleRuntimeWaitReady
	lifecycleRuntimeExecutable = func() (string, error) { return "credimi-runner", nil }
	lifecycleRuntimeManagerFactory = func(string, string, dashboardruntime.Values) dashboardruntime.Manager { return manager }
	readyCalls := 0
	lifecycleRuntimeWaitReady = func(context.Context, dashboardruntime.Values) error { readyCalls++; return nil }
	t.Cleanup(func() {
		lifecycleRuntimeExecutable, lifecycleRuntimeManagerFactory = originalExecutable, originalFactory
		lifecycleRuntimeWaitReady = originalReady
	})

	command, output := lifecycleTestCommand()
	if err := runLifecycleRuntimeAction(command, "start"); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Runner started successfully.\n" || manager.starts != 1 || readyCalls != 1 || registered != 1 {
		t.Fatalf("start output=%q starts=%d ready=%d register=%d", output.String(), manager.starts, readyCalls, registered)
	}
	output.Reset()
	if err := runLifecycleRuntimeAction(command, "stop"); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Runner stopped successfully.\n" || manager.stops != 1 {
		t.Fatalf("stop output=%q stops=%d", output.String(), manager.stops)
	}
	output.Reset()
	if err := runLifecycleStatus(command, nil); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Dashboard: stopped\nRunner: running\n" {
		t.Fatalf("status output = %q", output.String())
	}
}

func TestLifecycleCLIDirectRuntimeControlRefusesHeldDashboardLock(t *testing.T) {
	dir := t.TempDir()
	setLifecycleConfigDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("CREDIMI_RUNNER_ID=acme/runner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lease, err := controller.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	command, _ := lifecycleTestCommand()
	err = runLifecycleRuntimeAction(command, "stop")
	if err == nil || !strings.Contains(err.Error(), "refusing direct runtime control") {
		t.Fatalf("error = %v", err)
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
