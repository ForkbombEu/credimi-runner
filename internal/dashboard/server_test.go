package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/controller"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/maintenance"
	"github.com/forkbombeu/credimi-runner/internal/runtimesupervisor"
	"github.com/forkbombeu/credimi-runner/internal/servicecoordination"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func writeCoordinatorPresenceFixture(dir string, updatedAt time.Time) error {
	raw, err := json.Marshal(servicecoordination.Presence{PID: os.Getpid(), Protocol: servicecoordination.Protocol, UpdatedAt: updatedAt.UTC()})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, servicecoordination.CoordinatorFile), raw, 0o600)
}

type fakeRuntimeController struct {
	mu            sync.Mutex
	status        runtimesupervisor.Status
	starts        int
	stops         int
	restarts      int
	reconciles    int
	inventories   int
	endpoints     int
	requestStarts int
	reconcileErr  error
}

func persistentServerSettingsFromTestValues(values map[string]string) appliedServerSettings {
	settings, err := persistentServerSettingsFromValues(dashboardruntime.Values(values))
	if err != nil {
		panic(err)
	}
	return settings
}

func (f *fakeRuntimeController) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return nil
}

func (f *fakeRuntimeController) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return nil
}

func (f *fakeRuntimeController) Restart(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
	return nil
}

func (f *fakeRuntimeController) RequestStart() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requestStarts++
	f.status.Desired = runtimesupervisor.DesiredRunning
	return nil
}

func (f *fakeRuntimeController) Reconcile(context.Context, runnerconfig.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconciles++
	return f.reconcileErr
}

func (f *fakeRuntimeController) ApplyInventory(context.Context, runnerconfig.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inventories++
	return f.reconcileErr
}

func (f *fakeRuntimeController) ApplyEndpoint(context.Context, runnerconfig.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endpoints++
	return f.reconcileErr
}

func (f *fakeRuntimeController) Status() runtimesupervisor.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeRuntimeController) counts() (int, int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops, f.restarts, f.reconciles
}

func (f *fakeRuntimeController) requestedStarts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requestStarts
}

func (f *fakeRuntimeController) inventoryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inventories
}

func (f *fakeRuntimeController) endpointCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.endpoints
}

func TestExposureApplyClassificationUsesActiveMode(t *testing.T) {
	manual := runnerconfig.Config{Exposure: runnerconfig.ExposureConfig{Mode: "manual"}}
	quick := runnerconfig.Config{Exposure: runnerconfig.ExposureConfig{Mode: "quick_tunnel"}}
	named := runnerconfig.Config{Exposure: runnerconfig.ExposureConfig{Mode: "named_tunnel"}}
	endpoint := dashboardruntime.ConfigDiff{ChangedKeys: []string{"RUNNER_PUBLIC_URL"}}
	if !activeManualEndpointDiff(endpoint, manual) || inventoryOnlyDiff(endpoint, manual) {
		t.Fatal("manual endpoint change was not classified as an endpoint apply")
	}
	if !inactiveExposureOnlyDiff(endpoint, quick) || !inactiveExposureOnlyDiff(endpoint, named) {
		t.Fatal("inactive manual URL change was not ignored")
	}
	domain := dashboardruntime.ConfigDiff{ChangedKeys: []string{"RUNNER_DOMAIN"}}
	if !inactiveExposureOnlyDiff(domain, manual) || !inactiveExposureOnlyDiff(domain, quick) {
		t.Fatal("inactive named-domain change was not ignored")
	}
	if !inventoryOnlyDiff(dashboardruntime.ConfigDiff{ChangedKeys: []string{"CREDIMI_RUNNER_DESCRIPTION"}}, manual) {
		t.Fatal("description change was not classified as inventory-only")
	}
}

func TestApplySavedConfigUsesWholeDiffForEndpointHotApply(t *testing.T) {
	loaded, path := testSavedConfig(t)
	values := dashboardruntime.Values(loaded.Snapshot())
	values["CREDIMI_SERVICE_MODE"] = "manual"
	values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	loaded = persistDashboardValues(t, path, values)
	old := dashboardruntime.Values(loaded.Snapshot())
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(func() runnerconfig.Config {
		cfg, err := runnerconfig.LoadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}(), true))
	t.Setenv(servicemanager.ServiceNetworkModeEnv, "bridge")
	t.Setenv(servicemanager.AppliedServiceNeedsHostADBEnv, "false")
	t.Setenv(servicemanager.AppliedServiceNeedsUSBEnv, "false")
	t.Setenv(servicemanager.AppliedServiceNeedsEmulatorEnv, "false")
	t.Setenv(servicemanager.AppliedServiceRedroidKnownHostsEnv, "[]")
	for _, tc := range []struct {
		name                        string
		mutate                      func(dashboardruntime.Values)
		wantEndpoint, wantReconcile int
	}{
		{name: "url", mutate: func(v dashboardruntime.Values) { v["RUNNER_PUBLIC_URL"] = "https://new.example" }, wantEndpoint: 1},
		{name: "url plus temporal", mutate: func(v dashboardruntime.Values) {
			v["RUNNER_PUBLIC_URL"] = "https://new.example"
			v["TEMPORAL_ADDRESS"] = "temporal-new:7233"
		}, wantReconcile: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
			s := &Server{cfg: loaded, runtime: fake, composeDir: filepath.Dir(path)}
			candidate := dashboardruntime.Values(cloneStringMap(map[string]string(old)))
			tc.mutate(candidate)
			s.cfg = persistDashboardValues(t, path, candidate)
			if err := s.applySavedConfig(context.Background(), dashboardruntime.DiffValuesForOS(old, candidate, "linux")); err != nil {
				t.Fatal(err)
			}
			if fake.endpointCount() != tc.wantEndpoint {
				t.Fatalf("endpoint calls=%d want %d", fake.endpointCount(), tc.wantEndpoint)
			}
			if _, _, _, got := fake.counts(); got != tc.wantReconcile {
				t.Fatalf("reconcile calls=%d want %d", got, tc.wantReconcile)
			}
		})
	}
}

func TestNamedTunnelDomainChangeUsesRuntimeReconcile(t *testing.T) {
	loaded, path := testSavedConfig(t)
	values := dashboardruntime.Values(loaded.Snapshot())
	values["CREDIMI_SERVICE_MODE"] = "cloudflare-managed"
	values["RUNNER_DOMAIN"] = "old.example"
	values["CLOUDFLARE_TUNNEL_TOKEN"] = "test-token"
	loaded = persistDashboardValues(t, path, values)
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(func() runnerconfig.Config {
		cfg, err := runnerconfig.LoadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}(), true))
	t.Setenv(servicemanager.ServiceNetworkModeEnv, "bridge")
	t.Setenv(servicemanager.AppliedServiceNeedsHostADBEnv, "false")
	t.Setenv(servicemanager.AppliedServiceNeedsUSBEnv, "false")
	t.Setenv(servicemanager.AppliedServiceNeedsEmulatorEnv, "false")
	t.Setenv(servicemanager.AppliedServiceRedroidKnownHostsEnv, "[]")
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := &Server{cfg: loaded, runtime: fake, composeDir: filepath.Dir(path)}
	candidate := dashboardruntime.Values(cloneStringMap(map[string]string(values)))
	candidate["RUNNER_DOMAIN"] = "new.example"
	s.cfg = persistDashboardValues(t, path, candidate)
	if err := s.applySavedConfig(context.Background(), dashboardruntime.DiffValuesForOS(values, candidate, "linux")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, reconciles := fake.counts(); reconciles != 1 || fake.endpointCount() != 0 || fake.inventoryCount() != 0 {
		t.Fatalf("named tunnel apply calls: reconcile=%d endpoint=%d inventory=%d", reconciles, fake.endpointCount(), fake.inventoryCount())
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	if os.Getenv("ANDROID_SDK_ROOT") == "" {
		original := ensureCandidateEmulatorReady
		ensureCandidateEmulatorReady = func(context.Context, runnerconfig.Config, string, androidtools.EmulatorProgress) error { return nil }
		t.Cleanup(func() { ensureCandidateEmulatorReady = original })
	}
	cfg := &Config{path: filepath.Join(t.TempDir(), "config.toml"), values: map[string]string{}}
	for key, value := range Defaults {
		cfg.values[key] = value
	}
	cfg.values["CREDIMI_URL"] = "https://credimi.example"
	cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	cfg.values["TEMPORAL_ADDRESS"] = "temporal.example:7233"
	cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	render, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeRuntimeController{}
	hub := NewHub(cfg, render, func() dashboardruntime.RuntimeStatus { return dashboardRuntimeStatus(cfg, fake.Status()) })
	hub.snap = Snapshot{Services: []Service{{ID: "runner", Name: "runner", Status: Online}}, Devices: []Device{{Serial: "device-1", Name: "Pixel 8", Type: "android_phone", Mode: "usb", Status: Online}}}
	hub.workers = []Worker{{ID: "runner-worker", Env: "runner", Status: Online}}
	return &Server{
		cfg: cfg, hub: hub, render: render, composeDir: t.TempDir(), ctx: context.Background(),
		runtime: fake, operations: controller.NewCoordinator(context.Background()),
		appliedServerSettings: persistentServerSettingsFromTestValues(cfg.Snapshot()),
		lookupPath:            func(string) (string, error) { return "/tmp/fake-bin", nil },
	}
}

func TestDarwinAppliedServerTimeoutReversionClearsRestart(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	s := newTestServer(t)
	base := s.cfg.Snapshot()
	for _, tc := range []struct {
		name, key, changed string
	}{
		{"read header", "SERVER_READ_HEADER_TIMEOUT", "2m0s"},
		{"shutdown", "SERVER_SHUTDOWN_TIMEOUT", "45s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := cloneStringMap(base)
			candidate[tc.key] = tc.changed
			s.cfg.mu.Lock()
			s.cfg.values = candidate
			s.cfg.mu.Unlock()
			if !s.serviceRestartRequired() {
				t.Fatalf("changed %s did not require restart", tc.key)
			}
			s.cfg.mu.Lock()
			s.cfg.values = cloneStringMap(base)
			s.cfg.mu.Unlock()
			if s.serviceRestartRequired() {
				t.Fatalf("reverted %s still requires restart", tc.key)
			}
		})
	}
}

func TestDashboardListenCanonicalizesWildcardHosts(t *testing.T) {
	for _, tc := range []struct {
		name, host, want string
	}{
		{"empty host", "", "127.0.0.1:8051"},
		{"IPv4 wildcard", "0.0.0.0", "127.0.0.1:8051"},
		{"IPv6 wildcard", "::", "127.0.0.1:8051"},
		{"IPv6 loopback", "::1", "[::1]:8051"},
		{"alternate IPv4", "127.0.0.2", "127.0.0.2:8051"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dashboardListen(dashboardruntime.Values{"DASHBOARD_HOST": tc.host, "DASHBOARD_PORT": "8051"})
			if got != tc.want {
				t.Fatalf("dashboardListen(%q)=%q; want %q", tc.host, got, tc.want)
			}
		})
	}
}

func TestDarwinIPv6WildcardListenerEquivalence(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	for _, tc := range []struct {
		name, applied, desired string
		wantRestart            bool
	}{
		{"IPv6 wildcard to IPv4 loopback", "[::]:8051", "127.0.0.1:8051", false},
		{"IPv4 loopback to IPv6 wildcard", "127.0.0.1:8051", "[::]:8051", false},
		{"IPv4 wildcard to IPv6 wildcard", "0.0.0.0:8051", "[::]:8051", false},
		{"IPv6 wildcard to IPv4 wildcard", "[::]:8051", "0.0.0.0:8051", false},
		{"IPv6 loopback remains distinct", "[::1]:8051", "127.0.0.1:8051", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			appliedCfg := runnerconfig.Bootstrap()
			appliedCfg.Server.DashboardListen = tc.applied
			appliedCfg.Server.ReadHeaderTimeout = runnerconfig.Duration(time.Minute)
			appliedCfg.Server.ShutdownTimeout = runnerconfig.Duration(30 * time.Second)
			s.appliedServerSettings = persistentServerSettings(appliedCfg)
			host, port, err := net.SplitHostPort(tc.desired)
			if err != nil {
				t.Fatal(err)
			}
			s.cfg.mu.Lock()
			s.cfg.values["DASHBOARD_HOST"] = host
			s.cfg.values["DASHBOARD_PORT"] = port
			s.cfg.mu.Unlock()
			if got := s.serviceRestartRequired(); got != tc.wantRestart {
				t.Fatalf("restart required=%v; want %v", got, tc.wantRestart)
			}
		})
	}
}

func writeDashboardTestConfig(t *testing.T, dir, dashboardToken string) {
	t.Helper()
	cfg := runnerconfig.Config{
		SchemaVersion: runnerconfig.SchemaVersion,
		Runner:        runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"},
		Credimi:       runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "test"},
		Temporal:      runnerconfig.TemporalConfig{Address: "temporal.example:7233"},
		Server:        runnerconfig.ServerConfig{DashboardToken: dashboardToken},
		Exposure:      runnerconfig.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example"},
	}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
}

func waitForRuntimeCount(t *testing.T, runtime *fakeRuntimeController, want func(int, int, int, int) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		start, stop, restart, reconcile := runtime.counts()
		if want(start, stop, restart, reconcile) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	start, stop, restart, reconcile := runtime.counts()
	t.Fatalf("runtime calls did not reach expected state: %d/%d/%d/%d", start, stop, restart, reconcile)
}

