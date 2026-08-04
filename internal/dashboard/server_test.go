package dashboard

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/forkbombeu/credimi-runner/internal/controller"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/maintenance"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeManager struct {
	mu               sync.Mutex
	startCalls       int
	stopCalls        int
	restartCalls     int
	updateImageCalls int
	logLines         []dashboardruntime.LogLine
	logLinesSince    []dashboardruntime.LogLine
	status           dashboardruntime.RuntimeStatus
	startErr         error
	stopErr          error
	restartErr       error
	updateImageErr   error
	logTail          int
	upgradeBlock     chan struct{}
}

func (f *fakeManager) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	if f.startErr != nil {
		return f.startErr
	}
	f.status.RunnerRunning = true
	return nil
}
func (f *fakeManager) StartWithProgress(ctx context.Context, progress func(string)) error {
	if progress != nil {
		progress("Pulling Docker images.")
		progress("runner Downloading 128MB")
	}
	return f.Start(ctx)
}
func (f *fakeManager) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	if f.stopErr != nil {
		return f.stopErr
	}
	f.status.RunnerRunning = false
	f.status.PublicURL = ""
	return nil
}
func (f *fakeManager) Restart(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartCalls++
	if f.restartErr == nil {
		f.status.LastStartedAt = time.Now()
	}
	return f.restartErr
}
func (f *fakeManager) UpdateImage(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateImageCalls++
	return f.updateImageErr
}
func (f *fakeManager) UpgradeRunnerImage(_ context.Context, progress func(string)) error {
	f.mu.Lock()
	f.updateImageCalls++
	block := f.upgradeBlock
	err := f.updateImageErr
	f.mu.Unlock()
	if progress != nil {
		progress("Stopping the runner and Docker services.")
		progress("Downloading the latest runner image.")
	}
	if block != nil {
		<-block
	}
	return err
}
func (f *fakeManager) Configure(values dashboardruntime.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status.Configured = strings.TrimSpace(values["CREDIMI_RUNNER_ID"]) != ""
}
func (f *fakeManager) SetPublicURL(publicURL string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status.PublicURL = publicURL
}
func (f *fakeManager) Status(context.Context) dashboardruntime.RuntimeStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}
func (f *fakeManager) Logs(_ context.Context, tail int) ([]dashboardruntime.LogLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logTail = tail
	if tail > 0 && f.logLinesSince != nil {
		return f.logLinesSince, nil
	}
	return f.logLines, nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	cfg := &Config{path: t.TempDir() + "/.env", values: map[string]string{}}
	for k, v := range Defaults {
		cfg.values[k] = v
	}
	render, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(cfg, t.TempDir(), render, func() dashboardruntime.RuntimeStatus { return dashboardruntime.RuntimeStatus{} })
	hub.snap = Snapshot{
		Services: []Service{{ID: "runner", Name: "runner", Status: Online}},
		Devices:  []Device{{Serial: "device-1", Name: "Pixel 8", Type: "android_phone", Mode: "usb", Status: Online}},
	}
	hub.workers = []Worker{{ID: "runner-mr", Env: "runner", Status: Online}}
	return &Server{
		cfg:        cfg,
		hub:        hub,
		render:     render,
		composeDir: t.TempDir(),
		ctx:        context.Background(),
		authToken:  "token",
		manager: &fakeManager{logLines: []dashboardruntime.LogLine{
			{Message: "INF quick tunnel ready at https://runner.example.trycloudflare.com"},
		}},
		runnerReady:        func(context.Context, map[string]string) error { return nil },
		lookupPath:         func(string) (string, error) { return "/tmp/fake-bin", nil },
		statPath:           func(string) (os.FileInfo, error) { return fakeFileInfo("ok"), nil },
		maintenanceChecked: true,
		maintenanceChecker: func(context.Context, string, time.Time, string) maintenance.Status { return maintenance.Status{} },
		downloadBinary:     func(context.Context, *http.Client, string, func(string)) error { return nil },
		restartDashboard:   func(string) error { return nil },
	}
}

func TestNewHandlerWithManagerWrapper(t *testing.T) {
	handler, cancel, err := NewHandlerWithManager(t.TempDir(), &fakeManager{})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if handler == nil {
		t.Fatal("handler is nil")
	}
}

func TestDashboardHandlerConstructorsAndConfigPreview(t *testing.T) {
	for _, build := range []func() (http.Handler, context.CancelFunc, error){
		func() (http.Handler, context.CancelFunc, error) { return NewHandler(t.TempDir()) },
		func() (http.Handler, context.CancelFunc, error) {
			return NewHandlerWithManagerContextAndIdentity(context.Background(), t.TempDir(), &fakeManager{}, "controller", "token", "fingerprint")
		},
		func() (http.Handler, context.CancelFunc, error) {
			return NewHandlerWithManagerContextAndIdentityAndCoordinator(context.Background(), t.TempDir(), &fakeManager{}, "controller", "token", "fingerprint", controller.NewCoordinator(context.Background()))
		},
	} {
		handler, cancel, err := build()
		if err != nil || handler == nil {
			t.Fatalf("handler=%v err=%v", handler, err)
		}
		cancel()
	}
	s := newTestServer(t)
	form := url.Values{"CREDIMI_RUNNER_ID": {"acme/runner"}, "CREDIMI_DEVICE_COUNT": {"1"}, "CREDIMI_DEVICE_1_ID": {"acme/runner/device"}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/config/normalize", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.normalizeConfigPreview(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "acme/runner") {
		t.Fatalf("preview=%d %s", recorder.Code, recorder.Body.String())
	}
}

func TestControllerRuntimeAPIQueuesAndSerializesOperations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("CREDIMI_RUNNER_ID=acme/runner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := &fakeManager{}
	handler, cancel, err := NewHandlerWithManagerContext(context.Background(), dir, manager)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/api/controller/runtime/start", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("queue status = %d, body=%s", response.Code, response.Body.String())
	}
	var queued map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil || queued["id"] == nil {
		t.Fatalf("queued operation = %s", response.Body.String())
	}
	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, httptest.NewRequest(http.MethodPost, "/api/controller/runtime/start", nil))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting queue status = %d", conflict.Code)
	}
}

