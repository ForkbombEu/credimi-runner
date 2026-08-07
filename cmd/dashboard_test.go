package cmd

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/spf13/cobra"
)

type dashboardTestListener struct{ closed chan struct{} }

func (l *dashboardTestListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *dashboardTestListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}
func (l *dashboardTestListener) Addr() net.Addr { return &net.TCPAddr{} }

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

func TestRunDashboardGracefullyStopsFromInjectedSignal(t *testing.T) {
	dir := t.TempDir()
	originalEnv, hadEnv := os.LookupEnv("CREDIMI_RUNNER_CONFIG_DIR")
	originalSource, originalReserve, originalOpen, originalConfigDir, originalHost, originalPort := dashboardSignalSource, dashboardListenerReservation, dashboardOpen, dashboardConfigDir, dashboardHost, dashboardPort
	dashboardSignalSource = func() (<-chan os.Signal, func()) {
		signals := make(chan os.Signal, 1)
		signals <- syscall.SIGTERM
		return signals, func() {}
	}
	dashboardOpen = false
	dashboardListenerReservation = func(string, int) (net.Listener, error) {
		return &dashboardTestListener{closed: make(chan struct{})}, nil
	}
	dashboardConfigDir = dir
	dashboardHost = "127.0.0.1"
	dashboardPort = 0
	t.Cleanup(func() {
		dashboardSignalSource = originalSource
		dashboardListenerReservation = originalReserve
		dashboardOpen = originalOpen
		dashboardConfigDir = originalConfigDir
		dashboardHost = originalHost
		dashboardPort = originalPort
		if hadEnv {
			_ = os.Setenv("CREDIMI_RUNNER_CONFIG_DIR", originalEnv)
		} else {
			_ = os.Unsetenv("CREDIMI_RUNNER_CONFIG_DIR")
		}
	})
	command := &cobra.Command{}
	command.Flags().String("host", "127.0.0.1", "")
	command.Flags().Int("port", 0, "")
	if err := command.Flags().Set("host", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("port", "0"); err != nil {
		t.Fatal(err)
	}
	if err := runDashboard(command, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardConfiguredStoreExists(t *testing.T) {
	dir := t.TempDir()
	writeTestTOMLConfig(t, dir)
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Exists() {
		t.Fatal("expected existing store")
	}
}

func TestRootCommandDefaultsToDashboard(t *testing.T) {
	if rootCmd.RunE == nil {
		t.Fatal("root command should run the dashboard by default")
	}
	if !rootCmd.SilenceErrors || rootCmd.SilenceUsage {
		t.Fatal("root command must print errors once while retaining automatic usage output")
	}
	if cmd, _, err := rootCmd.Find([]string{"client"}); err == nil && cmd != rootCmd {
		t.Fatal("client command should not be registered")
	}
	if cmd, _, err := rootCmd.Find([]string{"dashboard", "status"}); err != nil || cmd.Name() != "status" {
		t.Fatalf("dashboard status command should be registered, cmd=%v err=%v", cmd, err)
	}
	if cmd, _, err := rootCmd.Find([]string{"serve"}); err != nil || cmd == rootCmd || cmd.Name() != "serve" {
		t.Fatalf("serve command should remain registered, cmd=%v err=%v", cmd, err)
	} else if !cmd.Hidden {
		t.Fatal("serve command should be hidden from CLI help")
	}
}

func TestExecuteHelp(t *testing.T) {
	origArgs := os.Args
	origOut := rootCmd.OutOrStdout()
	origErr := rootCmd.ErrOrStderr()
	var output bytes.Buffer
	t.Cleanup(func() {
		os.Args = origArgs
		rootCmd.SetOut(origOut)
		rootCmd.SetErr(origErr)
	})

	os.Args = []string{"credimi-runner", "--help"}
	rootCmd.SetOut(&output)
	rootCmd.SetErr(&output)

	Execute()
	if !strings.Contains(output.String(), "Credimi mobile runner") {
		t.Fatalf("help output = %q", output.String())
	}
}

func TestRunDashboardReturnsStoreLoadError(t *testing.T) {
	restoreEnv(t, "CREDIMI_RUNNER_CONFIG_DIR")
	oldConfigDir, oldOpen := dashboardConfigDir, dashboardOpen
	t.Cleanup(func() {
		dashboardConfigDir = oldConfigDir
		dashboardOpen = oldOpen
	})

	configFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(configFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	dashboardConfigDir = configFile
	dashboardOpen = false

	err := runDashboard(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("expected LoadStore error")
	}
}

func TestRunDashboardReturnsListenError(t *testing.T) {
	restoreEnv(t, "CREDIMI_RUNNER_CONFIG_DIR")
	oldConfigDir, oldOpen, oldPort := dashboardConfigDir, dashboardOpen, dashboardPort
	t.Cleanup(func() {
		dashboardConfigDir = oldConfigDir
		dashboardOpen = oldOpen
		dashboardPort = oldPort
	})

	dashboardConfigDir = t.TempDir()
	dashboardOpen = false
	dashboardPort = -1
	cmd := &cobra.Command{}
	cmd.Flags().Int("port", 8051, "")
	if err := cmd.Flags().Set("port", "-1"); err != nil {
		t.Fatal(err)
	}

	err := runDashboard(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Fatalf("runDashboard listen error = %v", err)
	}
}

func TestRunDashboardServesUntilTerminationSignal(t *testing.T) {
	restoreEnv(t, "CREDIMI_RUNNER_CONFIG_DIR")
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	oldConfigDir, oldOpen, oldHost, oldPort := dashboardConfigDir, dashboardOpen, dashboardHost, dashboardPort
	t.Cleanup(func() {
		dashboardConfigDir = oldConfigDir
		dashboardOpen = oldOpen
		dashboardHost = oldHost
		dashboardPort = oldPort
	})
	dashboardConfigDir = t.TempDir()
	dashboardOpen = false
	dashboardHost = "127.0.0.1"
	dashboardPort = port

	cmd := &cobra.Command{}
	cmd.Flags().String("host", dashboardruntime.DefaultDashboardHost, "")
	cmd.Flags().Int("port", 8051, "")
	if err := cmd.Flags().Set("host", dashboardHost); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("port", strconv.Itoa(port)); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()

	if err := runDashboard(cmd, nil); err != nil {
		t.Fatalf("runDashboard = %v", err)
	}
}

func TestReserveDashboardListenerExplainsOccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	_, err = reserveDashboardListener("127.0.0.1", port)
	if err == nil {
		t.Fatal("expected occupied dashboard port to fail")
	}
	message := err.Error()
	for _, want := range []string{strconv.Itoa(port), "lsof", "ss -ltnp", "kill <PID>", "--port"} {
		if !strings.Contains(message, want) {
			t.Fatalf("occupied port error %q does not contain %q", message, want)
		}
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

func TestDashboardBrowserHelpers(t *testing.T) {
	if got := dashboardBrowserURL("0.0.0.0", 8051); got != "http://127.0.0.1:8051" {
		t.Fatalf("dashboardBrowserURL(0.0.0.0) = %q", got)
	}
	if got := dashboardBrowserURL("localhost", 8051); got != "http://localhost:8051" {
		t.Fatalf("dashboardBrowserURL(localhost) = %q", got)
	}
	if err := openDashboardBrowser(""); err == nil {
		t.Fatal("expected empty dashboard URL to fail")
	}
}

func TestOpenDashboardBrowserStartsPlatformCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test supplies the Linux xdg-open command")
	}
	dir := t.TempDir()
	opener := filepath.Join(dir, "xdg-open")
	done := filepath.Join(dir, "opened")
	if err := os.WriteFile(opener, []byte("#!/bin/sh\n: > '"+done+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := openDashboardBrowser("http://127.0.0.1:8051"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(done); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("xdg-open did not start")
}