func waitForQueuedOperation(t *testing.T, s *Server, response *httptest.ResponseRecorder) controller.Snapshot {
	t.Helper()
	if response.Code != http.StatusAccepted {
		t.Fatalf("queued operation response = %d body=%s", response.Code, response.Body.String())
	}
	snapshot := s.operations.Current()
	if snapshot.ID == "" {
		t.Fatal("queued operation has no ID")
	}
	completed, err := s.operations.Wait(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Phase != controller.PhaseSucceeded {
		t.Fatalf("queued operation failed: %#v", completed)
	}
	return completed
}

func TestDashboardRuntimeControllerRoutesUseSharedController(t *testing.T) {
	fake := &fakeRuntimeController{}
	handler, cancel, err := NewHandler(context.Background(), t.TempDir(), "controller", "identity", "fingerprint", fake, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	actions := []struct {
		name string
		want func(int, int, int, int) bool
	}{
		{"start", func(start, _, _, _ int) bool { return start == 1 }},
		{"stop", func(_, stop, _, _ int) bool { return stop == 1 }},
		{"restart", func(_, _, restart, _ int) bool { return restart == 1 }},
	}
	for _, action := range actions {
		request := httptest.NewRequest(http.MethodPost, "/api/controller/runtime/"+action.name, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("%s status = %d, body=%s", action.name, recorder.Code, recorder.Body)
		}
		waitForRuntimeCount(t, fake, action.want)
	}
}

func TestDashboardRuntimeActionHandlersAndControllerStatus(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct {
		handler http.HandlerFunc
		want    func(int, int, int, int) bool
	}{
		{s.runtimeStart, func(start, _, _, _ int) bool { return start == 1 }},
		{s.runtimeStop, func(_, stop, _, _ int) bool { return stop == 1 }},
		{s.runtimeRestart, func(_, _, restart, _ int) bool { return restart == 1 }},
	} {
		recorder := httptest.NewRecorder()
		tc.handler(recorder, httptest.NewRequest(http.MethodPost, "/runtime", nil))
		waitForRuntimeCount(t, s.runtime.(*fakeRuntimeController), tc.want)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("runtime action status = %d", recorder.Code)
		}
	}
	for _, endpoint := range []string{"/api/controller/status", "/api/controller/operations/current"} {
		recorder := httptest.NewRecorder()
		s.routes(http.NewServeMux())
		if endpoint == "/api/controller/status" {
			s.controllerStatus(recorder, httptest.NewRequest(http.MethodGet, endpoint, nil))
		} else if endpoint == "/api/controller/operations/current" {
			s.controllerOperationCurrent(recorder, httptest.NewRequest(http.MethodGet, endpoint, nil))
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d", endpoint, recorder.Code)
		}
	}
	operationID := s.operations.Current().ID
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/controller/operations/"+operationID, nil)
	request.SetPathValue("id", operationID)
	s.controllerOperation(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("controller operation status = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/controller/operations/missing", nil)
	request.SetPathValue("id", "missing")
	s.controllerOperation(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing controller operation status = %d", recorder.Code)
	}
	if got := runtimeActionSuccessMessage("unknown"); got == "" {
		t.Fatal("missing default runtime action message")
	}
	if got := describeDiffImpact(dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRuntimeReconcile}}); !strings.Contains(got, "reconciled") {
		t.Fatalf("diff description = %q", got)
	}
}

func TestDashboardRemainingControllerAndMutationHandlers(t *testing.T) {
	s := newTestServer(t)
	for _, request := range []struct {
		path string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"/internal/controller/identity", s.controllerIdentity},
		{"/config/diff", s.configDiff},
		{"/devices/enable", s.deviceEnable},
	} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, request.path, nil)
		request.call(recorder, req)
		if request.path == "/config/diff" && recorder.Code != http.StatusOK {
			t.Fatalf("config diff status = %d", recorder.Code)
		}
	}
	s.controllerIdentityToken = "identity"
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/controller/identity", nil)
	req.Header.Set("X-Credimi-Controller-Token", "identity")
	s.controllerIdentity(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("identity status = %d", recorder.Code)
	}
	if got := dashboardRefreshPath("devices"); got != "/devices" || dashboardRefreshPath("setup") != "/setup" || dashboardRefreshPath("other") != "/" {
		t.Fatalf("refresh paths = %q", got)
	}
	recorder = httptest.NewRecorder()
	s.renderRuntimeActionError(recorder, "overview", errors.New("runtime failed"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("runtime error status = %d", recorder.Code)
	}
}

func TestDashboardConfigAndViewHelperBranches(t *testing.T) {
	s := newTestServer(t)
	if !(PageData{Snapshot: Snapshot{Services: []Service{{Expected: true, Critical: true, Status: Online}}}}.HasCriticalServices()) {
		t.Fatal("critical service was not detected")
	}
	if (PageData{Snapshot: Snapshot{Services: []Service{{Expected: true, Critical: true, Status: Offline}}}}.ServicesAllUp()) {
		t.Fatal("offline critical service reported healthy")
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/overview/config", strings.NewReader(url.Values{"CREDIMI_RUNNER_PUBLISHED": {"on"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveOverviewConfig(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("overview config status = %d %s", recorder.Code, recorder.Body.String())
	}
	waitForQueuedOperation(t, s, recorder)
}

func TestDashboardRemainsReachableWhenRuntimeStops(t *testing.T) {
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredStopped, Actual: runtimesupervisor.ActualStopped}}
	handler, cancel, err := NewHandler(context.Background(), t.TempDir(), "controller", "", "", fake, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("dashboard health = %d %q", recorder.Code, recorder.Body.String())
	}
	if start, stop, restart, _ := fake.counts(); start != 0 || stop != 0 || restart != 0 {
		t.Fatalf("health request changed runtime counts: %d/%d/%d", start, stop, restart)
	}
}

func TestDashboardRecoveryCORSAllowsSameHostPortChangeWithoutBypassingAuth(t *testing.T) {
	s := newTestServer(t)
	s.cfg.mu.Lock()
	s.cfg.values["DASHBOARD_TOKEN"] = "replacement-token"
	s.cfg.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /startup/status", s.startupStatus)
	handler := recoveryCORS(s.auth(mux))

	req := httptest.NewRequest(http.MethodGet, "/startup/status?token=replacement-token", nil)
	req.Host = "runner.example:8052"
	req.Header.Set("Origin", "http://runner.example:8051")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://runner.example:8051" {
		t.Fatalf("allow origin=%q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/startup/status", nil)
	req.Host = "runner.example:8052"
	req.Header.Set("Origin", "http://runner.example:8051")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated recovery status=%d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://runner.example:8051" {
		t.Fatalf("unauthenticated allow origin=%q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/startup/status?token=replacement-token", nil)
	req.Host = "runner.example:8052"
	req.Header.Set("Origin", "http://other.example:8051")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign allow origin=%q", got)
	}
}

func TestDashboardMapsSupervisorStatus(t *testing.T) {
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner.ID = "org/runner"
	cfg.Runner.Name = "runner"
	cfg.Runner.Organization = "org"
	cfg.Credimi.URL = "https://credimi.example"
	cfg.Credimi.UserAPIKey = "key"
	cfg.Temporal.Address = "temporal.example:7233"
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{
		Desired: runtimesupervisor.DesiredRunning, Actual: runtimesupervisor.ActualRunning,
		PublicURL: "https://runner.example", APIListening: true, WorkersRunning: true,
		EdgeRunning: true, HeartbeatRunning: true,
	}}
	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	status := dashboardRuntimeStatus(loaded, fake.Status())
	if !status.Configured || !status.RunnerRunning || !status.APIListening || !status.WorkersRunning || !status.EdgeRunning || !status.HeartbeatRunning {
		t.Fatalf("status mapping lost supervisor state: %+v", status)
	}
	if status.PublicURL != "https://runner.example" {
		t.Fatalf("public URL = %q", status.PublicURL)
	}
}

func testSavedConfig(t *testing.T) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner.ID = "org/runner"
	cfg.Runner.Name = "runner"
	cfg.Runner.Organization = "org"
	cfg.Credimi.URL = "https://credimi.example"
	cfg.Credimi.UserAPIKey = "key"
	cfg.Temporal.Address = "temporal.example:7233"
	path := filepath.Join(dir, "config.toml")
	if err := runnerconfig.WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded, path
}

func TestDashboardTokenOnlySaveDoesNotReconcileRuntime(t *testing.T) {
	loaded, _ := testSavedConfig(t)
	fake := &fakeRuntimeController{}
	s := &Server{cfg: loaded, runtime: fake, composeDir: filepath.Dir(loaded.Path())}
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	newValues := dashboardruntime.Values(loaded.Snapshot())
	newValues["DASHBOARD_TOKEN"] = "new-token"
	diff := dashboardruntime.DiffValues(oldValues, newValues)
	if len(diff.Classes) != 1 || diff.Classes[0] != dashboardruntime.ApplySavedOnly {
		t.Fatalf("token diff = %+v", diff)
	}
	err := s.applySavedConfig(context.Background(), diff)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, reconciles := fake.counts(); reconciles != 0 {
		t.Fatalf("reconcile calls = %d", reconciles)
	}
}

func TestDashboardRuntimeConfigChangeReconcilesSupervisor(t *testing.T) {
	loaded, _ := testSavedConfig(t)
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := &Server{cfg: loaded, runtime: fake, composeDir: filepath.Dir(loaded.Path())}
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	newValues := dashboardruntime.Values(loaded.Snapshot())
	newValues["CREDIMI_RUNNER_DESCRIPTION"] = "updated"
	diff := dashboardruntime.DiffValues(oldValues, newValues)
	if !diffNeedsRuntimeApply(diff) {
		t.Fatalf("description diff did not require runtime reconcile: %+v", diff)
	}
	if err := s.applySavedConfig(context.Background(), diff); err != nil {
		t.Fatal(err)
	}
	if _, _, _, reconciles := fake.counts(); reconciles != 0 || fake.inventoryCount() != 1 {
		t.Fatalf("runtime apply calls: reconcile=%d inventory=%d", reconciles, fake.inventoryCount())
	}
}

func TestServiceTopologyChangeDoesNotCallRuntime(t *testing.T) {
	oldValues := dashboardruntime.Values{"ANDROID_RUNNER_IMAGE": "old"}
	newValues := dashboardruntime.Values{"ANDROID_RUNNER_IMAGE": "new"}
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, "linux")
	if len(diff.Classes) != 1 || diff.Classes[0] != dashboardruntime.ApplyServiceRestartRequired {
		t.Fatalf("service diff = %+v", diff)
	}
}

func TestServiceTopologyChangeRequestsAttachedHostRestart(t *testing.T) {
	loaded, path := testSavedConfig(t)
	oldTyped, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(oldTyped, true))
	newValues := dashboardruntime.Values(cloneStringMap(loaded.Snapshot()))
	newValues["ANDROID_RUNNER_IMAGE"] = "credimi-runner:replacement"
	s := newTestServer(t)
	s.cfg = persistDashboardValues(t, path, newValues)
	s.composeDir = filepath.Dir(path)
	diff := dashboardruntime.DiffValuesForOS(dashboardruntime.Values(loaded.Snapshot()), newValues, "linux")
	if err := s.applySavedConfig(context.Background(), diff); err != nil {
		t.Fatal(err)
	}
	request, err := servicecoordination.ReadRestartRequest(s.composeDir)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := runnerconfig.ConfigFileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	if request.RequestedConfigDigest != digest {
		t.Fatalf("request config digest=%q, want %q", request.RequestedConfigDigest, digest)
	}
	if got := s.startupSnapshot(); got.Phase != StartupNeedsAttention || !strings.Contains(got.Message, "credimi-runner service restart") {
		t.Fatalf("detached startup state=%+v", got)
	}
	if got := s.runtime.(*fakeRuntimeController).requestedStarts(); got != 0 {
		t.Fatalf("runtime start requests=%d, want 0 for an ordinary config edit", got)
	}
}