func TestControllerAPIsExposeLifecycleOperations(t *testing.T) {
	s := newTestServer(t)
	s.operations = controller.NewCoordinator(context.Background())
	status := httptest.NewRecorder()
	s.controllerStatus(status, httptest.NewRequest(http.MethodGet, "/api/controller/status", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), "runtime") {
		t.Fatalf("controller status = %d %s", status.Code, status.Body.String())
	}

	current := httptest.NewRecorder()
	s.controllerOperationCurrent(current, httptest.NewRequest(http.MethodGet, "/api/controller/operations/current", nil))
	if current.Code != http.StatusOK {
		t.Fatalf("controller current = %d", current.Code)
	}

	missing := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodGet, "/api/controller/operations/missing", nil)
	missingRequest.SetPathValue("id", "missing")
	s.controllerOperation(missing, missingRequest)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing operation = %d", missing.Code)
	}

	unavailable := httptest.NewRecorder()
	s.manager = nil
	action := httptest.NewRequest(http.MethodPost, "/api/controller/runtime/stop", nil)
	action.SetPathValue("action", "stop")
	s.controllerRuntimeAction(unavailable, action)
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable action = %d", unavailable.Code)
	}

	s.manager = &fakeManager{}
	queued := httptest.NewRecorder()
	action = httptest.NewRequest(http.MethodPost, "/api/controller/runtime/stop", nil)
	action.SetPathValue("action", "stop")
	s.controllerRuntimeAction(queued, action)
	if queued.Code != http.StatusAccepted {
		t.Fatalf("queued action = %d %s", queued.Code, queued.Body.String())
	}
	operation := s.operations.Current()
	if _, err := s.operations.Wait(context.Background(), operation.ID); err != nil {
		t.Fatal(err)
	}
	got := httptest.NewRecorder()
	getRequest := httptest.NewRequest(http.MethodGet, "/api/controller/operations/"+operation.ID, nil)
	getRequest.SetPathValue("id", operation.ID)
	s.controllerOperation(got, getRequest)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), operation.ID) {
		t.Fatalf("operation lookup = %d %s", got.Code, got.Body.String())
	}

}

func TestControllerIdentityRuntimeLogsAndStartupStatus(t *testing.T) {
	s := newTestServer(t)
	s.controllerID = "controller-1"
	s.controllerFingerprint = "fingerprint"
	s.controllerIdentityToken = "identity-token"
	unauthorized := httptest.NewRecorder()
	s.controllerIdentity(unauthorized, httptest.NewRequest(http.MethodGet, "/internal/controller/identity", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("identity without token = %d", unauthorized.Code)
	}
	identity := httptest.NewRecorder()
	identityRequest := httptest.NewRequest(http.MethodGet, "/internal/controller/identity", nil)
	identityRequest.Header.Set("X-Credimi-Controller-Token", "identity-token")
	s.controllerIdentity(identity, identityRequest)
	if identity.Code != http.StatusOK || !strings.Contains(identity.Body.String(), "controller-1") {
		t.Fatalf("identity = %d %s", identity.Code, identity.Body.String())
	}

	s.manager = nil
	logs := httptest.NewRecorder()
	s.runtimeLogs(logs, httptest.NewRequest(http.MethodGet, "/runtime/logs", nil))
	if logs.Code != http.StatusOK || !strings.Contains(logs.Body.String(), "lines") {
		t.Fatalf("logs without manager = %d %s", logs.Code, logs.Body.String())
	}
	s.manager = &fakeManager{logLines: []dashboardruntime.LogLine{{Message: " line one "}, {Message: ""}}}
	logs = httptest.NewRecorder()
	s.runtimeLogs(logs, httptest.NewRequest(http.MethodGet, "/runtime/logs", nil))
	if !strings.Contains(logs.Body.String(), "line one") {
		t.Fatalf("logs with manager = %s", logs.Body.String())
	}

	parent, stop := context.WithCancel(context.Background())
	s.operations = controller.NewCoordinator(parent)
	started := make(chan struct{})
	op, err := s.operations.Submit(controller.OperationRuntimeStart, func(ctx context.Context, _ func(controller.Progress)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	startup := httptest.NewRecorder()
	s.startupStatus(startup, httptest.NewRequest(http.MethodGet, "/startup/status", nil))
	if !strings.Contains(startup.Body.String(), string(StartupStarting)) {
		t.Fatalf("running startup status = %s", startup.Body.String())
	}
	stop()
	if _, err := s.operations.Wait(context.Background(), op.ID); err != nil {
		t.Fatal(err)
	}
	startup = httptest.NewRecorder()
	s.startupStatus(startup, httptest.NewRequest(http.MethodGet, "/startup/status", nil))
	if !strings.Contains(startup.Body.String(), string(StartupNeedsAttention)) {
		t.Fatalf("cancelled startup status = %s", startup.Body.String())
	}
}

func TestApplyModeAndSaveMessageHelpers(t *testing.T) {
	for input, want := range map[string]string{
		"quick":              "auto",
		"direct":             "manual",
		"named":              "cloudflare-managed",
		"auto":               "auto",
		"manual":             "manual",
		"cloudflare-managed": "cloudflare-managed",
		"unexpected":         "auto",
	} {
		if got := normalizedApplyServiceMode(input); got != want {
			t.Fatalf("normalizedApplyServiceMode(%q) = %q, want %q", input, got, want)
		}
	}
	if got := saveSuccessMessage(applyOutcome{Restarted: true}); got != "Runner restarted with the new configuration." {
		t.Fatalf("restart message = %q", got)
	}
	if got := saveSuccessMessage(applyOutcome{}); got != "Configuration updated." {
		t.Fatalf("save message = %q", got)
	}
}

func formValuesFromConfig(cfg *Config) url.Values {
	form := url.Values{}
	for _, field := range Registry {
		value := cfg.values[field.Key]
		if field.Type == TypeBool {
			if value == "true" {
				form.Set(field.Key, "on")
			}
			continue
		}
		form.Set(field.Key, value)
	}
	return form
}

type fakeFileInfo string

func (f fakeFileInfo) Name() string       { return string(f) }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestNewHandlerAndRoutes(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	h, cancel, err := NewHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/workers", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("workers route = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNewHandlerAppliesDashboardTokenAuth(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DASHBOARD_TOKEN=secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, cancel, err := NewHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config/raw", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("config/raw without token = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config/raw?token=secret-token", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("config/raw with token = %d", rec.Code)
	}
}

func TestServerAuth(t *testing.T) {
	s := newTestServer(t)
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

	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	if err := s.cfg.write(); err != nil {
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

func TestServerSaveDevicesConfigAddsIndexedDevice(t *testing.T) {
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-device/preview-id" && req.URL.Path != "/api/mobile-device" {
			return nil, errors.New("unexpected path: " + req.URL.Path)
		}
		body := `{"device_id":"acme/runner/pixel"}`
		if req.URL.Path == "/api/mobile-device" {
			body = `{}`
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })

	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(url.Values{
		"name": {"Pixel"}, "type": {"android_phone"}, "mode": {"usb"}, "serial": {"usb-1"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveDevicesConfig(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/devices" {
		t.Fatalf("save device = %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.RuntimeConfig()
	if err != nil || len(config.Devices) != 1 || config.Devices[0].ID != "acme/runner/pixel" || config.Devices[0].Serial != "usb-1" {
		t.Fatalf("saved config = %#v, %v", config, err)
	}
}

func TestApplyDeviceDefaultsAndRegistrationRequirements(t *testing.T) {
	emulator := dashboardruntime.DeviceRuntimeConfig{Type: "android_emulator", Mode: "emulator"}
	applyDeviceDefaults(&emulator)
	if emulator.Values["RUNNER_IMAGE"] == "" || emulator.Values["BASE_NAME"] != "credimi" || emulator.Values["GOLDEN_PATH"] == "" {
		t.Fatalf("emulator defaults = %#v", emulator.Values)
	}
	redroid := dashboardruntime.DeviceRuntimeConfig{Type: "redroid", Mode: "no_device", Values: dashboardruntime.Values{"RUNNER_IMAGE": "custom:local"}}
	applyDeviceDefaults(&redroid)
	if redroid.Values["RUNNER_IMAGE"] != "custom:local" || redroid.Values["WIFI_PORT"] != "5555" || redroid.Values["REDROID_DATA_DIR"] == "" {
		t.Fatalf("redroid defaults = %#v", redroid.Values)
	}
	s := newTestServer(t)
	if err := s.registerConfiguredDevice(context.Background(), dashboardruntime.Values{}, dashboardruntime.DeviceRuntimeConfig{Name: "Pixel", Type: "android_phone", Mode: "usb"}); err == nil || !strings.Contains(err.Error(), "Credimi URL") {
		t.Fatalf("missing credentials error = %v", err)
	}
}

func TestServerConfigDiffAndHelpers(t *testing.T) {
	s := newTestServer(t)
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(s.cfg.values), runtimeGOOS())
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.values = map[string]string(normalized)
	form := formValuesFromConfig(s.cfg)
	form.Set("CREDIMI_RUNNER_DESCRIPTION", "updated description")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config/diff", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.configDiff(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "credimi_update_required") || !strings.Contains(rec.Body.String(), `"confirm_required":false`) {
		t.Fatalf("configDiff = %d %s", rec.Code, rec.Body.String())
	}
	if got := describeDiffImpact(dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplySavedOnly}}); got != "" {
		t.Fatalf("describeDiffImpact = %q", got)
	}
	if got := describeDiffImpact(dashboardruntime.ConfigDiff{}); got != "" {
		t.Fatalf("default describeDiffImpact = %q", got)
	}
	for _, diff := range []dashboardruntime.ConfigDiff{
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyComposeRecreate, dashboardruntime.ApplyCredimiUpdateRequired}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired, dashboardruntime.ApplyCredimiUpdateRequired}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyComposeRecreate}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired}},
	} {
		if got := describeDiffImpact(diff); got == "" {
			t.Fatalf("describeDiffImpact(%#v) returned empty string", diff)
		}
	}
	if got := describeDiffImpact(dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyCredimiUpdateRequired}}); got != "" {
		t.Fatalf("Credimi-only diff should not ask for confirmation: %q", got)
	}
}

