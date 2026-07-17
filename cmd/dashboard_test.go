package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

type dashboardFakeManager struct {
	startCalls  int
	downCalls   int
	logs        []dashboardruntime.LogLine
	status      dashboardruntime.RuntimeStatus
	startErr    error
	progress    []string
	logDeadline time.Time
	logTail     int
}

func (f *dashboardFakeManager) Start(context.Context) error {
	f.startCalls++
	return f.startErr
}
func (f *dashboardFakeManager) StartWithProgress(ctx context.Context, progress func(string)) error {
	f.progress = append(f.progress, "Pulling Docker images.")
	progress("Pulling Docker images.")
	return f.Start(ctx)
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
func (f *dashboardFakeManager) Logs(ctx context.Context, tail int) ([]dashboardruntime.LogLine, error) {
	f.logTail = tail
	if deadline, ok := ctx.Deadline(); ok {
		f.logDeadline = deadline
	}
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
	if configFileExists(dir) {
		t.Fatal("configFileExists should be false before file creation")
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

func TestRootCommandDefaultsToDashboard(t *testing.T) {
	if rootCmd.RunE == nil {
		t.Fatal("root command should run the dashboard by default")
	}
	if !rootCmd.SilenceErrors || rootCmd.SilenceUsage {
		t.Fatal("root command must print errors once while retaining automatic usage output")
	}
	for _, name := range []string{"client", "dashboard"} {
		if cmd, _, err := rootCmd.Find([]string{name}); err == nil && cmd != rootCmd {
			t.Fatalf("%s command should not be registered", name)
		}
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
	if err := os.WriteFile(opener, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := openDashboardBrowser("http://127.0.0.1:8051"); err != nil {
		t.Fatal(err)
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
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"service":"credimi-runner","runner_id":"","boot_id":"test-boot"}`))
			return
		}
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"connected","devices":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer runner.Close()
	runnerHost, runnerPort, err := net.SplitHostPort(strings.TrimPrefix(runner.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

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
		"RUNNER_HOST":            runnerHost,
		"RUNNER_PORT":            runnerPort,
	}
	if err := startDashboardRuntime(context.Background(), manager, values); err != nil {
		t.Fatalf("startDashboardRuntime = %v", err)
	}
	if manager.startCalls != 1 {
		t.Fatalf("startCalls = %d", manager.startCalls)
	}
	if !registered {
		t.Fatal("startDashboardRuntime should register container runners after runner readiness")
	}
}

func TestStartDashboardRuntimeHostAutoRegistersFreshTunnelURL(t *testing.T) {
	oldTimeout := dashboardRegistrationTimeout
	dashboardRegistrationTimeout = 30 * time.Second
	t.Cleanup(func() {
		dashboardRegistrationTimeout = oldTimeout
	})

	var registeredBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mobile-runner" {
			body, _ := io.ReadAll(r.Body)
			registeredBody = string(body)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()

	manager := &dashboardFakeManager{
		status: dashboardruntime.RuntimeStatus{PublicURL: "https://stale-runner.trycloudflare.com"},
		logs:   []dashboardruntime.LogLine{{Message: "tunnel ready https://fresh-runner.trycloudflare.com"}},
	}
	values := dashboardruntime.Values{
		"CREDIMI_RUNNER_BACKEND":      "host",
		"CREDIMI_RUNNER_TYPE":         "ios_simulator",
		"CREDIMI_SERVICE_MODE":        "auto",
		"CREDIMI_RUNNER_ID":           "acme/runner",
		"CREDIMI_RUNNER_NAME":         "runner",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
		"CREDIMI_URL":                 api.URL,
		"CREDIMI_USER_API_KEY":        "secret",
		"RUNNER_HOST":                 "127.0.0.1",
		"RUNNER_PORT":                 "1",
	}
	if err := startDashboardRuntime(context.Background(), manager, values); err != nil {
		t.Fatalf("startDashboardRuntime = %v", err)
	}
	if !strings.Contains(registeredBody, "https://fresh-runner.trycloudflare.com") {
		t.Fatalf("registration body did not include fresh tunnel URL: %s", registeredBody)
	}
	if strings.Contains(registeredBody, "https://stale-runner.trycloudflare.com") {
		t.Fatalf("registration body reused stale tunnel URL: %s", registeredBody)
	}
	if manager.logDeadline.IsZero() || time.Until(manager.logDeadline) < 20*time.Second {
		t.Fatalf("registration log scan deadline = %v, want startup window near %s", manager.logDeadline, dashboardRegistrationTimeout)
	}
	if manager.logTail != quickTunnelLogTail {
		t.Fatalf("registration log tail = %d, want %d", manager.logTail, quickTunnelLogTail)
	}
}

func TestResolveDashboardRegistrationEndpointPrefersRuntimePublicURL(t *testing.T) {
	manager := &dashboardFakeManager{
		status: dashboardruntime.RuntimeStatus{PublicURL: "https://cached.trycloudflare.com"},
		logs:   []dashboardruntime.LogLine{{Message: "tunnel ready https://stale.trycloudflare.com"}},
	}
	publicURL, publicPort, err := resolveDashboardRegistrationEndpoint(context.Background(), manager, dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "auto",
	})
	if err != nil {
		t.Fatalf("resolveDashboardRegistrationEndpoint = %v", err)
	}
	if publicURL != "https://cached.trycloudflare.com" || publicPort != "" {
		t.Fatalf("endpoint = %q:%q", publicURL, publicPort)
	}
}

func TestStartDashboardRuntimeBranches(t *testing.T) {
	manager := &dashboardFakeManager{startErr: errors.New("boom")}
	if err := startDashboardRuntime(context.Background(), manager, dashboardruntime.Values{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("startDashboardRuntime start error = %v", err)
	}

	manager = &dashboardFakeManager{}
	if err := startDashboardRuntime(context.Background(), manager, dashboardruntime.Values{}); err != nil {
		t.Fatalf("startDashboardRuntime without runner id = %v", err)
	}
	if manager.startCalls != 1 {
		t.Fatalf("startCalls = %d", manager.startCalls)
	}
	if len(manager.progress) != 1 {
		t.Fatalf("startup progress = %v", manager.progress)
	}
}

func TestStartDashboardRuntimeWritesProgressToProvidedTerminalStream(t *testing.T) {
	manager := &dashboardFakeManager{}
	var terminal bytes.Buffer
	progress := func(line string) {
		_, _ = fmt.Fprintf(&terminal, "runner startup: %s\n", line)
	}

	if err := startDashboardRuntimeWithProgress(context.Background(), manager, dashboardruntime.Values{}, progress); err != nil {
		t.Fatalf("startDashboardRuntimeWithProgress = %v", err)
	}
	if got := terminal.String(); !strings.Contains(got, "runner startup: Pulling Docker images.") {
		t.Fatalf("terminal progress = %q", got)
	}
}

func TestDashboardRuntimeHelpers(t *testing.T) {
	manager := &dashboardFakeManager{logs: []dashboardruntime.LogLine{
		{Message: "line-1"},
		{Message: "line-2"},
	}, status: dashboardruntime.RuntimeStatus{LastError: "boom"}}
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", t.TempDir())
	values := dashboardruntime.Values{"CREDIMI_RUNNER_BACKEND": "container", "CREDIMI_RUNNER_TYPE": "android_emulator"}
	if got := runtimeStartupDiagnostics(context.Background(), manager, values); !strings.Contains(got, "last runtime error: boom") || !strings.Contains(got, "recent runtime logs") {
		t.Fatalf("runtimeStartupDiagnostics = %q", got)
	}
	values = dashboardruntime.Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_SERVICE_MODE": "manual"}
	if got := runtimeStartupDiagnostics(context.Background(), manager, values); !strings.Contains(got, "diagnostics:") {
		t.Fatalf("host runtimeStartupDiagnostics = %q", got)
	}
	if err := waitForDashboardRunnerReady(context.Background(), dashboardruntime.Values{"CREDIMI_RUNNER_BACKEND": "container", "CREDIMI_RUNNER_TYPE": "android_emulator"}); err != nil {
		t.Fatalf("waitForDashboardRunnerReady should skip when readiness not required: %v", err)
	}
}

func TestWaitForDashboardRunnerReady(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"connected","devices":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer runner.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(runner.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	values := dashboardruntime.Values{
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
		"CREDIMI_RUNNER_TYPE":    "ios_simulator",
		"RUNNER_HOST":            host,
		"RUNNER_PORT":            port,
	}
	if err := waitForDashboardRunnerReady(context.Background(), values); err != nil {
		t.Fatalf("waitForDashboardRunnerReady = %v", err)
	}
}

func TestResolveDashboardRegistrationEndpointBranches(t *testing.T) {
	manager := &dashboardFakeManager{logs: []dashboardruntime.LogLine{{Message: "https://runner.example.trycloudflare.com"}}}
	if url, port, err := resolveDashboardRegistrationEndpoint(context.Background(), manager, dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "manual",
		"RUNNER_PUBLIC_URL":    "https://manual.example",
		"RUNNER_PUBLIC_PORT":   "443",
	}); err != nil || url != "https://manual.example" || port != "443" {
		t.Fatalf("manual endpoint = %q %q %v", url, port, err)
	}
	if url, _, err := resolveDashboardRegistrationEndpoint(context.Background(), manager, dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "cloudflare-managed",
		"RUNNER_DOMAIN":        "runner.example",
	}); err != nil || url != "https://runner.example" {
		t.Fatalf("managed endpoint = %q %v", url, err)
	}
	if url, _, err := resolveDashboardRegistrationEndpoint(context.Background(), manager, dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "auto",
	}); err != nil || url != "https://runner.example.trycloudflare.com" {
		t.Fatalf("auto endpoint = %q %v", url, err)
	}
}

func TestRegisterDashboardRunnerRequiresAPIKey(t *testing.T) {
	err := registerDashboardRunner(context.Background(), &dashboardFakeManager{}, dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "manual",
		"RUNNER_PUBLIC_URL":    "https://manual.example",
	})
	if err == nil || !strings.Contains(err.Error(), "missing Credimi API key") {
		t.Fatalf("registerDashboardRunner error = %v", err)
	}
}

func TestResolveDashboardRegistrationEndpointErrors(t *testing.T) {
	manager := &dashboardFakeManager{}
	if _, _, err := resolveDashboardRegistrationEndpoint(context.Background(), manager, dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "manual",
	}); err == nil {
		t.Fatal("expected manual mode without public URL to fail")
	}
	if _, _, err := resolveDashboardRegistrationEndpoint(context.Background(), manager, dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "cloudflare-managed",
	}); err == nil {
		t.Fatal("expected managed mode without domain to fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := resolveDashboardRegistrationEndpoint(ctx, manager, dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "auto",
	}); err == nil {
		t.Fatal("expected auto mode without tunnel URL to fail")
	}
}
