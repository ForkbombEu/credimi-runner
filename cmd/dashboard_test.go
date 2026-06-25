package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/spf13/cobra"
)

type dashboardFakeManager struct {
	startCalls int
	downCalls  int
	logs       []dashboardruntime.LogLine
	status     dashboardruntime.RuntimeStatus
}

func (f *dashboardFakeManager) Start(context.Context) error {
	f.startCalls++
	return nil
}
func (f *dashboardFakeManager) Stop(context.Context) error        { return nil }
func (f *dashboardFakeManager) Restart(context.Context) error     { return nil }
func (f *dashboardFakeManager) Down(context.Context) error        { f.downCalls++; return nil }
func (f *dashboardFakeManager) UpdateImage(context.Context) error { return nil }
func (f *dashboardFakeManager) Configure(dashboardruntime.Values) {}
func (f *dashboardFakeManager) SetPublicURL(publicURL string) {
	f.status.PublicURL = publicURL
}
func (f *dashboardFakeManager) Status(context.Context) dashboardruntime.RuntimeStatus {
	return f.status
}
func (f *dashboardFakeManager) Logs(context.Context, int) ([]dashboardruntime.LogLine, error) {
	return f.logs, nil
}

func TestDashboardConfigPathHonorsOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", dir)
	if got := dashboard.ConfigDir(); got != dir {
		t.Fatalf("ConfigDir = %q", got)
	}
}

func TestDashboardLoadStoreMissingConfig(t *testing.T) {
	store, err := dashboardruntime.LoadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store.Exists() {
		t.Fatal("store should report missing config")
	}
}

func TestDashboardHandlerStartsWithoutRunnerIDOnFirstRun(t *testing.T) {
	handler, cancel, err := dashboard.NewHandler(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if body := rec.Body.String(); body == "" || !strings.Contains(body, "Set up Credimi Runner") {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDashboardConfigDirEnvPath(t *testing.T) {
	dir := t.TempDir()
	if got := dashboardEnvPath(dir); got != filepath.Join(dir, ".env") {
		t.Fatalf("dashboardEnvPath = %q", got)
	}
}

func TestDashboardConfiguredStoreExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("CREDIMI_RUNNER_ID=acme/runner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Exists() {
		t.Fatal("expected existing store")
	}
}

func TestResolveDashboardListenAddressUsesConfigValues(t *testing.T) {
	cmd := &cobra.Command{Use: "dashboard"}
	cmd.Flags().String("host", dashboardruntime.DefaultDashboardHost, "")
	cmd.Flags().Int("port", 8051, "")

	host, port := resolveDashboardListenAddress(cmd, dashboardruntime.Values{
		"DASHBOARD_HOST": "0.0.0.0",
		"DASHBOARD_PORT": "9001",
	})
	if host != "0.0.0.0" || port != 9001 {
		t.Fatalf("resolveDashboardListenAddress = %s:%d", host, port)
	}
}

func TestResolveDashboardListenAddressPrefersFlags(t *testing.T) {
	oldHost, oldPort := dashboardHost, dashboardPort
	t.Cleanup(func() {
		dashboardHost = oldHost
		dashboardPort = oldPort
	})
	dashboardHost = "127.0.0.2"
	dashboardPort = 9010

	cmd := &cobra.Command{Use: "dashboard"}
	cmd.Flags().String("host", dashboardruntime.DefaultDashboardHost, "")
	cmd.Flags().Int("port", 8051, "")
	if err := cmd.Flags().Set("host", dashboardHost); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("port", "9010"); err != nil {
		t.Fatal(err)
	}

	host, port := resolveDashboardListenAddress(cmd, dashboardruntime.Values{
		"DASHBOARD_HOST": "0.0.0.0",
		"DASHBOARD_PORT": "9001",
	})
	if host != dashboardHost || port != dashboardPort {
		t.Fatalf("resolveDashboardListenAddress flags = %s:%d", host, port)
	}
}

func TestValidateDashboardSecurity(t *testing.T) {
	if err := validateDashboardSecurity("127.0.0.1", dashboardruntime.Values{}); err != nil {
		t.Fatalf("localhost should be allowed: %v", err)
	}
	if err := validateDashboardSecurity("0.0.0.0", dashboardruntime.Values{}); err == nil {
		t.Fatal("remote bind without token should fail")
	}
	if err := validateDashboardSecurity("0.0.0.0", dashboardruntime.Values{"DASHBOARD_TOKEN": "secret"}); err != nil {
		t.Fatalf("remote bind with token should pass: %v", err)
	}
}

func TestStartDashboardRuntimeDoesNotFailWhenRunnerIsStillBooting(t *testing.T) {
	var registered bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mobile-runner" {
			registered = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()

	manager := &dashboardFakeManager{
		logs: []dashboardruntime.LogLine{{Message: "tunnel ready https://runner.example.trycloudflare.com"}},
	}
	values := dashboardruntime.Values{
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_RUNNER_TYPE":    "android_phone",
		"CREDIMI_SERVICE_MODE":   "auto",
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_NAME":    "runner",
		"CREDIMI_URL":            api.URL,
		"CREDIMI_USER_API_KEY":   "secret",
		"RUNNER_HOST":            "127.0.0.1",
		"RUNNER_PORT":            "1",
	}
	if err := startDashboardRuntime(context.Background(), manager, values); err != nil {
		t.Fatalf("startDashboardRuntime = %v", err)
	}
	if manager.startCalls != 1 {
		t.Fatalf("startCalls = %d", manager.startCalls)
	}
	if !registered {
		t.Fatal("startDashboardRuntime should register container runners without waiting for localhost readiness")
	}
}

func TestShutdownDashboardRuntimeRunsDownWhenConfigured(t *testing.T) {
	manager := &dashboardFakeManager{}
	if err := shutdownDashboardRuntime(context.Background(), manager, true); err != nil {
		t.Fatalf("shutdownDashboardRuntime = %v", err)
	}
	if manager.downCalls != 1 {
		t.Fatalf("downCalls = %d, want 1", manager.downCalls)
	}
}

func TestShutdownDashboardRuntimeRunsDownWhenComposeRunning(t *testing.T) {
	manager := &dashboardFakeManager{status: dashboardruntime.RuntimeStatus{ComposeRunning: true}}
	if err := shutdownDashboardRuntime(context.Background(), manager, false); err != nil {
		t.Fatalf("shutdownDashboardRuntime = %v", err)
	}
	if manager.downCalls != 1 {
		t.Fatalf("downCalls = %d, want 1", manager.downCalls)
	}
}

func TestShutdownDashboardRuntimeSkipsUnconfiguredStoppedRuntime(t *testing.T) {
	manager := &dashboardFakeManager{}
	if err := shutdownDashboardRuntime(context.Background(), manager, false); err != nil {
		t.Fatalf("shutdownDashboardRuntime = %v", err)
	}
	if manager.downCalls != 0 {
		t.Fatalf("downCalls = %d, want 0", manager.downCalls)
	}
}