func TestServerConfigDiffRunnerTypeChangeRequiresApply(t *testing.T) {
	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	s.cfg.values["RUNNER_IMAGE"] = defaultPhoneImage

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config/diff", strings.NewReader(url.Values{
		"CREDIMI_URL":         {"https://credimi.example"},
		"CREDIMI_RUNNER_ID":   {"acme/runner"},
		"CREDIMI_RUNNER_NAME": {"runner"},
		"CREDIMI_RUNNER_TYPE": {"android_emulator"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.configDiff(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("configDiff runner type change = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "No restart") {
		t.Fatalf("runner type change should not be saved-only: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"confirm_required":true`) {
		t.Fatalf("runner type change should require confirmation: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "runner record in Credimi") {
		t.Fatalf("runner type change should require Credimi update: %s", rec.Body.String())
	}
}

func TestServerConfigDiffManualPublicURLOnlyUpdatesCredimi(t *testing.T) {
	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://old.example"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(s.cfg.values), runtimeGOOS())
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.values = map[string]string(normalized)

	form := formValuesFromConfig(s.cfg)
	form.Set("RUNNER_PUBLIC_URL", "https://manual.example")
	form.Set("RUNNER_PUBLIC_PORT", "8443")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config/diff", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.configDiff(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("configDiff manual URL change = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "credimi_update_required") {
		t.Fatalf("manual URL change should update Credimi: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"confirm_required":false`) {
		t.Fatalf("manual URL change should not require restart confirmation: %s", rec.Body.String())
	}
}

func TestServerConfigDiffIgnoresDirectRunnerIDChange(t *testing.T) {
	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(s.cfg.values), runtimeGOOS())
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.values = map[string]string(normalized)

	form := formValuesFromConfig(s.cfg)
	form.Set("CREDIMI_RUNNER_ID", "evil/id")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config/diff", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.configDiff(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("configDiff direct ID change = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "saved_only") || !strings.Contains(rec.Body.String(), `"confirm_required":false`) {
		t.Fatalf("direct runner ID edits should be ignored: %s", rec.Body.String())
	}
}

func TestServerConfigDiffNameChangeDerivesRunnerID(t *testing.T) {
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-runner/preview-id" {
			return nil, errors.New("unexpected path: " + req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"organization":"acme","runner_id":"acme/renamed-runner"}`)),
		}, nil
	})
	defer func() { http.DefaultTransport = transport }()

	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(s.cfg.values), runtimeGOOS())
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.values = map[string]string(normalized)

	form := formValuesFromConfig(s.cfg)
	form.Set("CREDIMI_RUNNER_NAME", "Renamed Runner")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config/diff", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.configDiff(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("configDiff name change = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "restart_required") || !strings.Contains(rec.Body.String(), "credimi_update_required") || !strings.Contains(rec.Body.String(), `"confirm_required":true`) {
		t.Fatalf("name change should require restart and Credimi update: %s", rec.Body.String())
	}
}

func TestServerConfigDiffRejectsUserScopedOrganizationChange(t *testing.T) {
	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_INTERNAL_ADMIN_KEY"] = ""
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(s.cfg.values), runtimeGOOS())
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.values = map[string]string(normalized)

	form := formValuesFromConfig(s.cfg)
	form.Set("CREDIMI_RUNNER_ORGANIZATION", "other")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config/diff", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.configDiff(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("configDiff user org change = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "organization cannot be changed for user-scoped runners") {
		t.Fatalf("configDiff user org change should explain rejection: %s", rec.Body.String())
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

func TestServerSaveOverviewPublishedConfig(t *testing.T) {
	transport := http.DefaultTransport
	var payload dashboardruntime.RegisterRunnerRequest
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-runner" {
			return nil, errors.New("unexpected path: " + req.URL.Path)
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})
	defer func() { http.DefaultTransport = transport }()

	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	s.cfg.values["CREDIMI_RUNNER_PUBLISHED"] = "false"

	form := url.Values{
		"CREDIMI_RUNNER_PUBLISHED": {"on"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/overview/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveOverviewConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("saveOverviewConfig = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Runner publication") || strings.Contains(rec.Body.String(), "API &amp; Config") {
		t.Fatalf("saveOverviewConfig should render overview, got %s", rec.Body.String())
	}
	if payload.Published == nil || !*payload.Published {
		t.Fatalf("published payload = %#v", payload.Published)
	}
	if got := s.cfg.Get("CREDIMI_RUNNER_PUBLISHED"); got != "true" {
		t.Fatalf("stored CREDIMI_RUNNER_PUBLISHED = %q", got)
	}
}

func TestServerRuntimeActionVariants(t *testing.T) {
	s := newTestServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer api.Close()
	s.cfg.values["CREDIMI_URL"] = api.URL
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	s.runnerReady = func(context.Context, map[string]string) error { return nil }

	for _, target := range []struct {
		path string
		fn   http.HandlerFunc
	}{
		{"/runtime/stop", s.runtimeStop},
		{"/runtime/restart", s.runtimeRestart},
	} {
		rec := httptest.NewRecorder()
		target.fn(rec, httptest.NewRequest(http.MethodPost, target.path, nil))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s = %d %s", target.path, rec.Code, rec.Body.String())
		}
		op := s.operations.Current()
		if _, err := s.operations.Wait(context.Background(), op.ID); err != nil {
			t.Fatalf("%s operation: %v", target.path, err)
		}
	}
}

func TestServerMaintenanceUpgradeRunsInBackgroundAndPublishesLogs(t *testing.T) {
	registered := make(chan string, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		registered <- string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	s := newTestServer(t)
	manager := s.manager.(*fakeManager)
	manager.status.PublicURL = ""
	manager.logLines = []dashboardruntime.LogLine{{Message: "INF quick tunnel ready at https://fresh.example.trycloudflare.com"}}
	s.cfg.values["CREDIMI_URL"] = api.URL
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	s.cfg.values["CREDIMI_RUNNER_BACKEND"] = "container"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "auto"
	s.maintenance.Image.UpdateAvailable = true
	recorder := httptest.NewRecorder()
	s.maintenanceUpgrade(recorder, httptest.NewRequest(http.MethodPost, "/maintenance/upgrade", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("maintenanceUpgrade = %d %s", recorder.Code, recorder.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for s.startupSnapshot().running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	startup := s.startupSnapshot()
	if startup.Phase != StartupReady || !strings.Contains(strings.Join(startup.Logs, "\n"), "Downloading the latest runner image") {
		t.Fatalf("startup = %#v", startup)
	}
	select {
	case body := <-registered:
		if !strings.Contains(body, "https://fresh.example.trycloudflare.com") {
			t.Fatalf("registration body = %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("Credimi registration was not updated")
	}
	if manager.status.PublicURL != "https://fresh.example.trycloudflare.com" {
		t.Fatalf("manager public URL = %q", manager.status.PublicURL)
	}
}

func TestServerMaintenanceUpgradeRejectsConcurrentJobAndReportsFailure(t *testing.T) {
	s := newTestServer(t)
	manager := s.manager.(*fakeManager)
	manager.upgradeBlock = make(chan struct{})
	manager.updateImageErr = errors.New("pull failed")
	s.maintenance = maintenance.Status{Image: maintenance.Component{UpdateAvailable: true}}

	first := httptest.NewRecorder()
	s.maintenanceUpgrade(first, httptest.NewRequest(http.MethodPost, "/maintenance/upgrade", nil))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first upgrade = %d", first.Code)
	}
	second := httptest.NewRecorder()
	s.maintenanceUpgrade(second, httptest.NewRequest(http.MethodPost, "/maintenance/upgrade", nil))
	if second.Code != http.StatusConflict {
		t.Fatalf("second upgrade = %d %s", second.Code, second.Body.String())
	}
	close(manager.upgradeBlock)
	deadline := time.Now().Add(time.Second)
	for s.startupSnapshot().running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	startup := s.startupSnapshot()
	if startup.Phase != StartupNeedsAttention || !strings.Contains(startup.Message, "pull failed") {
		t.Fatalf("startup = %#v", startup)
	}
}

func TestServerMaintenanceCheckRefreshesMetadata(t *testing.T) {
	s := newTestServer(t)
	s.maintenanceChecked = false
	calls := 0
	s.maintenanceChecker = func(context.Context, string, time.Time, string) maintenance.Status {
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

func TestServerMaintenanceCheckSkipsLocalRunnerImage(t *testing.T) {
	s := newTestServer(t)
	s.maintenanceChecked = false
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_DEVICE_COUNT"] = "1"
	s.cfg.values["CREDIMI_DEVICE_1_ID"] = "acme/runner/phone"
	s.cfg.values["CREDIMI_DEVICE_1_RUNNER_IMAGE"] = "local:latest"
	s.cfg.values["CREDIMI_DEVICE_1_RUNNER_IMAGE_PULL_POLICY"] = "never"
	checkedImage := "not-called"
	s.maintenanceChecker = func(_ context.Context, _ string, _ time.Time, image string) maintenance.Status {
		checkedImage = image
		return maintenance.Status{Runner: maintenance.Component{LatestVersion: "v2"}}
	}
	s.ensureMaintenanceChecked(context.Background(), true)
	if checkedImage != "" {
		t.Fatalf("checked image = %q", checkedImage)
	}
	if s.maintenance.Error != "" || s.maintenance.Runner.LatestVersion != "v2" {
		t.Fatalf("maintenance status = %#v", s.maintenance)
	}
}

func TestServerMaintenanceUpgradeStagesBinaryAndSchedulesRestart(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer api.Close()
	s := newTestServer(t)
	s.binaryPath = "/installed/credimi-runner"
	s.maintenance = maintenance.Status{Runner: maintenance.Component{UpdateAvailable: true}}
	s.cfg.values["CREDIMI_URL"] = api.URL
	s.cfg.values["CREDIMI_USER_API_KEY"] = "key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	var downloaded, restarted string
	s.downloadBinary = func(_ context.Context, _ *http.Client, target string, progress func(string)) error {
		downloaded = target
		progress("binary staged")
		return nil
	}
	s.restartDashboard = func(staged string) error { restarted = staged; return nil }
	recorder := httptest.NewRecorder()
	s.maintenanceUpgrade(recorder, httptest.NewRequest(http.MethodPost, "/maintenance/upgrade", nil))
	op := s.operations.Current()
	if _, err := s.operations.Wait(context.Background(), op.ID); err != nil {
		t.Fatalf("maintenance upgrade operation: %v", err)
	}
	if downloaded != "/installed/credimi-runner.upgrade" || restarted != downloaded {
		t.Fatalf("downloaded=%q restarted=%q startup=%#v", downloaded, restarted, s.startupSnapshot())
	}
}

func TestScheduleDashboardRestartUsesCurrentBinaryAsHelper(t *testing.T) {
	originalExecutable, originalStart, originalTerminate := dashboardExecutable, startDashboardRestartHelper, terminateDashboardAfter
	t.Cleanup(func() {
		dashboardExecutable, startDashboardRestartHelper, terminateDashboardAfter = originalExecutable, originalStart, originalTerminate
	})
	dashboardExecutable = func() (string, error) { return "/installed/credimi-runner", nil }
	var helper string
	var args []string
	startDashboardRestartHelper = func(name string, values ...string) error {
		helper, args = name, append([]string(nil), values...)
		return nil
	}
	terminated := false
	terminateDashboardAfter = func(time.Duration, int) { terminated = true }
	if err := scheduleDashboardRestart("/installed/credimi-runner.upgrade"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if helper != "/installed/credimi-runner" || !strings.Contains(joined, "--staged /installed/credimi-runner.upgrade") || !terminated {
		t.Fatalf("helper=%q args=%q terminated=%v", helper, joined, terminated)
	}
}

func TestScheduleDashboardRestartReportsHelperErrors(t *testing.T) {
	originalExecutable, originalStart := dashboardExecutable, startDashboardRestartHelper
	t.Cleanup(func() { dashboardExecutable, startDashboardRestartHelper = originalExecutable, originalStart })
	dashboardExecutable = func() (string, error) { return "", errors.New("executable failed") }
	if err := scheduleDashboardRestart("staged"); err == nil || !strings.Contains(err.Error(), "executable failed") {
		t.Fatalf("error = %v", err)
	}
	dashboardExecutable = func() (string, error) { return "/runner", nil }
	startDashboardRestartHelper = func(string, ...string) error { return errors.New("start failed") }
	if err := scheduleDashboardRestart("staged"); err == nil || !strings.Contains(err.Error(), "start failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestServerRuntimeRegisterAndActionError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mobile-runner" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()

	s := newTestServer(t)
	manager := s.manager.(*fakeManager)
	s.cfg.values["CREDIMI_URL"] = api.URL
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"

	rec := httptest.NewRecorder()
	s.runtimeRegister(rec, httptest.NewRequest(http.MethodPost, "/runtime/register", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("runtimeRegister = %d body=%s publicURL=%q", rec.Code, rec.Body.String(), manager.status.PublicURL)
	}
	op := s.operations.Current()
	if _, err := s.operations.Wait(context.Background(), op.ID); err != nil || manager.status.PublicURL != "https://runner.example" {
		t.Fatalf("runtimeRegister operation=%#v err=%v publicURL=%q", op, err, manager.status.PublicURL)
	}

	manager.stopErr = errors.New("stop failed")
	rec = httptest.NewRecorder()
	s.runtimeStop(rec, httptest.NewRequest(http.MethodPost, "/runtime/stop", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("runtimeStop = %d %s", rec.Code, rec.Body.String())
	}
	op = s.operations.Current()
	result, err := s.operations.Wait(context.Background(), op.ID)
	if err != nil || result.Phase != controller.PhaseFailed || !strings.Contains(result.Error, "stop failed") {
		t.Fatalf("runtimeStop operation=%#v err=%v", result, err)
	}
}

func TestControllerImageUpgradeUsesLifecycleRegistration(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mobile-runner" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	s := newTestServer(t)
	manager := s.manager.(*fakeManager)
	s.runnerReady = func(context.Context, map[string]string) error { return nil }
	s.cfg.values["CREDIMI_URL"] = api.URL
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"

	rec := httptest.NewRecorder()
	s.controllerUpgradeImage(rec, httptest.NewRequest(http.MethodPost, "/api/controller/maintenance/upgrade-image", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("controllerUpgradeImage = %d %s", rec.Code, rec.Body.String())
	}
	op := s.operations.Current()
	if completed, err := s.operations.Wait(context.Background(), op.ID); err != nil || completed.Phase != controller.PhaseSucceeded || manager.updateImageCalls != 1 {
		t.Fatalf("upgrade operation=%#v err=%v updates=%d", completed, err, manager.updateImageCalls)
	}
	s.manager = nil
	rec = httptest.NewRecorder()
	s.controllerUpgradeImage(rec, httptest.NewRequest(http.MethodPost, "/api/controller/maintenance/upgrade-image", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unavailable") {
		t.Fatalf("unavailable upgrade = %d %s", rec.Code, rec.Body.String())
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
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"preview_runner_id":"acme/runner-slug-2"`) {
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

func TestRegisterCurrentAndWaitForRunnerReadyBranches(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	s := newTestServer(t)
	if err := s.registerCurrent(context.Background(), map[string]string{}); err == nil {
		t.Fatal("expected registerCurrent without API key to fail")
	}

	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"connected","devices":[]}`))
		case "/readyz":
			_, _ = w.Write([]byte(`{"service":"credimi-runner","boot_id":"test-boot"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer runner.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(runner.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	values := map[string]string{
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
		"CREDIMI_RUNNER_TYPE":    "ios_simulator",
		"RUNNER_HOST":            host,
		"RUNNER_PORT":            port,
	}
	if err := s.runnerReady(context.Background(), values); err != nil {
		t.Fatalf("waitForRunnerReady = %v", err)
	}
}

func TestApplySavedConfigClearsCachedQuickTunnelURL(t *testing.T) {
	s := newTestServer(t)
	fm := &fakeManager{
		status: dashboardruntime.RuntimeStatus{
			RunnerRunning: true,
			PublicURL:     "https://old.example.trycloudflare.com",
		},
		logLines: []dashboardruntime.LogLine{
			{Message: "INF quick tunnel ready at https://new.example.trycloudflare.com"},
		},
	}
	s.manager = fm
	values := map[string]string{
		"CREDIMI_URL":                 "https://credimi.example",
		"CREDIMI_USER_API_KEY":        "user-key",
		"CREDIMI_RUNNER_ID":           "acme/runner",
		"CREDIMI_RUNNER_NAME":         "runner",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
		"CREDIMI_SERVICE_MODE":        "auto",
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mobile-runner" {
			var payload dashboardruntime.RegisterRunnerRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.IP != "https://new.example.trycloudflare.com" {
				t.Fatalf("registered IP = %q", payload.IP)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	values["CREDIMI_URL"] = api.URL

	outcome, err := s.applySavedConfig(dashboardruntime.ConfigDiff{
		ChangedKeys: []string{"RUNNER_PORT"},
		Classes:     []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired},
	}, values)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Restarted || !outcome.CredimiUpdated {
		t.Fatalf("outcome = %#v", outcome)
	}
	if fm.status.PublicURL != "https://new.example.trycloudflare.com" {
		t.Fatalf("cached public URL = %q", fm.status.PublicURL)
	}
}

func TestShouldRegisterAfterApply(t *testing.T) {
	if !shouldRegisterAfterApply(dashboardruntime.ConfigDiff{
		Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyCredimiUpdateRequired},
	}, map[string]string{}, false) {
		t.Fatal("expected explicit Credimi update to register")
	}
	if shouldRegisterAfterApply(dashboardruntime.ConfigDiff{
		Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired},
	}, map[string]string{"CREDIMI_SERVICE_MODE": "manual"}, true) {
		t.Fatal("manual restart should not force registration")
	}
	if !shouldRegisterAfterApply(dashboardruntime.ConfigDiff{
		Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired},
	}, map[string]string{"CREDIMI_SERVICE_MODE": "auto"}, true) {
		t.Fatal("auto restart should force registration")
	}
}

func TestRuntimeLogsReturnsRecentLines(t *testing.T) {
	s := newTestServer(t)
	s.manager = &fakeManager{logLines: []dashboardruntime.LogLine{
		{Message: "runner pulling image"},
		{Message: "runner started"},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/runtime/logs", nil)
	s.runtimeLogs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtimeLogs = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "runner pulling image") || !strings.Contains(rec.Body.String(), "runner started") {
		t.Fatalf("runtimeLogs body = %s", rec.Body.String())
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

func TestServerSaveConfigDescriptionUpdateUsesCompactToast(t *testing.T) {
	s := newTestServer(t)
	s.manager = &fakeManager{
		status: dashboardruntime.RuntimeStatus{
			RunnerRunning: true,
			PublicURL:     "https://cached.example.trycloudflare.com",
		},
	}
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "auto"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(s.cfg.values), runtimeGOOS())
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.values = map[string]string(normalized)

	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-runner" {
			return nil, errors.New("unexpected path: " + req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})
	defer func() { http.DefaultTransport = transport }()

	form := formValuesFromConfig(s.cfg)
	form.Set("CREDIMI_RUNNER_DESCRIPTION", "updated description")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("saveConfig description update = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "must be updated") {
		t.Fatalf("saveConfig flash should describe the completed update, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Configuration updated.") {
		t.Fatalf("saveConfig should not render inline success messages: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("HX-Trigger"), "Configuration updated.") {
		t.Fatalf("saveConfig toast missing compact result: %s", rec.Header().Get("HX-Trigger"))
	}
}

func TestServerSaveConfigRestartUsesCompactToast(t *testing.T) {
	s := newTestServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer api.Close()
	fm := &fakeManager{
		status: dashboardruntime.RuntimeStatus{RunnerRunning: true},
	}
	s.manager = fm
	s.cfg.values["CREDIMI_URL"] = api.URL
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(s.cfg.values), runtimeGOOS())
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.values = map[string]string(normalized)

	form := formValuesFromConfig(s.cfg)
	form.Set("CREDIMI_USER_API_KEY", "new-user-key")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("saveConfig restart update = %d body=%s", rec.Code, rec.Body.String())
	}
	if fm.stopCalls != 1 || fm.startCalls != 1 {
		t.Fatalf("saveConfig lifecycle calls stop=%d start=%d", fm.stopCalls, fm.startCalls)
	}
	if strings.Contains(rec.Body.String(), "Runner restarted with the new configuration.") {
		t.Fatalf("saveConfig should not render inline restart success: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("HX-Trigger"), "Runner restarted with the new configuration.") {
		t.Fatalf("saveConfig restart toast missing compact result: %s", rec.Header().Get("HX-Trigger"))
	}
}

func TestServerSaveConfigNameChangeStoresDerivedRunnerIDAndRestarts(t *testing.T) {
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/mobile-runner/preview-id":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"organization":"acme","runner_id":"acme/renamed-runner"}`)),
			}, nil
		case "/api/mobile-runner":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		default:
			return nil, errors.New("unexpected path: " + req.URL.Path)
		}
	})
	defer func() { http.DefaultTransport = transport }()

	s := newTestServer(t)
	fm := &fakeManager{status: dashboardruntime.RuntimeStatus{RunnerRunning: true}}
	s.manager = fm
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(s.cfg.values), runtimeGOOS())
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.values = map[string]string(normalized)

	form := formValuesFromConfig(s.cfg)
	form.Set("CREDIMI_RUNNER_NAME", "Renamed Runner")
	form.Set("CREDIMI_RUNNER_ID", "evil/id")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("saveConfig name change = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.cfg.Get("CREDIMI_RUNNER_ID"); got != "acme/renamed-runner" {
		t.Fatalf("stored runner ID = %q", got)
	}
	if got := s.cfg.Get("CREDIMI_RUNNER_NAME"); got != "Renamed Runner" {
		t.Fatalf("stored runner name = %q", got)
	}
	if fm.stopCalls != 1 || fm.startCalls != 1 {
		t.Fatalf("saveConfig name change lifecycle calls stop=%d start=%d", fm.stopCalls, fm.startCalls)
	}
	if !strings.Contains(rec.Header().Get("HX-Trigger"), "Runner restarted with the new configuration.") {
		t.Fatalf("saveConfig name change toast missing restart result: %s", rec.Header().Get("HX-Trigger"))
	}
}

