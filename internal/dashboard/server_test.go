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
	f.stopCalls++
	if f.stopErr != nil {
		return f.stopErr
	}
	f.status.RunnerRunning = false
	f.status.PublicURL = ""
	return nil
}
func (f *fakeManager) Restart(context.Context) error {
	f.restartCalls++
	if f.restartErr == nil {
		f.status.LastStartedAt = time.Now()
	}
	return f.restartErr
}
func (f *fakeManager) UpdateImage(context.Context) error {
	f.updateImageCalls++
	return f.updateImageErr
}
func (f *fakeManager) UpgradeRunnerImage(_ context.Context, progress func(string)) error {
	f.updateImageCalls++
	if progress != nil {
		progress("Stopping the runner and Docker services.")
		progress("Downloading the latest runner image.")
	}
	if f.upgradeBlock != nil {
		<-f.upgradeBlock
	}
	return f.updateImageErr
}
func (f *fakeManager) Configure(values dashboardruntime.Values) {
	f.status.Configured = strings.TrimSpace(values["CREDIMI_RUNNER_ID"]) != ""
}
func (f *fakeManager) SetPublicURL(publicURL string)                         { f.status.PublicURL = publicURL }
func (f *fakeManager) Status(context.Context) dashboardruntime.RuntimeStatus { return f.status }
func (f *fakeManager) Logs(_ context.Context, tail int) ([]dashboardruntime.LogLine, error) {
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
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil || queued["ID"] == nil {
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

func TestWaitForRunnerHealthValidatesConfiguredDevice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"connected","devices":[{"serial":"ready","state":"device"}]}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForRunnerHealth(context.Background(), host, port, "ready"); err != nil {
		t.Fatal(err)
	}
	if err := waitForRunnerHealth(context.Background(), host, port, "missing"); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("missing device error = %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer bad.Close()
	badHost, badPort, err := net.SplitHostPort(strings.TrimPrefix(bad.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForRunnerHealth(context.Background(), badHost, badPort, ""); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("bad health error = %v", err)
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
	if !strings.Contains(rec.Body.String(), "Set up Credimi Runner") {
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

func TestServerNormalizeConfigPreviewRunnerTypeChange(t *testing.T) {
	s := newTestServer(t)
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	s.cfg.values["RUNNER_IMAGE"] = defaultPhoneImage

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config/normalize", strings.NewReader(url.Values{
		"CREDIMI_RUNNER_TYPE": {"android_emulator"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.normalizeConfigPreview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("normalizeConfigPreview = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), defaultEmulatorImage) || !strings.Contains(rec.Body.String(), dashboardruntime.DefaultGoldenPath) {
		t.Fatalf("normalizeConfigPreview missing emulator defaults: %s", rec.Body.String())
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

func TestServerSaveDevicesConfig(t *testing.T) {
	s := newTestServer(t)
	form := url.Values{
		"CREDIMI_URL":                 {"https://credimi.example"},
		"CREDIMI_RUNNER_ID":           {"acme/runner"},
		"CREDIMI_RUNNER_NAME":         {"runner"},
		"CREDIMI_RUNNER_ORGANIZATION": {"acme"},
		"CREDIMI_USER_API_KEY":        {"user-key"},
		"CREDIMI_SERVICE_MODE":        {"manual"},
		"RUNNER_PUBLIC_URL":           {"https://runner.example"},
		"CREDIMI_RUNNER_TYPE":         {"android_phone"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveDevicesConfig(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Save target") {
		t.Fatalf("saveDevicesConfig = %d %s", rec.Code, rec.Body.String())
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
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"

	for _, target := range []struct {
		path string
		fn   http.HandlerFunc
	}{
		{"/runtime/stop", s.runtimeStop},
		{"/runtime/restart", s.runtimeRestart},
		{"/runtime/update-image", s.runtimeUpdateImage},
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
	s.cfg.values["RUNNER_IMAGE"] = "credimi-runner-phone:latest"
	s.cfg.values["RUNNER_IMAGE_PULL_POLICY"] = "never"
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
	deadline := time.Now().Add(time.Second)
	for restarted == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
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

func TestFinishSetupValidationAndRequirementErrors(t *testing.T) {
	s := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(url.Values{
		"CREDIMI_URL":          {"https://credimi.example"},
		"CREDIMI_USER_API_KEY": {"user-key"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.finishSetup(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Some fields need attention.") {
		t.Fatalf("finishSetup validation = %d %s", rec.Code, rec.Body.String())
	}

	s.lookupPath = func(string) (string, error) { return "", os.ErrNotExist }
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(url.Values{
		"CREDIMI_URL":                 {"https://credimi.example"},
		"CREDIMI_RUNNER_ID":           {"acme/runner"},
		"CREDIMI_RUNNER_NAME":         {"runner"},
		"CREDIMI_RUNNER_ORGANIZATION": {"acme"},
		"CREDIMI_USER_API_KEY":        {"user-key"},
		"CREDIMI_SERVICE_MODE":        {"cloudflare-managed"},
		"CREDIMI_RUNNER_TYPE":         {"android_phone"},
		"CREDIMI_RUNNER_SERIAL":       {"device-1"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.finishSetup(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "runtime requirement check failed") {
		t.Fatalf("finishSetup requirements = %d %s", rec.Code, rec.Body.String())
	}
}

func TestValidateSetupInputRequiresRedroidWiFiIP(t *testing.T) {
	values := map[string]string{
		"CREDIMI_URL":          "https://credimi.example",
		"CREDIMI_USER_API_KEY": "user-key",
		"CREDIMI_RUNNER_NAME":  "runner",
		"CREDIMI_RUNNER_TYPE":  "redroid",
	}
	if errs := validateSetupInput(values); errs["CREDIMI_RUNNER_WIFI_IP"] == "" {
		t.Fatalf("validateSetupInput errors = %#v", errs)
	}
	values["CREDIMI_RUNNER_WIFI_IP"] = "192.168.1.30"
	if errs := validateSetupInput(values); len(errs) != 0 {
		t.Fatalf("validateSetupInput errors = %#v", errs)
	}
}

func TestValidateSetupInputRequiresConnectionAndPublicEndpoint(t *testing.T) {
	errs := validateSetupInput(map[string]string{"CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_SERVICE_MODE": "manual"})
	for _, key := range []string{"CREDIMI_URL", "CREDIMI_USER_API_KEY", "CREDIMI_RUNNER_NAME", "CREDIMI_RUNNER_SERIAL", "RUNNER_PUBLIC_URL"} {
		if errs[key] == "" {
			t.Fatalf("missing validation error %s: %#v", key, errs)
		}
	}
	valid := validateSetupInput(map[string]string{
		"CREDIMI_URL": "https://credimi.example", "CREDIMI_INTERNAL_ADMIN_KEY": "key", "CREDIMI_RUNNER_ID": "acme/runner",
		"CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_RUNNER_DEVICE_MODE": "wifi", "CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://runner.example",
	})
	if len(valid) != 0 {
		t.Fatalf("valid setup errors = %#v", valid)
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
	if err := s.waitForRunnerReady(context.Background(), values); err != nil {
		t.Fatalf("waitForRunnerReady = %v", err)
	}
}

func TestResolveRegistrationEndpointBranches(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	if _, _, err := s.resolveRegistrationEndpoint(context.Background(), map[string]string{
		"CREDIMI_SERVICE_MODE": "auto",
	}); err == nil || !strings.Contains(err.Error(), "runtime manager unavailable") {
		t.Fatalf("expected runtime manager unavailable, got %v", err)
	}

	manager := &fakeManager{logLines: []dashboardruntime.LogLine{
		{Message: "INF quick tunnel ready at https://runner.example.trycloudflare.com"},
	}}
	s.manager = manager
	url, port, err := s.resolveRegistrationEndpoint(context.Background(), map[string]string{
		"CREDIMI_SERVICE_MODE": "auto",
	})
	if err != nil || url != "https://runner.example.trycloudflare.com" || port != "" {
		t.Fatalf("resolveRegistrationEndpoint auto = %q %q %v", url, port, err)
	}
	if manager.logTail != quickTunnelLogTail {
		t.Fatalf("registration log tail = %d, want %d", manager.logTail, quickTunnelLogTail)
	}

	url, port, err = s.resolveRegistrationEndpoint(context.Background(), map[string]string{
		"CREDIMI_SERVICE_MODE": "cloudflare-managed",
		"RUNNER_DOMAIN":        "runner.example",
	})
	if err != nil || url != "https://runner.example" || port != "" {
		t.Fatalf("resolveRegistrationEndpoint managed = %q %q %v", url, port, err)
	}

	s.manager = &fakeManager{
		status: dashboardruntime.RuntimeStatus{PublicURL: "https://cached.example.trycloudflare.com"},
		logLines: []dashboardruntime.LogLine{
			{Message: "INF quick tunnel ready at https://old.example.trycloudflare.com"},
			{Message: "INF quick tunnel ready at https://new.example.trycloudflare.com"},
		},
	}
	url, _, err = s.resolveRegistrationEndpoint(context.Background(), map[string]string{
		"CREDIMI_SERVICE_MODE": "auto",
	})
	if err != nil || url != "https://cached.example.trycloudflare.com" {
		t.Fatalf("resolveRegistrationEndpoint cached auto URL = %q %v", url, err)
	}

	manager = &fakeManager{
		status: dashboardruntime.RuntimeStatus{
			LastStartedAt: time.Now(),
		},
		logLines: []dashboardruntime.LogLine{
			{Message: "INF quick tunnel ready at https://old.example.trycloudflare.com"},
		},
		logLinesSince: []dashboardruntime.LogLine{
			{Message: "INF quick tunnel ready at https://fresh.example.trycloudflare.com"},
		},
	}
	s.manager = manager
	url, _, err = s.resolveRegistrationEndpoint(context.Background(), map[string]string{
		"CREDIMI_SERVICE_MODE": "auto",
	})
	if err != nil || url != "https://fresh.example.trycloudflare.com" {
		t.Fatalf("resolveRegistrationEndpoint restarted auto URL = %q %v", url, err)
	}
	if manager.logTail <= 0 {
		t.Fatalf("restarted auto URL should use current-start logs, tail = %d", manager.logTail)
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

	outcome, err := s.applySavedConfig(context.Background(), dashboardruntime.ConfigDiff{
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

func TestServerSaveAndFinishSetup(t *testing.T) {
	s := newTestServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/mobile-runner":
			w.WriteHeader(http.StatusOK)
		case "/api/organizations/my":
			w.Write([]byte(`{"canonified_name":"acme"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	form := url.Values{
		"CREDIMI_URL":                 {api.URL},
		"CREDIMI_RUNNER_ID":           {"acme/runner"},
		"CREDIMI_RUNNER_NAME":         {"runner"},
		"CREDIMI_RUNNER_ORGANIZATION": {"acme"},
		"CREDIMI_USER_API_KEY":        {"user-key"},
		"CREDIMI_SERVICE_MODE":        {"manual"},
		"RUNNER_PUBLIC_URL":           {"https://runner.example"},
		"CREDIMI_RUNNER_TYPE":         {"android_phone"},
		"CREDIMI_RUNNER_SERIAL":       {"device-1"},
		"RUNNER_PORT":                 {"8050"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveConfig(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Trigger") == "" {
		t.Fatalf("saveConfig = %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data-config-form") {
		t.Fatalf("saveConfig body missing config form: %s", rec.Body.String())
	}
	if fm, ok := s.manager.(*fakeManager); !ok || fm.restartCalls != 0 {
		t.Fatalf("saveConfig should not restart automatically: %#v", s.manager)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	s.finishSetup(rec, req)
	if rec.Code != http.StatusAccepted || rec.Header().Get("HX-Redirect") != "" {
		t.Fatalf("finishSetup = %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	waitForCondition(t, func() bool {
		return s.manager.(*fakeManager).startCalls > 0
	})
	if fm := s.manager.(*fakeManager); fm.startCalls == 0 {
		t.Fatal("finishSetup should start runtime")
	}
	startup := s.startupSnapshot()
	if !containsString(startup.Logs, "runner Downloading 128MB") {
		t.Fatalf("startup logs missing docker progress: %#v", startup.Logs)
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
	fm := &fakeManager{
		status: dashboardruntime.RuntimeStatus{RunnerRunning: true},
	}
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
	form.Set("CREDIMI_USER_API_KEY", "new-user-key")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("saveConfig restart update = %d body=%s", rec.Code, rec.Body.String())
	}
	if fm.restartCalls != 1 {
		t.Fatalf("saveConfig restart calls = %d", fm.restartCalls)
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
	if fm.restartCalls != 1 {
		t.Fatalf("saveConfig name change restart calls = %d", fm.restartCalls)
	}
	if !strings.Contains(rec.Header().Get("HX-Trigger"), "Runner restarted with the new configuration.") {
		t.Fatalf("saveConfig name change toast missing restart result: %s", rec.Header().Get("HX-Trigger"))
	}
}

func TestServerFinishSetupKeepsStartedRuntimeWhenRegistrationFails(t *testing.T) {
	s := newTestServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/mobile-runner":
			http.Error(w, "registration unavailable", http.StatusBadGateway)
		case "/api/organizations/my":
			w.Write([]byte(`{"canonified_name":"acme"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	form := url.Values{
		"CREDIMI_URL":                 {api.URL},
		"CREDIMI_RUNNER_ID":           {"acme/runner"},
		"CREDIMI_RUNNER_NAME":         {"runner"},
		"CREDIMI_RUNNER_ORGANIZATION": {"acme"},
		"CREDIMI_USER_API_KEY":        {"user-key"},
		"CREDIMI_SERVICE_MODE":        {"manual"},
		"RUNNER_PUBLIC_URL":           {"https://runner.example"},
		"CREDIMI_RUNNER_TYPE":         {"android_phone"},
		"CREDIMI_RUNNER_SERIAL":       {"device-1"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	s.finishSetup(rec, req)
	if rec.Code != http.StatusAccepted || rec.Header().Get("HX-Redirect") != "" {
		t.Fatalf("finishSetup registration failure = %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	waitForCondition(t, func() bool {
		return strings.Contains(s.startupSnapshot().Message, "Credimi registration failed")
	})
	if fm := s.manager.(*fakeManager); fm.startCalls == 0 {
		t.Fatal("finishSetup should keep the runtime start when registration fails")
	}
	if !strings.Contains(s.lastRegistrationStatus, "Credimi registration failed") {
		t.Fatalf("lastRegistrationStatus = %q", s.lastRegistrationStatus)
	}
}

func TestServerFinishSetupKeepsStartedRuntimeWhenReadinessFails(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	s := newTestServer(t)
	s.runnerReady = func(context.Context, map[string]string) error {
		return context.DeadlineExceeded
	}
	form := url.Values{
		"CREDIMI_URL":                 {"https://credimi.example"},
		"CREDIMI_RUNNER_ID":           {"acme/runner"},
		"CREDIMI_RUNNER_BACKEND":      {"host"},
		"CREDIMI_RUNNER_NAME":         {"runner"},
		"CREDIMI_RUNNER_ORGANIZATION": {"acme"},
		"CREDIMI_USER_API_KEY":        {"user-key"},
		"CREDIMI_SERVICE_MODE":        {"manual"},
		"RUNNER_PUBLIC_URL":           {"https://runner.example"},
		"CREDIMI_RUNNER_TYPE":         {"ios_simulator"},
		"BASE_NAME":                   {"credimi"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	s.finishSetup(rec, req)
	if rec.Code != http.StatusAccepted || rec.Header().Get("HX-Redirect") != "" {
		t.Fatalf("finishSetup readiness failure = %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	waitForCondition(t, func() bool {
		return strings.Contains(s.startupSnapshot().Message, "readiness was not confirmed")
	})
	if fm := s.manager.(*fakeManager); fm.startCalls == 0 {
		t.Fatal("finishSetup should keep the runtime start when readiness is not confirmed")
	}
	if !strings.Contains(s.lastRegistrationStatus, "readiness was not confirmed") {
		t.Fatalf("lastRegistrationStatus = %q", s.lastRegistrationStatus)
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestServerRuntimeApply(t *testing.T) {
	s := newTestServer(t)
	s.pendingDiff = dashboardruntime.ConfigDiff{
		Classes: []dashboardruntime.ApplyClass{
			dashboardruntime.ApplyRestartRequired,
			dashboardruntime.ApplyCredimiUpdateRequired,
		},
	}
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
	req := httptest.NewRequest(http.MethodPost, "/runtime/apply", nil)
	s.runtimeApply(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("runtimeApply = %d body=%s", rec.Code, rec.Body.String())
	}
	op := s.operations.Current()
	if _, err := s.operations.Wait(context.Background(), op.ID); err != nil {
		t.Fatalf("runtimeApply operation: %v", err)
	}
	fm := s.manager.(*fakeManager)
	if fm.restartCalls == 0 {
		t.Fatal("runtimeApply should restart when restart is pending")
	}
	if len(s.pendingDiff.Classes) != 0 {
		t.Fatalf("pendingDiff not cleared: %#v", s.pendingDiff)
	}
}

func TestServerRuntimeApplyAutoRestartRefreshesTunnelURL(t *testing.T) {
	s := newTestServer(t)
	s.manager = &fakeManager{
		status: dashboardruntime.RuntimeStatus{
			RunnerRunning: true,
			PublicURL:     "https://old.example.trycloudflare.com",
		},
		logLines: []dashboardruntime.LogLine{
			{Message: "INF quick tunnel ready at https://fresh.example.trycloudflare.com"},
		},
	}
	s.pendingDiff = dashboardruntime.ConfigDiff{
		ChangedKeys: []string{"RUNNER_IMAGE"},
		Classes:     []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired},
	}
	s.cfg.values["CREDIMI_URL"] = "https://credimi.example"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "auto"

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
	req := httptest.NewRequest(http.MethodPost, "/runtime/apply", nil)
	s.runtimeApply(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("runtimeApply = %d body=%s", rec.Code, rec.Body.String())
	}
	op := s.operations.Current()
	if _, err := s.operations.Wait(context.Background(), op.ID); err != nil {
		t.Fatalf("runtimeApply operation: %v", err)
	}
	if payload.IP != "https://fresh.example.trycloudflare.com" {
		t.Fatalf("registered IP = %q", payload.IP)
	}
	if got := s.manager.(*fakeManager).status.PublicURL; got != "https://fresh.example.trycloudflare.com" {
		t.Fatalf("cached public URL = %q", got)
	}
}

func TestValidateRuntimeRequirements(t *testing.T) {
	s := newTestServer(t)
	values := map[string]string{
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "auto",
		"CREDIMI_RUNNER_TYPE":    "android_phone",
		"CREDIMI_RUNNER_SERIAL":  "device-1",
	}
	if err := s.validateRuntimeRequirements(values); err != nil {
		t.Fatalf("validateRuntimeRequirements = %v", err)
	}

	s.hub.snap.Devices = []Device{
		{Serial: "emulator-5554", Name: "Pixel test", Type: "android_emulator", Mode: "emulator", OS: "Android", Status: Online},
	}
	values["CREDIMI_RUNNER_SERIAL"] = "emulator-5554"
	if err := s.validateRuntimeRequirements(values); err != nil {
		t.Fatalf("validateRuntimeRequirements emulator serial = %v", err)
	}

	s.lookupPath = func(name string) (string, error) {
		if name == "docker" {
			return "", os.ErrNotExist
		}
		return "/tmp/fake", nil
	}
	if err := s.validateRuntimeRequirements(values); err == nil || !strings.Contains(err.Error(), "docker is required") {
		t.Fatalf("expected docker requirement error, got %v", err)
	}

	s = newTestServer(t)
	t.Setenv("GOOS_OVERRIDE", "darwin")
	s.lookupPath = func(name string) (string, error) {
		if name == "xcrun" {
			return "", os.ErrNotExist
		}
		return "/tmp/fake", nil
	}
	if err := s.validateRuntimeRequirements(map[string]string{
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
		"CREDIMI_RUNNER_TYPE":    "ios_simulator",
	}); err == nil || !strings.Contains(err.Error(), "xcrun simctl") {
		t.Fatalf("expected xcrun requirement error, got %v", err)
	}

	s = newTestServer(t)
	s.statPath = func(path string) (os.FileInfo, error) {
		if path == "/dev/kvm" {
			return nil, os.ErrNotExist
		}
		return fakeFileInfo("ok"), nil
	}
	if err := s.validateRuntimeRequirements(map[string]string{
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "manual",
		"CREDIMI_RUNNER_TYPE":    "android_emulator",
		"ANDROID_KEYS_DIR":       "/tmp/keys",
		"HOST_AVD_HOME_PATH":     "/tmp/avd-home",
		"HOST_AVD_GOLDEN_PATH":   "/tmp/avd-golden",
	}); err == nil || !strings.Contains(err.Error(), "/dev/kvm") {
		t.Fatalf("expected /dev/kvm requirement error, got %v", err)
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
}

func TestResolveRegistrationEndpointWaitsForTunnelURL(t *testing.T) {
	s := newTestServer(t)
	attempts := 0
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "auto"
	s.manager = dashboardruntime.Manager(fakeLogManager(func(context.Context, int) ([]dashboardruntime.LogLine, error) {
		attempts++
		if attempts < 3 {
			return nil, nil
		}
		return []dashboardruntime.LogLine{{Message: "tunnel ready https://runner.example.trycloudflare.com"}}, nil
	}))

	got, _, err := s.resolveRegistrationEndpoint(context.Background(), s.cfg.Snapshot())
	if err != nil {
		t.Fatalf("resolveRegistrationEndpoint error = %v", err)
	}
	if got != "https://runner.example.trycloudflare.com" {
		t.Fatalf("resolveRegistrationEndpoint = %q", got)
	}
}

type fakeLogManager func(context.Context, int) ([]dashboardruntime.LogLine, error)

func (f fakeLogManager) Start(context.Context) error       { return nil }
func (f fakeLogManager) Stop(context.Context) error        { return nil }
func (f fakeLogManager) Restart(context.Context) error     { return nil }
func (f fakeLogManager) UpdateImage(context.Context) error { return nil }
func (f fakeLogManager) Configure(dashboardruntime.Values) {}
func (f fakeLogManager) SetPublicURL(string)               {}
func (f fakeLogManager) Status(context.Context) dashboardruntime.RuntimeStatus {
	return dashboardruntime.RuntimeStatus{}
}
func (f fakeLogManager) Logs(ctx context.Context, tail int) ([]dashboardruntime.LogLine, error) {
	return f(ctx, tail)
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