func TestServiceRestartRequestForUnknownHostnameForcesHostEvaluation(t *testing.T) {
	loaded, path := testSavedConfig(t)
	values := dashboardruntime.Values(cloneStringMap(loaded.Snapshot()))
	values["CREDIMI_URL"] = "http://new-host.example:8090"
	s := newTestServer(t)
	s.cfg = persistDashboardValues(t, path, values)
	s.composeDir = filepath.Dir(path)
	resolved, _ := json.Marshal(map[string]string{"credimi.example": ""})
	t.Setenv(servicemanager.AppliedServiceResolvedHostsEnv, string(resolved))
	if err := s.requestServiceRestart(); err != nil {
		t.Fatal(err)
	}
	request, err := servicecoordination.ReadRestartRequest(s.composeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !request.ForceRestart || request.RequestedConfigDigest == "" {
		t.Fatalf("request = %+v", request)
	}
	want, err := runnerconfig.ConfigFileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	if request.RequestedConfigDigest != want {
		t.Fatalf("request digest = %q, want %q", request.RequestedConfigDigest, want)
	}
}

func TestServiceRestartRequestBindsDigestAndForceRestartToOneSnapshot(t *testing.T) {
	loaded, path := testSavedConfig(t)
	first, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first.Credimi.URL = "http://new-host.example:8090"
	if err := runnerconfig.WriteFile(path, first); err != nil {
		t.Fatal(err)
	}
	_, firstDigest, err := runnerconfig.LoadFileSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.Credimi.URL = "https://credimi.example"
	previousLoader := loadDashboardConfigSnapshot
	loadDashboardConfigSnapshot = func(snapshotPath string) (runnerconfig.Config, string, error) {
		cfg, digest, snapshotErr := runnerconfig.LoadFileSnapshot(snapshotPath)
		if snapshotErr == nil {
			snapshotErr = runnerconfig.WriteFile(snapshotPath, second)
		}
		return cfg, digest, snapshotErr
	}
	t.Cleanup(func() { loadDashboardConfigSnapshot = previousLoader })
	resolved, _ := json.Marshal(map[string]string{"credimi.example": ""})
	t.Setenv(servicemanager.AppliedServiceResolvedHostsEnv, string(resolved))
	s := newTestServer(t)
	s.cfg = loaded
	s.composeDir = filepath.Dir(path)
	if err := s.requestServiceRestart(); err != nil {
		t.Fatal(err)
	}
	request, err := servicecoordination.ReadRestartRequest(s.composeDir)
	if err != nil {
		t.Fatal(err)
	}
	if request.RequestedConfigDigest != firstDigest || !request.ForceRestart {
		t.Fatalf("request=%+v, want digest=%q force=true", request, firstDigest)
	}
}

func TestServiceTopologyChangeWaitsForAttachedHost(t *testing.T) {
	loaded, path := testSavedConfig(t)
	oldTyped, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(oldTyped, true))
	newValues := dashboardruntime.Values(cloneStringMap(loaded.Snapshot()))
	newValues["ANDROID_RUNNER_IMAGE"] = "credimi-runner:replacement"
	s := newTestServer(t)
	s.cfg = persistDashboardValues(t, path, newValues)
	s.composeDir = filepath.Dir(path)
	if err := writeCoordinatorPresenceFixture(s.composeDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	diff := dashboardruntime.DiffValuesForOS(dashboardruntime.Values(loaded.Snapshot()), newValues, "linux")
	if err := s.applySavedConfig(context.Background(), diff); err != nil {
		t.Fatal(err)
	}
	got := s.startupSnapshot()
	if got.Phase != StartupStarting || !strings.Contains(got.Message, "attached") {
		t.Fatalf("attached startup state=%+v", got)
	}
}

func TestRecoveryOriginUsesDesiredDashboardPortAndBrowserHost(t *testing.T) {
	s := newTestServer(t)
	s.cfg.mu.Lock()
	s.cfg.values["DASHBOARD_PORT"] = "8052"
	s.cfg.mu.Unlock()
	for _, tc := range []struct {
		name, requestHost, want string
	}{
		{"hostname", "runner.example:8051", "http://runner.example:8052"},
		{"forwarded scheme", "runner.example", "https://runner.example:8052"},
		{"wildcard", "0.0.0.0:8051", "http://127.0.0.1:8052"},
		{"ipv6", "[::1]:8051", "http://[::1]:8052"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/config", nil)
			req.Host = tc.requestHost
			if tc.name == "forwarded scheme" {
				req.Header.Set("X-Forwarded-Proto", "https, http")
			}
			if got := s.recoveryOrigin(req); got != tc.want {
				t.Fatalf("recovery origin=%q want=%q", got, tc.want)
			}
		})
	}
	req := httptest.NewRequest(http.MethodPost, "/config", nil)
	req.Host = "runner.example:8051"
	s.cfg.mu.Lock()
	s.cfg.values["DASHBOARD_PORT"] = "8051"
	s.cfg.mu.Unlock()
	req.PostForm = url.Values{"DASHBOARD_PORT": {"8052"}}
	response := httptest.NewRecorder()
	s.writeQueuedRuntimeAction(response, req, controller.Snapshot{ID: "operation"}, "saved", "/", true)
	var trigger map[string]map[string]string
	if err := json.Unmarshal([]byte(response.Header().Get("HX-Trigger")), &trigger); err != nil {
		t.Fatal(err)
	}
	if got := trigger["runtimeOperation"]["recoveryOrigin"]; got != "http://runner.example:8052" {
		t.Fatalf("recovery trigger origin=%q", got)
	}
}

func TestStartupStatusFallsBackWhenAttachedCoordinatorGoesStale(t *testing.T) {
	loaded, path := testSavedConfig(t)
	dir := filepath.Dir(path)
	s := newTestServer(t)
	s.cfg = loaded
	s.composeDir = dir
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, "old-service-fingerprint")
	s.setStartupState(StartupStarting, "Configuration saved. Waiting for the attached Credimi Runner to restart the service.")
	now := time.Unix(1000, 0)
	oldNow := dashboardNow
	dashboardNow = func() time.Time { return now }
	t.Cleanup(func() { dashboardNow = oldNow })
	if err := writeCoordinatorPresenceFixture(dir, now); err != nil {
		t.Fatal(err)
	}
	typedConfig, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := servicemanager.ServiceConfigFingerprint(typedConfig, true)
	request, err := servicecoordination.NewRestartRequest(fingerprint, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.startupStatus(response, httptest.NewRequest(http.MethodGet, "/startup/status", nil))
	var state map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["phase"] != string(StartupStarting) {
		t.Fatalf("fresh coordinator phase=%v", state["phase"])
	}
	if err := writeCoordinatorPresenceFixture(dir, now.Add(-servicecoordination.CoordinatorMaxAge-time.Second)); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	s.startupStatus(response, httptest.NewRequest(http.MethodGet, "/startup/status", nil))
	state = map[string]any{}
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["phase"] != string(StartupNeedsAttention) || state["message"] != serviceRestartManualMessage {
		t.Fatalf("stale coordinator state=%v", state)
	}
}

func TestStartupStatusWaitsForRuntimeAfterSuccessfulServiceReplacement(t *testing.T) {
	loaded, path := testSavedConfig(t)
	dir := filepath.Dir(path)
	typedConfig, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := servicemanager.ServiceConfigFingerprint(typedConfig, true)
	request, err := servicecoordination.NewRestartRequest(fingerprint, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartResult(dir, servicecoordination.RestartResult{
		RequestID: request.RequestID, Success: true, AppliedFingerprint: fingerprint, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, "old-service-fingerprint")
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning, Actual: runtimesupervisor.ActualStopped}}
	s := newTestServer(t)
	s.cfg = loaded
	s.composeDir = dir
	s.runtime = fake
	response := httptest.NewRecorder()
	s.startupStatus(response, httptest.NewRequest(http.MethodGet, "/startup/status", nil))
	var state map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["phase"] != string(StartupStarting) {
		t.Fatalf("stopped runtime phase=%v", state["phase"])
	}
	fake.mu.Lock()
	fake.status.Actual = runtimesupervisor.ActualRunning
	fake.mu.Unlock()
	response = httptest.NewRecorder()
	s.startupStatus(response, httptest.NewRequest(http.MethodGet, "/startup/status", nil))
	state = map[string]any{}
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["phase"] != string(StartupReady) {
		t.Fatalf("running runtime phase=%v", state["phase"])
	}
}

func TestStartupStatusCompletesForStoppedRuntimeAfterServiceReplacement(t *testing.T) {
	loaded, path := testSavedConfig(t)
	dir := filepath.Dir(path)
	typedConfig, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := servicemanager.ServiceConfigFingerprint(typedConfig, true)
	request, err := servicecoordination.NewRestartRequest(fingerprint, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartResult(dir, servicecoordination.RestartResult{
		RequestID: request.RequestID, Success: true, AppliedFingerprint: fingerprint, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, "old-service-fingerprint")
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredStopped, Actual: runtimesupervisor.ActualStopped}}
	s := newTestServer(t)
	s.cfg = loaded
	s.composeDir = dir
	s.runtime = fake
	response := httptest.NewRecorder()
	s.startupStatus(response, httptest.NewRequest(http.MethodGet, "/startup/status", nil))
	var state map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["phase"] != string(StartupReady) {
		t.Fatalf("stopped runtime phase=%v", state["phase"])
	}
	if starts, _, _, _ := fake.counts(); starts != 0 {
		t.Fatalf("runtime starts=%d, want 0", starts)
	}
}

func TestStartupStatusReflectsStartingAndFailedRuntime(t *testing.T) {
	loaded, path := testSavedConfig(t)
	dir := filepath.Dir(path)
	typedConfig, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(typedConfig, true))
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning, Actual: runtimesupervisor.ActualStarting}}
	s := newTestServer(t)
	s.cfg = loaded
	s.composeDir = dir
	s.runtime = fake
	response := httptest.NewRecorder()
	s.startupStatus(response, httptest.NewRequest(http.MethodGet, "/startup/status", nil))
	var state map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["phase"] != string(StartupStarting) {
		t.Fatalf("starting runtime phase=%v", state["phase"])
	}
	fake.mu.Lock()
	fake.status.Actual = runtimesupervisor.ActualFailed
	fake.status.LastError = "registration failed"
	fake.mu.Unlock()
	response = httptest.NewRecorder()
	s.startupStatus(response, httptest.NewRequest(http.MethodGet, "/startup/status", nil))
	state = map[string]any{}
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["phase"] != string(StartupNeedsAttention) || state["message"] != "registration failed" {
		t.Fatalf("failed runtime state=%v", state)
	}
}