func waitForCondition(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not satisfied before timeout")
}

func TestServerSaveAndFinishSetupValidationErrors(t *testing.T) {
	s := newTestServer(t)
	form := url.Values{"CREDIMI_URL": {"https://credimi.io"}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveConfig(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Some fields need attention") {
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

	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/devices" {
		t.Fatalf("save device response = %d location=%q body=%s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Devices) != 1 || config.Devices[0].ID != "acme/runner/pixel" || config.Devices[0].Serial != "usb-1" {
		t.Fatalf("persisted devices = %#v", config.Devices)
	}
}

func TestServerSaveDevicesConfigUpdatesOnlySelectedDevice(t *testing.T) {
	s := newTestServer(t)
	dir := filepath.Dir(s.cfg.Path())
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values{"CREDIMI_RUNNER_ID": "acme/runner"}, Devices: []dashboardruntime.DeviceRuntimeConfig{
		{ID: "acme/runner/one", Name: "One", Type: "android_phone", Mode: "usb", Enabled: true, Values: dashboardruntime.Values{"SERIAL": "one"}},
		{ID: "acme/runner/two", Name: "Two", Type: "android_phone", Mode: "wifi", Enabled: true, Values: dashboardruntime.Values{"WIFI_IP": "10.0.0.2"}},
	}}); err != nil {
		t.Fatal(err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	form := url.Values{"device_id": {"acme/runner/one"}, "name": {"Renamed One"}, "type": {"android_phone"}, "mode": {"wifi"}, "wifi_ip": {"10.0.0.1"}, "wifi_port": {"5555"}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveDevicesConfig(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("update = %d %s", recorder.Code, recorder.Body.String())
	}
	updated, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	config, err := updated.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Devices[0].Name != "Renamed One" || config.Devices[0].Values["WIFI_IP"] != "10.0.0.1" || config.Devices[1].Name != "Two" || config.Devices[1].Values["WIFI_IP"] != "10.0.0.2" {
		t.Fatalf("devices = %#v", config.Devices)
	}

	form.Set("device_id", "acme/runner/missing")
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveDevicesConfig(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing update = %d", recorder.Code)
	}
}

func TestServerDeviceEnableAndRemovePersistIndexedInventory(t *testing.T) {
	s := newTestServer(t)
	dir := filepath.Dir(s.cfg.Path())
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	config := dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values{"CREDIMI_RUNNER_ID": "acme/runner"}, Devices: []dashboardruntime.DeviceRuntimeConfig{
		{ID: "acme/runner/one", Name: "One", Type: "android_phone", Mode: "usb", Enabled: true, Values: dashboardruntime.Values{}},
		{ID: "acme/runner/two", Name: "Two", Type: "ios_simulator", Mode: "no_device", Enabled: true, Values: dashboardruntime.Values{}},
	}}
	if err := store.SaveRuntimeConfig(config); err != nil {
		t.Fatal(err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)

	enable := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/devices/disable", strings.NewReader(url.Values{"device_id": {"acme/runner/one"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.deviceDisable(enable, req)
	if enable.Code != http.StatusSeeOther {
		t.Fatalf("disable = %d %s", enable.Code, enable.Body.String())
	}
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
	if remove.Code != http.StatusOK {
		t.Fatalf("remove = %d %s", remove.Code, remove.Body.String())
	}
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
		if req.URL.Path != "/api/mobile-device/preview-id" {
			return nil, errors.New("unexpected path: " + req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_id":"acme/runner/pixel"}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	form := url.Values{
		"CREDIMI_URL":                 {"https://credimi.example"},
		"CREDIMI_USER_API_KEY":        {"user-key"},
		"CREDIMI_RUNNER_ID":           {"acme/runner"},
		"CREDIMI_RUNNER_ORGANIZATION": {"acme"},
		"CREDIMI_SERVICE_MODE":        {"manual"},
		"RUNNER_PUBLIC_URL":           {"https://runner.example"},
		"setup_device_name":           {"Pixel"},
		"setup_device_type":           {"redroid"},
		"setup_device_mode":           {"no_device"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("HX-Request", "true")
	s.finishSetup(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("setup response = %d redirect=%q body=%s", recorder.Code, recorder.Header().Get("HX-Redirect"), recorder.Body.String())
	}
	if !s.cfg.Exists() || s.cfg.Get("CREDIMI_RUNNER_ID") != "acme/runner" {
		t.Fatalf("setup was not persisted: exists=%t values=%#v", s.cfg.Exists(), s.cfg.Snapshot())
	}
}

func TestServerRuntimeStartRegistersRunner(t *testing.T) {
	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	s.cfg.values["CREDIMI_RUNNER_PUBLISHED"] = "true"

	var payload dashboardruntime.RegisterRunnerRequest
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mobile-runner" {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	s.cfg.values["CREDIMI_URL"] = api.URL

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/runtime/start", nil)
	s.runtimeStart(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("runtimeStart = %d body=%s", rec.Code, rec.Body.String())
	}
	op := s.operations.Current()
	if _, err := s.operations.Wait(context.Background(), op.ID); err != nil {
		t.Fatalf("runtimeStart operation: %v", err)
	}
	fm := s.manager.(*fakeManager)
	if fm.startCalls == 0 {
		t.Fatal("runtimeStart should start runtime")
	}
	if payload.Published == nil || !*payload.Published {
		t.Fatalf("runtimeStart registration published = %#v", payload.Published)
	}
}

func TestDashboardRuntimeStartUsesControllerLifecycleAndRefreshesAutoURL(t *testing.T) {
	s := newTestServer(t)
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "auto"
	s.manager.(*fakeManager).logLines = []dashboardruntime.LogLine{{Message: "tunnel ready https://new-url.trycloudflare.com"}}

	var payload dashboardruntime.RegisterRunnerRequest
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mobile-runner" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	s.cfg.values["CREDIMI_URL"] = api.URL

	rec := httptest.NewRecorder()
	s.runtimeStart(rec, httptest.NewRequest(http.MethodPost, "/runtime/start", nil))
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 || rec.Header().Get("HX-Reswap") != "none" {
		t.Fatalf("runtime start response = %d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
	}
	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "runtimeOperation") || strings.Contains(trigger, "Operation op-") {
		t.Fatalf("runtime start trigger = %q", trigger)
	}

	op := s.operations.Current()
	completed, err := s.operations.Wait(context.Background(), op.ID)
	if err != nil || completed.Phase != controller.PhaseSucceeded {
		t.Fatalf("runtime start completed=%#v err=%v", completed, err)
	}
	if payload.IP != "https://new-url.trycloudflare.com" {
		t.Fatalf("registered URL = %q", payload.IP)
	}
	if got := s.manager.Status(context.Background()).PublicURL; got != payload.IP {
		t.Fatalf("dashboard public URL = %q, want %q", got, payload.IP)
	}

	rec = httptest.NewRecorder()
	s.runtimeStop(rec, httptest.NewRequest(http.MethodPost, "/runtime/stop", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("runtime stop response = %d", rec.Code)
	}
	op = s.operations.Current()
	if completed, err = s.operations.Wait(context.Background(), op.ID); err != nil || completed.Phase != controller.PhaseSucceeded {
		t.Fatalf("runtime stop completed=%#v err=%v", completed, err)
	}
	if got := s.manager.Status(context.Background()).PublicURL; got != "" {
		t.Fatalf("stopped runtime retained quick tunnel URL %q", got)
	}

	s.manager.(*fakeManager).logLines = []dashboardruntime.LogLine{{Message: "tunnel ready https://replacement-url.trycloudflare.com"}}
	rec = httptest.NewRecorder()
	s.runtimeRestart(rec, httptest.NewRequest(http.MethodPost, "/runtime/restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("runtime restart response = %d", rec.Code)
	}
	op = s.operations.Current()
	if completed, err = s.operations.Wait(context.Background(), op.ID); err != nil || completed.Phase != controller.PhaseSucceeded {
		t.Fatalf("runtime restart completed=%#v err=%v", completed, err)
	}
	if payload.IP != "https://replacement-url.trycloudflare.com" {
		t.Fatalf("restart registered URL = %q", payload.IP)
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
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values(s.cfg.Snapshot()), Devices: []dashboardruntime.DeviceRuntimeConfig{{ID: "acme/runner/pixel", Name: "Pixel", Type: "android_phone", Mode: "usb", Enabled: true, Values: dashboardruntime.Values{"SERIAL": "usb-1"}}, {ID: "acme/runner/pixel-2", Name: "Pixel Two", Type: "android_phone", Mode: "usb", Enabled: true, Values: dashboardruntime.Values{"SERIAL": "usb-2"}}}}); err != nil {
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
	if rec := post(s.deviceDisable, url.Values{"device_id": {"acme/runner/pixel"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(s.deviceRemove, url.Values{"device_id": {"acme/runner/pixel"}, "confirm": {"true"}}); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	}
}

func TestServerValidateRuntimeRequirements(t *testing.T) {
	base := map[string]string{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner/pixel", "CREDIMI_DEVICE_1_TYPE": "android_phone", "CREDIMI_DEVICE_1_MODE": "usb", "CREDIMI_DEVICE_1_SERIAL": "device-1"}
	s := newTestServer(t)
	if err := s.validateRuntimeRequirements(base); err != nil {
		t.Fatalf("connected phone requirements = %v", err)
	}
	s.hub.snap.Devices[0].Status = Offline
	if err := s.validateRuntimeRequirements(base); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("offline phone requirements = %v", err)
	}
	emulator := cloneStringMap(base)
	emulator["CREDIMI_DEVICE_1_TYPE"] = "android_emulator"
	emulator["CREDIMI_DEVICE_1_MODE"] = "emulator"
	emulator["CREDIMI_DEVICE_1_ANDROID_KEYS_DIR"] = "/keys"
	emulator["CREDIMI_DEVICE_1_HOST_AVD_HOME_PATH"] = "/avd"
	emulator["CREDIMI_DEVICE_1_HOST_AVD_GOLDEN_PATH"] = "/golden"
	s.statPath = func(path string) (os.FileInfo, error) {
		if path == "/dev/kvm" {
			return nil, os.ErrNotExist
		}
		return fakeFileInfo("ok"), nil
	}
	if err := s.validateRuntimeRequirements(emulator); err == nil || !strings.Contains(err.Error(), "/dev/kvm") {
		t.Fatalf("emulator requirements = %v", err)
	}
}

func TestServerStartupJobStartsAndRegistersConfiguredHost(t *testing.T) {
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/mobile-runner" {
			return nil, errors.New("unexpected path: " + req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	s.startStartupJob(s.cfg.Snapshot())
	waitForCondition(t, func() bool { return s.startupSnapshot().Phase == StartupReady && !s.startupSnapshot().running })
	if s.manager.(*fakeManager).startCalls != 1 {
		t.Fatalf("start calls = %d", s.manager.(*fakeManager).startCalls)
	}
}

func TestServerExistingRuntimeJobRegistersWithoutRestart(t *testing.T) {
	transport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	s := newTestServer(t)
	s.manager.(*fakeManager).status.RunnerRunning = true
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	s.startExistingRuntimeJob(s.cfg.Snapshot())
	waitForCondition(t, func() bool { return s.startupSnapshot().Phase == StartupReady && !s.startupSnapshot().running })
	if s.manager.(*fakeManager).startCalls != 0 {
		t.Fatalf("start calls = %d", s.manager.(*fakeManager).startCalls)
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
