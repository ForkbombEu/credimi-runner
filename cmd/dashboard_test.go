package cmd

import (
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