func TestStartupStatusPreservesServiceRestartFailure(t *testing.T) {
	loaded, path := testSavedConfig(t)
	dir := filepath.Dir(path)
	typedConfig, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := servicemanager.ServiceConfigFingerprint(typedConfig, true)
	request, err := servicecoordination.NewRestartRequest(fingerprint, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartResult(dir, servicecoordination.RestartResult{
		RequestID: request.RequestID, Error: "replacement failed", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, "old-service-fingerprint")
	s := newTestServer(t)
	s.cfg = loaded
	s.composeDir = dir
	response := httptest.NewRecorder()
	s.startupStatus(response, httptest.NewRequest(http.MethodGet, "/startup/status", nil))
	var state map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["phase"] != string(StartupNeedsAttention) || state["message"] != "replacement failed" {
		t.Fatalf("service failure state=%v", state)
	}
}

func TestServiceRestartResultStateMatchesCurrentRequest(t *testing.T) {
	loaded, path := testSavedConfig(t)
	dir := filepath.Dir(path)
	fingerprint := servicemanager.ServiceConfigFingerprint(func() runnerconfig.Config {
		cfg, err := runnerconfig.LoadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}(), true)
	request, err := servicecoordination.NewRestartRequest(fingerprint, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartResult(dir, servicecoordination.RestartResult{
		RequestID: request.RequestID, Success: true, AppliedFingerprint: fingerprint, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if !serviceRestartResultApplied(dir, path) || serviceRestartResultFailure(dir) != "" {
		t.Fatal("successful service restart result was not recognized")
	}
	if err := servicecoordination.WriteRestartResult(dir, servicecoordination.RestartResult{
		RequestID: request.RequestID, Error: "replacement failed", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := serviceRestartResultFailure(dir); got != "replacement failed" || serviceRestartResultApplied(dir, loaded.Path()) {
		t.Fatalf("failed service restart result: failure=%q applied=%t", got, serviceRestartResultApplied(dir, loaded.Path()))
	}
}

func persistDashboardValues(t *testing.T, path string, values dashboardruntime.Values) *Config {
	t.Helper()
	cfg, err := dashboardruntime.TypedConfigFromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := runnerconfig.WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestServiceStaleSaveDoesNotReconcileOrPartiallyApply(t *testing.T) {
	loaded, path := testSavedConfig(t)
	oldTyped, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(oldTyped, true))
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := &Server{cfg: loaded, runtime: fake, composeDir: filepath.Dir(path)}
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	newValues := dashboardruntime.Values(loaded.Snapshot())
	newValues["ANDROID_RUNNER_IMAGE"] = "runner:new"
	newValues["TEMPORAL_ADDRESS"] = "temporal:new:7233"
	s.cfg = persistDashboardValues(t, path, newValues)

	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, "linux")
	if !hasApplyClass(diff, dashboardruntime.ApplyServiceRestartRequired) || !hasApplyClass(diff, dashboardruntime.ApplyRuntimeReconcile) {
		t.Fatalf("mixed diff = %+v", diff)
	}
	if err := s.applySavedConfig(context.Background(), diff); err != nil {
		t.Fatal(err)
	}
	if starts, stops, restarts, reconciles := fake.counts(); starts != 0 || stops != 0 || restarts != 0 || reconciles != 0 {
		t.Fatalf("stale save activated runtime: %d/%d/%d/%d", starts, stops, restarts, reconciles)
	}
	if !s.serviceRestartRequired() || !hasApplyClass(s.pendingDiff, dashboardruntime.ApplyRuntimeReconcile) {
		t.Fatalf("stale save state: service=%t pending=%+v", s.serviceRestartRequired(), s.pendingDiff)
	}
}

func TestPureServiceSaveDoesNotReconcileRunningRuntime(t *testing.T) {
	loaded, path := testSavedConfig(t)
	oldTyped, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(oldTyped, true))
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := &Server{cfg: loaded, runtime: fake, composeDir: filepath.Dir(path)}
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	newValues := dashboardruntime.Values(loaded.Snapshot())
	newValues["ANDROID_RUNNER_IMAGE"] = "runner:new"
	s.cfg = persistDashboardValues(t, path, newValues)
	if err := s.applySavedConfig(context.Background(), dashboardruntime.DiffValuesForOS(oldValues, newValues, "linux")); err != nil {
		t.Fatal(err)
	}
	if starts, stops, restarts, reconciles := fake.counts(); starts != 0 || stops != 0 || restarts != 0 || reconciles != 0 {
		t.Fatalf("service-only save activated runtime: %d/%d/%d/%d", starts, stops, restarts, reconciles)
	}
	if len(s.pendingDiff.Classes) != 0 || !s.serviceRestartRequired() {
		t.Fatalf("service-only save state: pending=%+v stale=%t", s.pendingDiff, s.serviceRestartRequired())
	}
}

func TestRuntimeOnlySaveReconcilesWhenRunning(t *testing.T) {
	loaded, path := testSavedConfig(t)
	oldTyped, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(oldTyped, true))
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := &Server{cfg: loaded, runtime: fake, composeDir: filepath.Dir(path)}
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	newValues := dashboardruntime.Values(loaded.Snapshot())
	newValues["TEMPORAL_ADDRESS"] = "temporal:new:7233"
	s.cfg = persistDashboardValues(t, path, newValues)
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, "linux")
	if err := s.applySavedConfig(context.Background(), diff); err != nil {
		t.Fatal(err)
	}
	if _, _, _, reconciles := fake.counts(); reconciles != 1 {
		t.Fatalf("reconcile calls = %d", reconciles)
	}
	if len(s.pendingDiff.Classes) != 0 {
		t.Fatalf("pending diff = %+v", s.pendingDiff)
	}
}

func TestDarwinDashboardListenerSaveStaysPending(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, "")
	loaded, path := testSavedConfig(t)
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := newTestServer(t)
	s.cfg = loaded
	s.runtime = fake
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	newValues := dashboardruntime.Values(loaded.Snapshot())
	newValues["DASHBOARD_PORT"] = "9051"
	s.cfg = persistDashboardValues(t, path, newValues)
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, "darwin")
	if err := s.applySavedConfig(context.Background(), diff); err != nil {
		t.Fatal(err)
	}
	starts, stops, restarts, reconciles := fake.counts()
	if starts != 0 || stops != 0 || restarts != 0 || reconciles != 0 {
		t.Fatalf("Dashboard listener save activated runtime: %d/%d/%d/%d", starts, stops, restarts, reconciles)
	}
	if !s.serviceRestartRequired() || !hasApplyClass(s.pendingDiff, dashboardruntime.ApplyServiceRestartRequired) {
		t.Fatalf("pending service restart lost: service=%t pending=%+v", s.serviceRestartRequired(), s.pendingDiff)
	}
	if !s.pageData("overview", nil).RuntimeStatus().PendingServiceRestart {
		t.Fatal("page status did not report pending service restart")
	}
	for _, action := range []string{"start", "restart"} {
		snapshot, err := s.submitRuntimeAction(action)
		if err != nil {
			t.Fatal(err)
		}
		completed, err := s.operations.Wait(context.Background(), snapshot.ID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Phase != controller.PhaseFailed {
			t.Fatalf("%s was not blocked: %#v", action, completed)
		}
	}
	if got := s.cfg.Snapshot()["DASHBOARD_PORT"]; got != "9051" {
		t.Fatalf("saved Dashboard port = %q", got)
	}

	s.cfg = persistDashboardValues(t, path, oldValues)
	revert := dashboardruntime.DiffValuesForOS(newValues, oldValues, "darwin")
	if err := s.applySavedConfig(context.Background(), revert); err != nil {
		t.Fatal(err)
	}
	if s.serviceRestartRequired() || hasApplyClass(s.pendingDiff, dashboardruntime.ApplyServiceRestartRequired) {
		t.Fatalf("reverted listener remained pending: service=%t pending=%+v applied=%+v desired=%q", s.serviceRestartRequired(), s.pendingDiff, s.appliedServerSettings, s.desiredDashboardListen())
	}
	for _, action := range []string{"start", "restart"} {
		snapshot, err := s.submitRuntimeAction(action)
		if err != nil {
			t.Fatal(err)
		}
		completed, err := s.operations.Wait(context.Background(), snapshot.ID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Phase != controller.PhaseSucceeded {
			t.Fatalf("%s remained blocked after revert: %#v", action, completed)
		}
	}
	if starts, _, restarts, _ := fake.counts(); starts != 1 || restarts != 1 {
		t.Fatalf("runtime calls after revert: start=%d restart=%d", starts, restarts)
	}
}

func TestDarwinMixedDashboardAndRuntimeSaveWaitsForServiceRestart(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, "")
	loaded, path := testSavedConfig(t)
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := newTestServer(t)
	s.cfg = loaded
	s.runtime = fake
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	newValues := dashboardruntime.Values(loaded.Snapshot())
	newValues["DASHBOARD_PORT"] = "9051"
	newValues["TEMPORAL_ADDRESS"] = "temporal-new:7233"
	s.cfg = persistDashboardValues(t, path, newValues)
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, "darwin")
	if err := s.applySavedConfig(context.Background(), diff); err != nil {
		t.Fatal(err)
	}
	_, _, _, reconciles := fake.counts()
	if reconciles != 0 {
		t.Fatalf("mixed save partially reconciled runtime: %d", reconciles)
	}
	if !s.serviceRestartRequired() || !hasApplyClass(s.pendingDiff, dashboardruntime.ApplyRuntimeReconcile) {
		t.Fatalf("mixed save state: service=%t pending=%+v", s.serviceRestartRequired(), s.pendingDiff)
	}

	reverted := dashboardruntime.Values(cloneStringMap(map[string]string(newValues)))
	reverted["DASHBOARD_PORT"] = oldValues["DASHBOARD_PORT"]
	s.cfg = persistDashboardValues(t, path, reverted)
	second := dashboardruntime.DiffValuesForOS(newValues, reverted, "darwin")
	if err := s.applySavedConfig(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if s.serviceRestartRequired() || hasApplyClass(s.pendingDiff, dashboardruntime.ApplyServiceRestartRequired) {
		t.Fatalf("reverted mixed listener remained pending: service=%t pending=%+v applied=%+v desired=%q", s.serviceRestartRequired(), s.pendingDiff, s.appliedServerSettings, s.desiredDashboardListen())
	}
	if _, _, _, reconciles := fake.counts(); reconciles != 1 {
		t.Fatalf("runtime reconcile calls after revert = %d", reconciles)
	}
	if len(s.pendingDiff.Classes) != 0 {
		t.Fatalf("pending diff after revert = %+v", s.pendingDiff)
	}
}

func TestDarwinRunnerPortSaveReconcilesRuntime(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, "")
	loaded, path := testSavedConfig(t)
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := newTestServer(t)
	s.cfg = loaded
	s.runtime = fake
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	newValues := dashboardruntime.Values(loaded.Snapshot())
	newValues["RUNNER_PORT"] = "9050"
	s.cfg = persistDashboardValues(t, path, newValues)
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, "darwin")
	if err := s.applySavedConfig(context.Background(), diff); err != nil {
		t.Fatal(err)
	}
	starts, stops, restarts, reconciles := fake.counts()
	if starts != 0 || stops != 0 || restarts != 0 || reconciles != 1 {
		t.Fatalf("runner port save calls: %d/%d/%d/%d", starts, stops, restarts, reconciles)
	}
	if s.serviceRestartRequired() {
		t.Fatal("runner port save incorrectly required service restart")
	}
}

func TestRuntimeOnlySaveWhileStoppedDoesNotCreateRuntime(t *testing.T) {
	loaded, path := testSavedConfig(t)
	oldTyped, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(oldTyped, true))
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredStopped}}
	s := &Server{cfg: loaded, runtime: fake, composeDir: filepath.Dir(path)}
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	newValues := dashboardruntime.Values(loaded.Snapshot())
	newValues["TEMPORAL_ADDRESS"] = "temporal:new:7233"
	s.cfg = persistDashboardValues(t, path, newValues)
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, "linux")
	if err := s.applySavedConfig(context.Background(), diff); err != nil {
		t.Fatal(err)
	}
	if starts, stops, restarts, reconciles := fake.counts(); starts != 0 || stops != 0 || restarts != 0 || reconciles != 0 {
		t.Fatalf("stopped save activated runtime: %d/%d/%d/%d", starts, stops, restarts, reconciles)
	}
	if !hasApplyClass(s.pendingDiff, dashboardruntime.ApplyRuntimeReconcile) {
		t.Fatalf("pending runtime change lost: %+v", s.pendingDiff)
	}
}

func TestRevertingStaleTopologyAppliesAccumulatedRuntimeChange(t *testing.T) {
	loaded, path := testSavedConfig(t)
	oldTyped, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(oldTyped, true))
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := &Server{cfg: loaded, runtime: fake, composeDir: filepath.Dir(loaded.Path())}
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	serviceValues := dashboardruntime.Values(loaded.Snapshot())
	serviceValues["ANDROID_RUNNER_IMAGE"] = "runner:new"
	serviceValues["TEMPORAL_ADDRESS"] = "temporal:new:7233"
	s.cfg = persistDashboardValues(t, path, serviceValues)
	first := dashboardruntime.DiffValuesForOS(oldValues, serviceValues, "linux")
	if err := s.applySavedConfig(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	reverted := dashboardruntime.Values(serviceValues)
	reverted["ANDROID_RUNNER_IMAGE"] = oldValues["ANDROID_RUNNER_IMAGE"]
	s.cfg = persistDashboardValues(t, path, reverted)
	second := dashboardruntime.DiffValuesForOS(serviceValues, reverted, "linux")
	if err := s.applySavedConfig(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if s.serviceRestartRequired() {
		t.Fatal("reverted topology still reported stale")
	}
	if _, _, _, reconciles := fake.counts(); reconciles != 1 {
		t.Fatalf("reconcile calls = %d", reconciles)
	}
	if len(s.pendingDiff.Classes) != 0 {
		t.Fatalf("pending diff = %+v", s.pendingDiff)
	}
}

func TestFailedRuntimeReconcileRetainsPendingChange(t *testing.T) {
	loaded, path := testSavedConfig(t)
	oldTyped, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(oldTyped, true))
	want := errors.New("reconcile failed")
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}, reconcileErr: want}
	s := &Server{cfg: loaded, runtime: fake, composeDir: filepath.Dir(loaded.Path())}
	oldValues := dashboardruntime.Values(loaded.Snapshot())
	newValues := dashboardruntime.Values(loaded.Snapshot())
	newValues["TEMPORAL_ADDRESS"] = "temporal:new:7233"
	s.cfg = persistDashboardValues(t, path, newValues)
	err = s.applySavedConfig(context.Background(), dashboardruntime.DiffValuesForOS(oldValues, newValues, "linux"))
	if !errors.Is(err, want) {
		t.Fatalf("reconcile error = %v", err)
	}
	if !hasApplyClass(s.pendingDiff, dashboardruntime.ApplyRuntimeReconcile) {
		t.Fatalf("failed change was not retained: %+v", s.pendingDiff)
	}
}

func TestStaleServiceBlocksRuntimeActivationButAllowsStop(t *testing.T) {
	loaded, path := testSavedConfig(t)
	oldTyped, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(oldTyped, true))
	values := dashboardruntime.Values(loaded.Snapshot())
	values["ANDROID_RUNNER_IMAGE"] = "runner:new"
	loaded = persistDashboardValues(t, path, values)
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := &Server{cfg: loaded, runtime: fake, operations: controller.NewCoordinator(context.Background())}
	for _, action := range []string{"start", "restart"} {
		snapshot, err := s.submitRuntimeAction(action)
		if err != nil {
			t.Fatal(err)
		}
		completed, err := s.operations.Wait(context.Background(), snapshot.ID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Phase != controller.PhaseFailed || !strings.Contains(completed.Error, "credimi-runner service restart") {
			t.Fatalf("%s operation = %#v", action, completed)
		}
	}
	if starts, _, restarts, _ := fake.counts(); starts != 0 || restarts != 0 {
		t.Fatalf("stale service activated runtime: start=%d restart=%d", starts, restarts)
	}
	stopSnapshot, err := s.submitRuntimeAction("stop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.operations.Wait(context.Background(), stopSnapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, stops, _, _ := fake.counts(); stops != 1 {
		t.Fatalf("stop calls = %d", stops)
	}
}

func TestExplicitStartClearsPendingRuntimeChange(t *testing.T) {
	loaded, path := testSavedConfig(t)
	oldTyped, err := runnerconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(oldTyped, true))
	fake := &fakeRuntimeController{status: runtimesupervisor.Status{Desired: runtimesupervisor.DesiredRunning}}
	s := &Server{cfg: loaded, runtime: fake, operations: controller.NewCoordinator(context.Background()), pendingDiff: dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRuntimeReconcile}}}
	snapshot, err := s.submitRuntimeAction("start")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := s.operations.Wait(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Phase != controller.PhaseSucceeded {
		t.Fatalf("start operation = %#v", completed)
	}
	if starts, _, _, _ := fake.counts(); starts != 1 {
		t.Fatalf("start calls = %d", starts)
	}
	if len(s.pendingDiff.Classes) != 0 {
		t.Fatalf("pending diff = %+v", s.pendingDiff)
	}
}

func TestDashboardConfigIdentityAndImpactHelpers(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct {
		class dashboardruntime.ApplyClass
		want  string
	}{
		{dashboardruntime.ApplyServiceRestartRequired, "persistent service must restart"},
		{dashboardruntime.ApplyRuntimeRestartRequired, "runtime generation must restart"},
		{dashboardruntime.ApplyRuntimeReconcile, "runtime generation will be reconciled"},
		{dashboardruntime.ApplyCredimiUpdateRequired, "runner record in Credimi"},
		{"", ""},
	} {
		got := describeDiffImpact(dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{tc.class}})
		if !strings.Contains(got, tc.want) {
			t.Fatalf("impact %q = %q", tc.class, got)
		}
	}
	if got := clonePostForm(url.Values{"key": {"value"}}); got.Get("key") != "value" {
		t.Fatalf("cloned form = %#v", got)
	}
	if _, err := s.submitRuntimeAction("invalid"); err == nil {
		t.Fatal("unsupported runtime action was accepted")
	}

	current := map[string]string{
		"CREDIMI_URL":                 "https://credimi.example",
		"CREDIMI_RUNNER_ID":           "org/runner",
		"CREDIMI_RUNNER_NAME":         "runner",
		"CREDIMI_RUNNER_ORGANIZATION": "org",
		"CREDIMI_USER_API_KEY":        "user-key",
	}
	if err := s.resolveConfigIdentity(context.Background(), current, map[string]string{"CREDIMI_RUNNER_ID": "org/other"}); err == nil {
		t.Fatal("runner ID change was accepted")
	}
	if err := s.resolveConfigIdentity(context.Background(), current, map[string]string{"CREDIMI_RUNNER_ORGANIZATION": "other"}); err == nil {
		t.Fatal("user runner organization change was accepted")
	}
	withoutKey := map[string]string{"CREDIMI_RUNNER_ID": "org/runner", "CREDIMI_RUNNER_NAME": "runner", "CREDIMI_RUNNER_ORGANIZATION": "org"}
	if err := s.resolveConfigIdentity(context.Background(), withoutKey, map[string]string{"CREDIMI_RUNNER_NAME": "renamed"}); err == nil {
		t.Fatal("identity update without a key was accepted")
	}

	incoming := map[string]string{}
	if err := s.resolveConfigIdentity(context.Background(), current, incoming); err != nil {
		t.Fatal(err)
	}
	if incoming["CREDIMI_RUNNER_ID"] != "org/runner" {
		t.Fatalf("resolved identity = %#v", incoming)
	}
}

func TestServerAuth(t *testing.T) {
	s := newTestServer(t)
	s.cfg.mu.Lock()
	s.cfg.values["DASHBOARD_TOKEN"] = "token"
	s.cfg.mu.Unlock()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	s.auth(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized code = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config?token=token", nil)
	s.auth(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("query token code = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/config", nil)
	req.Header.Set("Authorization", "Bearer token")
	s.auth(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bearer token code = %d", rec.Code)
	}
	s.cfg.mu.Lock()
	s.cfg.values["DASHBOARD_TOKEN"] = "rotated"
	s.cfg.mu.Unlock()
	rec = httptest.NewRecorder()
	s.auth(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config?token=token", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token remained valid after rotation = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.auth(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config?token=rotated", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("rotated token code = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.auth(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("static bypass code = %d", rec.Code)
	}
}

func TestServerPageAndPageData(t *testing.T) {
	s := newTestServer(t)
	data := s.pageData("overview", map[string]any{"Saved": true})
	if data.Active != "overview" || data.Title != "Overview" || data.Pill.Label != "All healthy" {
		t.Fatalf("pageData = %#v", data)
	}

	rec := httptest.NewRecorder()
	s.page("overview").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("page code = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Credimi Runner — Setup") {
		t.Fatalf("first run should render setup page, got: %s", rec.Body.String()[:200])
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	req.Header.Set("HX-Request", "true")
	s.page("config").ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "API &amp; Config") || !strings.Contains(rec.Body.String(), "data-config-form") {
		t.Fatalf("fragment code/body = %d %s", rec.Code, rec.Body.String())
	}

	if _, err := s.cfg.Apply(map[string]string{"CREDIMI_RUNNER_ID": "acme/runner"}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.page("overview").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Set up Credimi Runner") {
		t.Fatalf("configured GET / should render dashboard, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestServerConfigHandlers(t *testing.T) {
	s := newTestServer(t)
	s.cfg.values["CREDIMI_USER_API_KEY"] = "test-secret-value-123"

	rec := httptest.NewRecorder()
	s.rawConfig(rec, httptest.NewRequest(http.MethodGet, "/config/raw", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("rawConfig code = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "test-secret-value-123") {
		t.Fatal("masked raw config leaked secret")
	}

	rec = httptest.NewRecorder()
	s.rawConfig(rec, httptest.NewRequest(http.MethodGet, "/config/raw?reveal=1", nil))
	if !strings.Contains(rec.Body.String(), "test-secret-value-123") {
		t.Fatal("revealed raw config missing secret")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /config/secret/{key}", s.revealSecret)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config/secret/CREDIMI_USER_API_KEY", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "test-secret-value-123") {
		t.Fatalf("reveal secret = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config/secret/CREDIMI_URL", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-secret reveal code = %d", rec.Code)
	}
}

func TestServerSetupRenderHelpers(t *testing.T) {
	s := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup", nil)
	req.Header.Set("HX-Request", "true")
	s.renderSetupComplete(rec, req)
	if rec.Code != http.StatusAccepted || rec.Header().Get("HX-Redirect") != "" {
		t.Fatalf("htmx renderSetupComplete = %d headers=%v", rec.Code, rec.Header())
	}

	rec = httptest.NewRecorder()
	s.renderSetupComplete(rec, httptest.NewRequest(http.MethodPost, "/setup", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("plain renderSetupComplete = %d headers=%v", rec.Code, rec.Header())
	}

	rec = httptest.NewRecorder()
	s.renderSetupError(rec, map[string]string{"CREDIMI_RUNNER_NAME": "runner"}, "broken")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "broken") {
		t.Fatalf("renderSetupError = %d %s", rec.Code, rec.Body.String())
	}
}

func TestValidateSetupInputRejectsManualURLWithoutScheme(t *testing.T) {
	errs := validateSetupInput(map[string]string{
		"CREDIMI_URL":          "https://credimi.example",
		"CREDIMI_USER_API_KEY": "user-key",
		"CREDIMI_RUNNER_NAME":  "runner",
		"CREDIMI_SERVICE_MODE": "manual",
		"RUNNER_PUBLIC_URL":    "runner.example",
	})
	if got := errs["RUNNER_PUBLIC_URL"]; !strings.Contains(got, "http:// or https://") {
		t.Fatalf("manual public URL validation = %q", got)
	}
}

func TestSetupPageDisplaysStartupFailure(t *testing.T) {
	s := newTestServer(t)
	s.setStartupState(StartupNeedsAttention, "runtime start failed: public URL is unreachable")
	recorder := httptest.NewRecorder()
	s.page("setup")(recorder, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "public URL is unreachable") {
		t.Fatalf("setup failure page = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestServerMaintenanceCheckRefreshesMetadata(t *testing.T) {
	s := newTestServer(t)
	s.maintenanceChecked = false
	calls := 0
	s.maintenanceChecker = func(context.Context, string, time.Time) maintenance.Status {
		calls++
		return maintenance.Status{Runner: maintenance.Component{LatestVersion: "v2", UpdateAvailable: true}}
	}
	recorder := httptest.NewRecorder()
	s.maintenanceCheck(recorder, httptest.NewRequest(http.MethodPost, "/maintenance/check", nil))
	if recorder.Code != http.StatusOK || calls != 1 || !s.maintenance.Runner.UpdateAvailable {
		t.Fatalf("code=%d calls=%d status=%#v", recorder.Code, calls, s.maintenance)
	}
	s.ensureMaintenanceChecked(context.Background(), false)
	if calls != 1 {
		t.Fatalf("cached check calls = %d", calls)
	}
}

func TestServerSetupHelperEndpoints(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		switch req.URL.Path {
		case "/api/organizations/my":
			body = `{"canonified_name":"acme","name":"Acme"}`
		case "/api/canonify/identifier/validate":
			body = `{"record":{"slug":"runner-slug"}}`
		case "/api/mobile-runner/preview-id":
			body = `{"organization":"acme","runner_id":"acme/runner-slug-2"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	s := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup/organization", strings.NewReader(`{"instance_url":"https://credimi.example","api_key":"key"}`))
	s.lookupSetupOrganization(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"canonified_name":"acme"`) {
		t.Fatalf("lookupSetupOrganization = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/setup/canonify?name=Runner+Slug", strings.NewReader(`{"instance_url":"https://credimi.example","api_key":"key"}`))
	s.canonifySetupName(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"canonified":"runner-slug"`) {
		t.Fatalf("canonifySetupName = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/setup/preview-id", strings.NewReader(`{"instance_url":"https://credimi.example","api_key":"key","organization":"acme","name":"Runner Slug"}`))
	s.previewSetupRunnerID(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"runner_id":"acme/runner-slug-2"`) {
		t.Fatalf("previewSetupRunnerID = %d %s", rec.Code, rec.Body.String())
	}
}

func TestServerSetupHelperEndpointValidation(t *testing.T) {
	s := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup/organization", strings.NewReader(`{`))
	s.lookupSetupOrganization(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("lookupSetupOrganization invalid JSON = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/setup/canonify", strings.NewReader(`{"instance_url":"https://credimi.example","api_key":"key"}`))
	s.canonifySetupName(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("canonifySetupName missing name = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/setup/preview-id", strings.NewReader(`{"instance_url":"https://credimi.example"}`))
	s.previewSetupRunnerID(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("previewSetupRunnerID missing fields = %d", rec.Code)
	}
}

func TestStartupStatusReturnsCurrentSetupProgress(t *testing.T) {
	s := newTestServer(t)
	s.startup.Phase = StartupWaitingRunner
	s.startup.Message = "Runtime started. Waiting for runner readiness."
	s.startup.Logs = []string{"Pulling Docker images.", "runner Pulling fs layer"}
	s.startup.LogBase = 1
	s.startup.LogNextID = 3
	s.startup.running = true

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/startup/status", nil)
	s.startupStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("startupStatus = %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"phase":"waiting_for_runner"`) ||
		!strings.Contains(body, `"running":true`) ||
		!strings.Contains(body, `"next_id":3`) ||
		!strings.Contains(body, "Waiting for runner readiness") ||
		!strings.Contains(body, "runner Pulling fs layer") {
		t.Fatalf("startupStatus body = %s", body)
	}

	s.appendStartupLog("runner Downloading 128MB")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/startup/status?since=3", nil)
	s.startupStatus(rec, req)
	body = rec.Body.String()
	if strings.Contains(body, "runner Pulling fs layer") ||
		!strings.Contains(body, "runner Downloading 128MB") ||
		!strings.Contains(body, `"next_id":4`) {
		t.Fatalf("startupStatus cursor body = %s", body)
	}
}

func TestServerSystemMetricsReturnsEmptySnapshotWithoutMonitor(t *testing.T) {
	s := newTestServer(t)
	s.systemMonitor = nil
	recorder := httptest.NewRecorder()
	s.systemMetrics(recorder, httptest.NewRequest(http.MethodGet, "/api/system-metrics", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"samples":[]`) || !strings.Contains(recorder.Body.String(), `"interval_ms":2000`) {
		t.Fatalf("empty system metrics = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestServerDeviceAndSSEHelpers(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.deviceError(rec, `bad <device> "quoted"`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("deviceError code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "&lt;device&gt;") || !strings.Contains(rec.Body.String(), "&quot;quoted&quot;") {
		t.Fatalf("deviceError did not escape body: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	writeSSE(rec, "rows", "a\nb")
	if got := rec.Body.String(); got != "event: rows\ndata: a\ndata: b\n\n" {
		t.Fatalf("writeSSE = %q", got)
	}

	if got := htmlAttr(`a&b<c>"`); got != "a&amp;b&lt;c&gt;&quot;" {
		t.Fatalf("htmlAttr = %q", got)
	}
}

func TestServerSSE(t *testing.T) {
	s := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events/health", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.sse("health").ServeHTTP(rec, req)
		close(done)
	}()
	cancel()
	<-done
	if !strings.Contains(rec.Body.String(), "event: pill") {
		t.Fatalf("sse body = %q", rec.Body.String())
	}
}

func TestServerRuntimeSSE(t *testing.T) {
	s := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events/runtime", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.sse("runtime").ServeHTTP(rec, req)
		close(done)
	}()
	cancel()
	<-done
	if !strings.Contains(rec.Body.String(), "event: runtime") {
		t.Fatalf("runtime sse body = %q", rec.Body.String())
	}
}

func TestDialTemporalOnline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Close()
		}
		close(done)
	}()
	if got := dialTemporal(ln.Addr().String()); got != Online {
		t.Fatalf("listening temporal = %s", got)
	}
	<-done
}
func TestApplyDeviceDefaultsAndRegistrationRequirements(t *testing.T) {
	emulator := dashboardruntime.DeviceRuntimeConfig{Type: "android_emulator", Mode: "emulator"}
	applyDeviceDefaults(&emulator)
	if emulator.Values["BASE_NAME"] != "credimi" || emulator.Values["AVD_NAME"] != "" || emulator.Values["GOLDEN_PATH"] == "" || emulator.Values["ANDROID_KEYS_DIR"] == "" || emulator.Values["HOST_AVD_HOME_PATH"] == "" || emulator.Values["HOST_AVD_GOLDEN_PATH"] == "" {
		t.Fatalf("emulator defaults = %#v", emulator.Values)
	}
	redroid := dashboardruntime.DeviceRuntimeConfig{Type: "redroid", Mode: "no_device", Values: dashboardruntime.Values{}}
	applyDeviceDefaults(&redroid)
	if redroid.Values["WIFI_PORT"] != "5555" || redroid.Values["REDROID_DATA_DIR"] == "" {
		t.Fatalf("redroid defaults = %#v", redroid.Values)
	}
}

func TestSetupDevicesUsesBaseNameAsEmulatorID(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mobile-device/preview-id" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_id":"acme/runner/emulator"}`))
	}))
	defer api.Close()

	s := newTestServer(t)
	values := map[string]string(dashboardruntime.DefaultValues())
	values["CREDIMI_URL"] = api.URL
	values["CREDIMI_USER_API_KEY"] = "key"
	values["CREDIMI_RUNNER_ID"] = "acme/runner"
	values["CREDIMI_RUNNER_NAME"] = "runner"
	values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	form := url.Values{
		"SETUP_DEVICE_COUNT":       {"1"},
		"SETUP_DEVICE_1_NAME":      {"Emulator"},
		"SETUP_DEVICE_1_TYPE":      {"android_emulator"},
		"SETUP_DEVICE_1_MODE":      {"emulator"},
		"SETUP_DEVICE_1_BASE_NAME": {"credimi"},
	}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	devices, err := s.setupDevices(req, values)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Values["BASE_NAME"] != "credimi" || devices[0].Values["AVD_NAME"] != "" {
		t.Fatalf("emulator device = %#v", devices)
	}
	store, err := dashboardruntime.LoadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values(values), Devices: devices}); err != nil {
		t.Fatalf("emulator inventory must persist: %v", err)
	}
	if store.Values["CREDIMI_DEVICE_1_AVD_NAME"] != "credimi" {
		t.Fatalf("derived emulator compatibility name = %#v", store.Values)
	}
}

func TestSetupDevicesParsesTwoUSBDevicesInCardOrder(t *testing.T) {
	var previewCount int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		previewCount++
		_ = json.NewEncoder(w).Encode(map[string]string{"device_id": fmt.Sprintf("acme/runner/device-%d", previewCount)})
	}))
	defer api.Close()

	s := newTestServer(t)
	form := url.Values{
		"SETUP_DEVICE_COUNT":    {"2"},
		"SETUP_DEVICE_1_NAME":   {"Pixel One"},
		"SETUP_DEVICE_1_TYPE":   {"android_phone"},
		"SETUP_DEVICE_1_MODE":   {"usb"},
		"SETUP_DEVICE_1_SERIAL": {"SERIAL_ONE"},
		"SETUP_DEVICE_2_NAME":   {"Pixel Two"},
		"SETUP_DEVICE_2_TYPE":   {"android_phone"},
		"SETUP_DEVICE_2_MODE":   {"usb"},
		"SETUP_DEVICE_2_SERIAL": {"SERIAL_TWO"},
	}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	devices, err := s.setupDevices(req, map[string]string{
		"CREDIMI_URL":                 api.URL,
		"CREDIMI_USER_API_KEY":        "key",
		"CREDIMI_RUNNER_ID":           "acme/runner",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %#v", devices)
	}
	for index, want := range []struct{ name, serial string }{{"Pixel One", "SERIAL_ONE"}, {"Pixel Two", "SERIAL_TWO"}} {
		if devices[index].Name != want.name || devices[index].Type != "android_phone" || devices[index].Mode != "usb" || devices[index].Serial != want.serial {
			t.Fatalf("device %d = %#v", index+1, devices[index])
		}
	}
}

func TestSetupDevicesKeepsMixedTypesAndModesInCardOrder(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"device_id": "acme/runner/device" + r.URL.Path})
	}))
	defer api.Close()

	s := newTestServer(t)
	form := url.Values{
		"SETUP_DEVICE_COUNT":    {"2"},
		"SETUP_DEVICE_1_NAME":   {"USB phone"},
		"SETUP_DEVICE_1_TYPE":   {"android_phone"},
		"SETUP_DEVICE_1_MODE":   {"usb"},
		"SETUP_DEVICE_1_SERIAL": {"SERIAL_ONE"},
		"SETUP_DEVICE_2_NAME":   {"Emulator"},
		"SETUP_DEVICE_2_TYPE":   {"android_emulator"},
		"SETUP_DEVICE_2_MODE":   {"emulator"},
	}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	devices, err := s.setupDevices(req, map[string]string{
		"CREDIMI_URL":                 api.URL,
		"CREDIMI_USER_API_KEY":        "key",
		"CREDIMI_RUNNER_ID":           "acme/runner",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	if devices[0].Type != "android_phone" || devices[0].Mode != "usb" || devices[0].Serial != "SERIAL_ONE" || devices[1].Type != "android_emulator" || devices[1].Mode != "emulator" {
		t.Fatalf("mixed devices = %#v", devices)
	}
}

func TestSetupDevicesKeepsIndexedOptionalFieldsWithTheirCards(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"device_id": "acme/runner/device" + r.URL.Path})
	}))
	defer api.Close()

	cases := []struct {
		name  string
		form  url.Values
		check func(*testing.T, []dashboardruntime.DeviceRuntimeConfig)
	}{
		{
			name: "wifi then usb",
			form: url.Values{
				"SETUP_DEVICE_COUNT":       {"2"},
				"SETUP_DEVICE_1_NAME":      {"Wi-Fi phone"},
				"SETUP_DEVICE_1_TYPE":      {"android_phone"},
				"SETUP_DEVICE_1_MODE":      {"wifi"},
				"SETUP_DEVICE_1_WIFI_IP":   {"192.168.1.10"},
				"SETUP_DEVICE_1_WIFI_PORT": {"5555"},
				"SETUP_DEVICE_2_NAME":      {"USB phone"},
				"SETUP_DEVICE_2_TYPE":      {"android_phone"},
				"SETUP_DEVICE_2_MODE":      {"usb"},
				"SETUP_DEVICE_2_SERIAL":    {"SERIAL_TWO"},
			},
			check: func(t *testing.T, devices []dashboardruntime.DeviceRuntimeConfig) {
				if devices[0].Values["WIFI_IP"] != "192.168.1.10" || devices[0].Serial != "192.168.1.10:5555" || devices[1].Serial != "SERIAL_TWO" {
					t.Fatalf("devices = %#v", devices)
				}
			},
		},
		{
			name: "usb then wifi",
			form: url.Values{
				"SETUP_DEVICE_COUNT":       {"2"},
				"SETUP_DEVICE_1_NAME":      {"USB phone"},
				"SETUP_DEVICE_1_TYPE":      {"android_phone"},
				"SETUP_DEVICE_1_MODE":      {"usb"},
				"SETUP_DEVICE_1_SERIAL":    {"SERIAL_ONE"},
				"SETUP_DEVICE_2_NAME":      {"Wi-Fi phone"},
				"SETUP_DEVICE_2_TYPE":      {"android_phone"},
				"SETUP_DEVICE_2_MODE":      {"wifi"},
				"SETUP_DEVICE_2_WIFI_IP":   {"192.168.1.20"},
				"SETUP_DEVICE_2_WIFI_PORT": {"5555"},
			},
			check: func(t *testing.T, devices []dashboardruntime.DeviceRuntimeConfig) {
				if devices[0].Serial != "SERIAL_ONE" || devices[1].Values["WIFI_IP"] != "192.168.1.20" || devices[1].Serial != "192.168.1.20:5555" {
					t.Fatalf("devices = %#v", devices)
				}
			},
		},
		{
			name: "emulator then usb",
			form: url.Values{
				"SETUP_DEVICE_COUNT":       {"2"},
				"SETUP_DEVICE_1_NAME":      {"Emulator"},
				"SETUP_DEVICE_1_TYPE":      {"android_emulator"},
				"SETUP_DEVICE_1_MODE":      {"emulator"},
				"SETUP_DEVICE_1_BASE_NAME": {"credimi-one"},
				"SETUP_DEVICE_2_NAME":      {"USB phone"},
				"SETUP_DEVICE_2_TYPE":      {"android_phone"},
				"SETUP_DEVICE_2_MODE":      {"usb"},
				"SETUP_DEVICE_2_SERIAL":    {"SERIAL_TWO"},
			},
			check: func(t *testing.T, devices []dashboardruntime.DeviceRuntimeConfig) {
				if devices[0].Values["BASE_NAME"] != "credimi-one" || devices[1].Serial != "SERIAL_TWO" {
					t.Fatalf("devices = %#v", devices)
				}
			},
		},
		{
			name: "usb then redroid",
			form: url.Values{
				"SETUP_DEVICE_COUNT":       {"2"},
				"SETUP_DEVICE_1_NAME":      {"USB phone"},
				"SETUP_DEVICE_1_TYPE":      {"android_phone"},
				"SETUP_DEVICE_1_MODE":      {"usb"},
				"SETUP_DEVICE_1_SERIAL":    {"SERIAL_ONE"},
				"SETUP_DEVICE_2_NAME":      {"Redroid"},
				"SETUP_DEVICE_2_TYPE":      {"redroid"},
				"SETUP_DEVICE_2_MODE":      {"no_device"},
				"SETUP_DEVICE_2_WIFI_IP":   {"192.168.1.30"},
				"SETUP_DEVICE_2_WIFI_PORT": {"5555"},
			},
			check: func(t *testing.T, devices []dashboardruntime.DeviceRuntimeConfig) {
				if devices[0].Serial != "SERIAL_ONE" || devices[1].Values["WIFI_IP"] != "192.168.1.30" {
					t.Fatalf("devices = %#v", devices)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(tc.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			devices, err := s.setupDevices(req, map[string]string{
				"CREDIMI_URL": api.URL, "CREDIMI_USER_API_KEY": "key", "CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_RUNNER_ORGANIZATION": "acme",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(devices) != 2 {
				t.Fatalf("devices = %#v", devices)
			}
			tc.check(t, devices)
		})
	}
}

func TestResolveSetupIdentityBranches(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		switch req.URL.Path {
		case "/api/organizations/my":
			body = `{"canonified_name":"acme"}`
		case "/api/mobile-runner/preview-id":
			payload, _ := io.ReadAll(req.Body)
			if strings.Contains(string(payload), `"name":"Runner Two"`) {
				body = `{"organization":"acme","runner_id":"acme/runner-two-2"}`
			} else {
				body = `{"organization":"acme","runner_id":"acme/runner-one"}`
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	s := newTestServer(t)
	values := map[string]string{
		"CREDIMI_URL":          "https://credimi.example",
		"CREDIMI_USER_API_KEY": "user-key",
		"CREDIMI_RUNNER_NAME":  "Runner One",
	}
	if err := s.resolveSetupIdentity(context.Background(), values); err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_ID"] != "acme/runner-one" || values["CREDIMI_RUNNER_ORGANIZATION"] != "acme" {
		t.Fatalf("resolved values = %#v", values)
	}

	values = map[string]string{
		"CREDIMI_URL":                         "https://credimi.example",
		"CREDIMI_USER_API_KEY":                "user-key",
		"CREDIMI_RUNNER_NAME":                 "Runner Two",
		"CREDIMI_RUNNER_ORGANIZATION":         "acme",
		"CREDIMI_RUNNER_NAME_CONFLICT_ACTION": "create",
	}
	if err := s.resolveSetupIdentity(context.Background(), values); err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_ID"] != "acme/runner-two-2" {
		t.Fatalf("create action resolved ID = %q", values["CREDIMI_RUNNER_ID"])
	}

	values["CREDIMI_RUNNER_ID"] = ""
	values["CREDIMI_RUNNER_NAME_CONFLICT_ACTION"] = "invalid"
	if err := s.resolveSetupIdentity(context.Background(), values); err == nil {
		t.Fatal("expected invalid conflict action to fail")
	}

	values = map[string]string{
		"CREDIMI_URL":                 "https://credimi.example",
		"CREDIMI_USER_API_KEY":        "user-key",
		"CREDIMI_RUNNER_NAME":         "Runner Two",
		"CREDIMI_RUNNER_ID":           "existing/id",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
	}
	if err := s.resolveSetupIdentity(context.Background(), values); err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_ID"] != "existing/id" {
		t.Fatalf("existing runner ID should be preserved: %#v", values)
	}
}

func TestServerManagedDeviceActions(t *testing.T) {
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-device" {
			return nil, errors.New("unexpected path: " + req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values(s.cfg.Snapshot()), Devices: []dashboardruntime.DeviceRuntimeConfig{{ID: "acme/runner/pixel", Name: "Pixel", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "usb-1", Values: dashboardruntime.Values{}}, {ID: "acme/runner/pixel-2", Name: "Pixel Two", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "usb-2", Values: dashboardruntime.Values{}}}}); err != nil {
		t.Fatal(err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	post := func(handler http.HandlerFunc, form url.Values) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		handler(rec, req)
		return rec
	}
	if rec := post(s.deviceDisable, url.Values{"device_id": {"acme/runner/pixel"}}); rec.Code != http.StatusAccepted {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body.String())
	} else {
		waitForQueuedOperation(t, s, rec)
	}
	if rec := post(s.deviceRemove, url.Values{"device_id": {"acme/runner/pixel"}, "confirm": {"true"}}); rec.Code != http.StatusAccepted {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	} else {
		waitForQueuedOperation(t, s, rec)
	}
}

func TestServerValidateRuntimeRequirements(t *testing.T) {
	base := map[string]string{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner/pixel", "CREDIMI_DEVICE_1_TYPE": "android_phone", "CREDIMI_DEVICE_1_MODE": "usb", "CREDIMI_DEVICE_1_SERIAL": "device-1"}
	s := newTestServer(t)
	if err := s.validateRuntimeRequirements(base); err != nil {
		t.Fatalf("connected phone requirements = %v", err)
	}
	s.hub.snap.Devices[0].Status = Offline
	if err := s.validateRuntimeRequirements(base); err != nil {
		t.Fatalf("offline phone should remain persistable for topology expansion = %v", err)
	}
	emulator := cloneStringMap(base)
	emulator["CREDIMI_DEVICE_1_TYPE"] = "android_emulator"
	emulator["CREDIMI_DEVICE_1_MODE"] = "emulator"
	emulator["CREDIMI_DEVICE_1_ANDROID_KEYS_DIR"] = "/keys"
	emulator["CREDIMI_DEVICE_1_HOST_AVD_HOME_PATH"] = "/avd"
	emulator["CREDIMI_DEVICE_1_HOST_AVD_GOLDEN_PATH"] = "/golden"
	if err := s.validateRuntimeRequirements(emulator); err != nil {
		t.Fatalf("emulator candidate requirements = %v", err)
	}
}
func TestServerSaveAndFinishSetupValidationErrors(t *testing.T) {
	s := newTestServer(t)
	form := url.Values{"CREDIMI_URL": {"not-a-url"}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveConfig(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("saveConfig validation = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.finishSetup(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Some fields need attention") {
		t.Fatalf("finishSetup validation = %d %s", rec.Code, rec.Body.String())
	}
}

func TestServerDevicePreviewAndConfigNormalizationEndpoints(t *testing.T) {
	s := newTestServer(t)
	missing := httptest.NewRecorder()
	s.devicePreviewID(missing, httptest.NewRequest(http.MethodPost, "/devices/preview-id", nil))
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "name is required") {
		t.Fatalf("missing preview name = %d %s", missing.Code, missing.Body.String())
	}

	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-device/preview-id" {
			return nil, errors.New("unexpected path: " + req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_id":"acme/runner/pixel"}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"

	preview := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/devices/preview-id", strings.NewReader(url.Values{"name": {"Pixel"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.devicePreviewID(preview, request)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "acme/runner/pixel") {
		t.Fatalf("device preview = %d %s", preview.Code, preview.Body.String())
	}

	normalized := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/config/normalize", strings.NewReader(url.Values{"RUNNER_PORT": {" 9000 "}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.normalizeConfigPreview(normalized, request)
	if normalized.Code != http.StatusOK || !strings.Contains(normalized.Body.String(), `"RUNNER_PORT":"9000"`) {
		t.Fatalf("normalized preview = %d %s", normalized.Code, normalized.Body.String())
	}
	if _, err := normalizedConfigValues(map[string]string{"CREDIMI_DEVICE_COUNT": "1"}, map[string]string{}, "linux"); err == nil {
		t.Fatal("invalid indexed configuration was normalized")
	}
}

func TestServerSetupDevicePreviewAndSystemMetricsEndpoints(t *testing.T) {
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-device/preview-id" {
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_id":"acme/runner/phone"}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	s := newTestServer(t)
	preview := httptest.NewRecorder()
	s.previewSetupDeviceID(preview, httptest.NewRequest(http.MethodPost, "/setup/device-id", strings.NewReader(`{"instance_url":"https://credimi.example","api_key":"key","organization":"acme","runner_id":"acme/runner","name":"Phone"}`)))
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "acme/runner/phone") {
		t.Fatalf("device preview = %d %s", preview.Code, preview.Body.String())
	}
	metrics := httptest.NewRecorder()
	s.systemMetrics(metrics, httptest.NewRequest(http.MethodGet, "/api/system-metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "interval_ms") {
		t.Fatalf("system metrics = %d %s", metrics.Code, metrics.Body.String())
	}
}

func TestServerSystemMetricsSupportsHourlyRange(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC()
	s.systemMonitor = &SystemMonitor{samples: []SystemMetrics{{Timestamp: now.Unix(), CPUPercent: 42}}}
	recorder := httptest.NewRecorder()
	s.systemMetrics(recorder, httptest.NewRequest(http.MethodGet, "/api/system-metrics?range=hourly", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"samples"`) {
		t.Fatalf("hourly system metrics = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestServerSetupValidationHandlers(t *testing.T) {
	s := newTestServer(t)
	for _, tt := range []struct {
		name    string
		handler http.HandlerFunc
		target  string
		body    string
		want    string
	}{
		{"organization invalid json", s.lookupSetupOrganization, "/setup/organization", "{", "invalid JSON"},
		{"organization missing fields", s.lookupSetupOrganization, "/setup/organization", `{}`, "required"},
		{"canonify invalid json", s.canonifySetupName, "/setup/canonify?name=Runner", "{", "invalid JSON"},
		{"canonify missing name", s.canonifySetupName, "/setup/canonify", `{"instance_url":"https://credimi.io","api_key":"key"}`, "name query parameter"},
		{"preview invalid json", s.previewSetupRunnerID, "/setup/runner-id", "{", "invalid JSON"},
		{"preview missing fields", s.previewSetupRunnerID, "/setup/runner-id", `{}`, "required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tt.target, strings.NewReader(tt.body))
			tt.handler(rec, req)
			if rec.Code < 400 || !strings.Contains(rec.Body.String(), tt.want) {
				t.Fatalf("code/body = %d %q, want %q", rec.Code, rec.Body.String(), tt.want)
			}
		})
	}
}

func TestServerDeviceHandlers(t *testing.T) {
	s := newTestServer(t)

	for _, form := range []url.Values{
		{"type": {"android_phone"}, "mode": {"usb"}},
		{"type": {"ios_simulator"}, "address": {"SIM-1"}},
		{"type": {"android_emulator"}},
		{"type": {"unknown"}},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/devices/connect", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		s.deviceConnect(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("HX-Trigger") == "" {
			t.Fatalf("deviceConnect(%v) = %d headers=%v body=%s", form, rec.Code, rec.Header(), rec.Body.String())
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /devices/{serial}/reconnect", s.deviceReconnect)
	mux.HandleFunc("POST /devices/{serial}/disconnect", s.deviceDisconnect)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/devices/ABC123/reconnect", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("deviceReconnect = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/devices/ABC123/disconnect", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("deviceDisconnect = %d %s", rec.Code, rec.Body.String())
	}
}
func TestServerSaveDevicesConfigPreviewsAndPersistsNewDevice(t *testing.T) {
	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"

	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/mobile-device/preview-id" && request.URL.Path != "/api/mobile-device" {
			return nil, errors.New("unexpected Credimi path: " + request.URL.Path)
		}
		if request.Header.Get("Credimi-Api-Key") != "user-key" {
			return nil, errors.New("missing Credimi API key")
		}
		body := `{"runner_id":"acme/runner","device_id":"/acme/runner/pixel"}`
		if request.URL.Path == "/api/mobile-device" {
			body = `{}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	form := url.Values{
		"name":        {"Pixel 8"},
		"description": {"USB test device"},
		"type":        {"android_phone"},
		"mode":        {"usb"},
		"serial":      {"usb-1"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveDevicesConfig(recorder, request)

	waitForQueuedOperation(t, s, recorder)
	updatedStore, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		t.Fatal(err)
	}
	config, err := updatedStore.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Devices) != 1 || config.Devices[0].ID != "acme/runner/pixel" || config.Devices[0].Serial != "usb-1" {
		t.Fatalf("persisted devices = %#v", config.Devices)
	}
}

func TestServerSaveDevicesConfigUpdatesOnlySelectedDevice(t *testing.T) {
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-device/preview-id" {
			return nil, fmt.Errorf("unexpected Credimi path: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_id":"acme/runner/renamed-one"}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	s := newTestServer(t)
	dir := filepath.Dir(s.cfg.Path())
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values{
		"CREDIMI_URL": "https://credimi.example", "CREDIMI_USER_API_KEY": "user-key", "CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_RUNNER_NAME": "runner", "CREDIMI_RUNNER_ORGANIZATION": "acme", "TEMPORAL_ADDRESS": "temporal.example:7233", "CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://runner.example",
	}, Devices: []dashboardruntime.DeviceRuntimeConfig{
		{ID: "acme/runner/one", Name: "One", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "one", Values: dashboardruntime.Values{}},
		{ID: "acme/runner/two", Name: "Two", Type: "android_phone", Mode: "wifi", Enabled: true, WiFiIP: "10.0.0.2", WiFiPort: "5555", Values: dashboardruntime.Values{}},
	}}); err != nil {
		t.Fatal(err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	form := url.Values{"device_id": {"acme/runner/one"}, "name": {"Renamed One"}, "type": {"android_phone"}, "mode": {"wifi"}, "wifi_ip": {"10.0.0.1"}, "wifi_port": {"5555"}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveDevicesConfig(recorder, request)
	waitForQueuedOperation(t, s, recorder)
	updated, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	config, err := updated.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Devices[0].ID != "acme/runner/renamed-one" || config.Devices[0].Name != "Renamed One" || config.Devices[0].Mode != "wifi" || config.Devices[0].Serial != "10.0.0.1:5555" || config.Devices[1].Name != "Two" || config.Devices[1].Serial != "10.0.0.2:5555" {
		t.Fatalf("devices = %#v", config.Devices)
	}

	form.Set("device_id", "acme/runner/missing")
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveDevicesConfig(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("missing update response = %d", recorder.Code)
	}
	failed, err := s.operations.Wait(context.Background(), s.operations.Current().ID)
	if err != nil || failed.Phase != controller.PhaseFailed || !strings.Contains(failed.Error, "device not found") {
		t.Fatalf("missing update operation = %#v err=%v", failed, err)
	}
}

func TestServerSaveDevicesConfigRenameConflictRequiresExplicitChoice(t *testing.T) {
	s := newTestServer(t)
	dir := filepath.Dir(s.cfg.Path())
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	host := dashboardruntime.Values{
		"CREDIMI_URL": "https://credimi.example", "CREDIMI_USER_API_KEY": "user-key", "CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_RUNNER_NAME": "runner", "CREDIMI_RUNNER_ORGANIZATION": "acme", "CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://runner.example",
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: host, Devices: []dashboardruntime.DeviceRuntimeConfig{{ID: "acme/runner/old", Name: "Old", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "usb-1", Values: dashboardruntime.Values{"SERIAL": "usb-1"}}}}); err != nil {
		t.Fatal(err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-device/preview-id" {
			return nil, fmt.Errorf("unexpected Credimi path: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_id":"acme/runner/new","existing_device_id":"acme/runner/other","conflict":true}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	form := url.Values{"device_id": {"acme/runner/old"}, "name": {"New"}, "type": {"android_phone"}, "mode": {"usb"}, "serial": {"usb-1"}}
	request := httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	err = s.saveDevicesConfigSync(request, nil)
	if err == nil || !strings.Contains(err.Error(), "choose create or update explicitly") {
		t.Fatalf("rename conflict = %v", err)
	}
	updated, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	current, err := updated.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if current.Devices[0].ID != "acme/runner/old" || current.Devices[0].Name != "Old" {
		t.Fatalf("conflicting rename changed local identity = %#v", current.Devices[0])
	}
}

func TestSaveRuntimeCandidateIgnoresStaleDashboardCache(t *testing.T) {
	s := newTestServer(t)
	dir := filepath.Dir(s.cfg.Path())
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	host := dashboardruntime.Values(s.cfg.Snapshot())
	devices := []dashboardruntime.DeviceRuntimeConfig{
		{ID: "acme/runner/one", Name: "One", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "usb-1", Values: dashboardruntime.Values{"SERIAL": "usb-1"}},
		{ID: "acme/runner/two", Name: "Two", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "usb-2", Values: dashboardruntime.Values{"SERIAL": "usb-2"}},
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: host, Devices: devices}); err != nil {
		t.Fatal(err)
	}
	baseline := dashboardruntime.Values(store.Snapshot())
	s.cfg.mu.Lock()
	s.cfg.values["CREDIMI_RUNNER_DESCRIPTION"] = "stale dashboard cache"
	s.cfg.mu.Unlock()
	candidate := dashboardruntime.RunnerRuntimeConfig{Host: host, Devices: devices[:1]}
	if err := s.saveRuntimeCandidate(baseline, store, candidate); err != nil {
		t.Fatalf("stale Dashboard cache blocked candidate: %v", err)
	}
	reloaded, err := store.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Devices) != 1 || reloaded.Devices[0].ID != "acme/runner/one" {
		t.Fatalf("saved devices = %#v", reloaded.Devices)
	}
}

func TestSaveRuntimeCandidateRejectsPersistedConcurrentChange(t *testing.T) {
	s := newTestServer(t)
	dir := filepath.Dir(s.cfg.Path())
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	host := dashboardruntime.Values(s.cfg.Snapshot())
	devices := []dashboardruntime.DeviceRuntimeConfig{
		{ID: "acme/runner/one", Name: "One", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "usb-1", Values: dashboardruntime.Values{"SERIAL": "usb-1"}},
		{ID: "acme/runner/two", Name: "Two", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "usb-2", Values: dashboardruntime.Values{"SERIAL": "usb-2"}},
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: host, Devices: devices}); err != nil {
		t.Fatal(err)
	}
	baseline := dashboardruntime.Values(store.Snapshot())
	changedHost := dashboardruntime.Values(host)
	changedHost["CREDIMI_RUNNER_DESCRIPTION"] = "concurrent change"
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: changedHost, Devices: devices}); err != nil {
		t.Fatal(err)
	}
	err = s.saveRuntimeCandidate(baseline, store, dashboardruntime.RunnerRuntimeConfig{Host: host, Devices: devices[:1]})
	if err == nil || !strings.Contains(err.Error(), "configuration changed while preparing") {
		t.Fatalf("concurrent change error = %v", err)
	}
	current, err := store.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Devices) != 2 || current.Host["CREDIMI_RUNNER_DESCRIPTION"] != "concurrent change" {
		t.Fatalf("concurrent configuration was overwritten: %#v", current)
	}
}

func TestSaveConfigPageIgnoresStaleDashboardCache(t *testing.T) {
	s := newTestServer(t)
	dir := filepath.Dir(s.cfg.Path())
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	values := dashboardruntime.Values(s.cfg.Snapshot())
	values["CREDIMI_DEVICE_COUNT"] = "1"
	values["CREDIMI_DEVICE_1_ID"] = "acme/runner/device"
	values["CREDIMI_DEVICE_1_NAME"] = "Device"
	values["CREDIMI_DEVICE_1_TYPE"] = "android_phone"
	values["CREDIMI_DEVICE_1_MODE"] = "no_device"
	values["CREDIMI_DEVICE_1_ENABLED"] = "true"
	values["CREDIMI_RUNNER_DESCRIPTION"] = "persisted"
	if err := store.Save(values); err != nil {
		t.Fatal(err)
	}
	s.cfg.mu.Lock()
	s.cfg.values["CREDIMI_RUNNER_DESCRIPTION"] = "stale cache"
	s.cfg.mu.Unlock()
	form := url.Values{}
	for key, value := range values {
		form.Set(key, value)
	}
	form.Set("CREDIMI_RUNNER_DESCRIPTION", "updated")
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := s.saveConfigPageSync(req, "config", func(string) {}); err != nil {
		t.Fatalf("stale cache blocked config save: %v", err)
	}
	reloaded, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Snapshot()["CREDIMI_RUNNER_DESCRIPTION"]; got != "updated" {
		t.Fatalf("persisted description = %q", got)
	}
	if got := s.cfg.Snapshot()["CREDIMI_RUNNER_DESCRIPTION"]; got != "updated" {
		t.Fatalf("dashboard cache description = %q", got)
	}
}

func TestServerSaveDevicesConfigCreatesPreviewedDeviceID(t *testing.T) {
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-device" {
			return nil, fmt.Errorf("unexpected Credimi path %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	s := newTestServer(t)
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values{
		"CREDIMI_URL": "https://credimi.example", "CREDIMI_USER_API_KEY": "user-key", "CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_RUNNER_NAME": "runner", "CREDIMI_RUNNER_ORGANIZATION": "acme", "CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://runner.example",
	}, Devices: []dashboardruntime.DeviceRuntimeConfig{{
		ID: "acme/runner/first-phone", Name: "First phone", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "usb-1", Values: dashboardruntime.Values{"SERIAL": "usb-1"},
	}}}); err != nil {
		t.Fatal(err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	form := url.Values{
		"CREDIMI_DEVICE_CONFLICT_ACTION": {"create"},
		"CREDIMI_DEVICE_ID":              {"acme/runner/second-phone"},
		"name":                           {"Second phone"},
		"type":                           {"android_phone"},
		"mode":                           {"usb"},
		"CREDIMI_RUNNER_SERIAL":          {"usb-2"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveDevicesConfig(recorder, request)
	waitForQueuedOperation(t, s, recorder)
	updatedStore, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		t.Fatal(err)
	}
	config, err := updatedStore.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Devices) != 2 || config.Devices[1].ID != "acme/runner/second-phone" || config.Devices[1].Serial != "usb-2" {
		t.Fatalf("created device inventory = %#v", config.Devices)
	}
}

func TestServerSaveDevicesConfigUsesCanonicalAliasesAndClearsFields(t *testing.T) {
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-device/preview-id" {
			return nil, fmt.Errorf("unexpected Credimi path: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_id":"acme/runner/usb-renamed"}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	s := newTestServer(t)
	dir := filepath.Dir(s.cfg.Path())
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	host := dashboardruntime.Values{
		"CREDIMI_URL": "https://credimi.example", "CREDIMI_USER_API_KEY": "user-key", "CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_RUNNER_NAME": "runner", "CREDIMI_RUNNER_ORGANIZATION": "acme", "CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://runner.example",
	}
	devices := []dashboardruntime.DeviceRuntimeConfig{
		{ID: "acme/runner/usb", Name: "USB", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "usb-old", Values: dashboardruntime.Values{"SERIAL": "usb-old"}},
		{ID: "acme/runner/wifi", Name: "Wi-Fi", Type: "android_phone", Mode: "wifi", Enabled: true, WiFiIP: "10.0.0.2", WiFiPort: "5555", Serial: "10.0.0.2:5555", Values: dashboardruntime.Values{"WIFI_IP": "10.0.0.2", "WIFI_PORT": "5555"}},
		{ID: "acme/runner/redroid", Name: "Redroid", Type: "redroid", Mode: "redroid", Enabled: true, WiFiIP: "10.0.0.3", WiFiPort: "5555", Serial: "10.0.0.3:5555", Values: dashboardruntime.Values{"WIFI_IP": "10.0.0.3", "WIFI_PORT": "5555"}},
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: host, Devices: devices}); err != nil {
		t.Fatal(err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)

	post := func(form url.Values) {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		s.saveDevicesConfig(recorder, request)
		waitForQueuedOperation(t, s, recorder)
	}

	post(url.Values{
		"CREDIMI_DEVICE_ID":          {"acme/runner/usb"},
		"CREDIMI_DEVICE_NAME":        {"USB renamed"},
		"CREDIMI_DEVICE_DESCRIPTION": {"updated description"},
		"CREDIMI_RUNNER_TYPE":        {"android_phone"},
		"CREDIMI_RUNNER_DEVICE_MODE": {"wifi"},
		"CREDIMI_RUNNER_WIFI_IP":     {"10.0.0.10"},
		"CREDIMI_RUNNER_WIFI_PORT":   {"5555"},
	})
	post(url.Values{
		"CREDIMI_DEVICE_ID":          {"acme/runner/usb-renamed"},
		"CREDIMI_RUNNER_TYPE":        {"android_phone"},
		"CREDIMI_RUNNER_DEVICE_MODE": {"usb"},
		"CREDIMI_RUNNER_SERIAL":      {"usb-new"},
	})
	post(url.Values{
		"CREDIMI_DEVICE_ID":          {"acme/runner/wifi"},
		"CREDIMI_RUNNER_TYPE":        {"android_phone"},
		"CREDIMI_RUNNER_DEVICE_MODE": {"wifi"},
		"CREDIMI_RUNNER_WIFI_IP":     {"10.0.0.20"},
		"CREDIMI_RUNNER_WIFI_PORT":   {"5566"},
	})
	post(url.Values{
		"CREDIMI_DEVICE_ID":          {"acme/runner/redroid"},
		"CREDIMI_RUNNER_TYPE":        {"redroid"},
		"CREDIMI_RUNNER_DEVICE_MODE": {"redroid"},
		"CREDIMI_RUNNER_WIFI_IP":     {"10.0.0.30"},
		"CREDIMI_RUNNER_WIFI_PORT":   {"5566"},
	})
	post(url.Values{
		"CREDIMI_DEVICE_ID":          {"acme/runner/redroid"},
		"CREDIMI_DEVICE_DESCRIPTION": {""},
		"CREDIMI_RUNNER_TYPE":        {"redroid"},
		"CREDIMI_RUNNER_DEVICE_MODE": {"redroid"},
		"CREDIMI_RUNNER_WIFI_IP":     {"10.0.0.30"},
		"CREDIMI_RUNNER_WIFI_PORT":   {"5566"},
	})

	updated, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	config, err := updated.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Devices[0]; got.Name != "USB renamed" || got.Description != "updated description" || got.Mode != "usb" || got.Serial != "usb-new" || got.WiFiIP != "" || got.WiFiPort != "" {
		t.Fatalf("USB device after transitions = %#v", got)
	}
	if got := config.Devices[1]; got.WiFiIP != "10.0.0.20" || got.WiFiPort != "5566" || got.Serial != "10.0.0.20:5566" {
		t.Fatalf("Wi-Fi device after update = %#v", got)
	}
	if got := config.Devices[2]; got.Description != "" || got.WiFiIP != "10.0.0.30" || got.WiFiPort != "5566" || got.Serial != "10.0.0.30:5566" {
		t.Fatalf("Redroid device after update = %#v", got)
	}
}

func TestServerDeviceEnableAndRemovePersistIndexedInventory(t *testing.T) {
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	s := newTestServer(t)
	dir := filepath.Dir(s.cfg.Path())
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	config := dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values{
		"CREDIMI_URL": "https://credimi.example", "CREDIMI_USER_API_KEY": "user-key", "CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_RUNNER_NAME": "runner", "CREDIMI_RUNNER_ORGANIZATION": "acme", "TEMPORAL_ADDRESS": "temporal.example:7233", "CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://runner.example",
	}, Devices: []dashboardruntime.DeviceRuntimeConfig{
		{ID: "acme/runner/one", Name: "One", Type: "android_phone", Mode: "usb", Serial: "usb-1", Enabled: true, Values: dashboardruntime.Values{"SERIAL": "usb-1"}},
		{ID: "acme/runner/two", Name: "Two", Type: "android_phone", Mode: "usb", Serial: "usb-2", Enabled: true, Values: dashboardruntime.Values{"SERIAL": "usb-2"}},
	}}
	if err := store.SaveRuntimeConfig(config); err != nil {
		t.Fatal(err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)

	enable := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/devices/disable", strings.NewReader(url.Values{"device_id": {"acme/runner/one"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.deviceDisable(enable, req)
	waitForQueuedOperation(t, s, enable)
	stored, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := stored.RuntimeConfig()
	if err != nil || updated.Devices[0].Enabled {
		t.Fatalf("updated inventory = %#v, %v", updated, err)
	}

	remove := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/devices/remove", strings.NewReader(url.Values{"device_id": {"acme/runner/one"}, "confirm": {"true"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.deviceRemove(remove, req)
	waitForQueuedOperation(t, s, remove)
	stored, err = dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	updated, err = stored.RuntimeConfig()
	if err != nil || len(updated.Devices) != 1 || updated.Devices[0].Index != 1 || updated.Devices[0].ID != "acme/runner/two" {
		t.Fatalf("reindexed inventory = %#v, %v", updated, err)
	}
}

func TestServerFinishSetupAcceptsValidHTMXSubmission(t *testing.T) {
	s := newTestServer(t)
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		switch req.URL.Path {
		case "/api/mobile-device/preview-id":
			body = `{"device_id":"acme/runner/pixel"}`
		case "/api/mobile-runner", "/api/mobile-device", "/api/mobile-device/reconcile":
		default:
			return nil, errors.New("unexpected path: " + req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	form := url.Values{
		"CREDIMI_URL":                 {"https://credimi.example"},
		"CREDIMI_USER_API_KEY":        {"user-key"},
		"CREDIMI_RUNNER_ID":           {"acme/runner"},
		"CREDIMI_RUNNER_ORGANIZATION": {"acme"},
		"CREDIMI_SERVICE_MODE":        {"manual"},
		"RUNNER_PUBLIC_URL":           {"https://runner.example"},
		"SETUP_DEVICE_COUNT":          {"1"},
		"SETUP_DEVICE_1_NAME":         {"Pixel"},
		"SETUP_DEVICE_1_TYPE":         {"redroid"},
		"SETUP_DEVICE_1_MODE":         {"no_device"},
		"SETUP_DEVICE_1_WIFI_IP":      {"192.0.2.10"},
		"SETUP_DEVICE_1_SERIAL":       {"redroid:5555"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("HX-Request", "true")
	s.finishSetup(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("setup response = %d redirect=%q body=%s", recorder.Code, recorder.Header().Get("HX-Redirect"), recorder.Body.String())
	}
	waitForQueuedOperation(t, s, recorder)
	if !s.cfg.Exists() || s.cfg.Get("CREDIMI_RUNNER_ID") != "acme/runner" {
		t.Fatalf("setup was not persisted: exists=%t values=%#v", s.cfg.Exists(), s.cfg.Snapshot())
	}
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig, err := store.RuntimeConfig()
	if err != nil || len(runtimeConfig.Devices) != 1 || runtimeConfig.Devices[0].ID != "acme/runner/pixel" {
		t.Fatalf("setup persisted incomplete inventory: %#v err=%v", runtimeConfig, err)
	}
	if startup := s.startupSnapshot(); startup.Phase != StartupReady {
		t.Fatalf("setup startup phase = %q: %s", startup.Phase, startup.Message)
	}
}
