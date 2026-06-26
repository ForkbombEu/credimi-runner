package dashboard

import (
	"context"
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

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeManager struct {
	startCalls       int
	stopCalls        int
	restartCalls     int
	downCalls        int
	updateImageCalls int
	logLines         []dashboardruntime.LogLine
	status           dashboardruntime.RuntimeStatus
	startErr         error
	stopErr          error
	restartErr       error
	downErr          error
	updateImageErr   error
}

func (f *fakeManager) Start(context.Context) error {
	f.startCalls++
	if f.startErr != nil {
		return f.startErr
	}
	f.status.RunnerRunning = true
	return nil
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
	return f.restartErr
}
func (f *fakeManager) Down(context.Context) error {
	f.downCalls++
	f.status.PublicURL = ""
	return f.downErr
}
func (f *fakeManager) UpdateImage(context.Context) error {
	f.updateImageCalls++
	return f.updateImageErr
}
func (f *fakeManager) Configure(values dashboardruntime.Values) {
	f.status.Configured = strings.TrimSpace(values["CREDIMI_RUNNER_ID"]) != ""
}
func (f *fakeManager) SetPublicURL(publicURL string)                         { f.status.PublicURL = publicURL }
func (f *fakeManager) Status(context.Context) dashboardruntime.RuntimeStatus { return f.status }
func (f *fakeManager) Logs(context.Context, int) ([]dashboardruntime.LogLine, error) {
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
	hub.snap = Snapshot{Services: []Service{{ID: "runner", Name: "runner", Status: Online}}}
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
		runnerReady: func(context.Context, map[string]string) error { return nil },
		lookupPath:  func(string) (string, error) { return "/tmp/fake-bin", nil },
		statPath:    func(string) (os.FileInfo, error) { return fakeFileInfo("ok"), nil },
	}
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
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config/diff", strings.NewReader(url.Values{
		"CREDIMI_RUNNER_ID":   {"acme/runner"},
		"CREDIMI_RUNNER_NAME": {"runner-2"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.configDiff(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "runner record in Credimi") {
		t.Fatalf("configDiff = %d %s", rec.Code, rec.Body.String())
	}
	if got := describeDiffImpact(dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplySavedOnly}}); !strings.Contains(got, "No restart") {
		t.Fatalf("describeDiffImpact = %q", got)
	}
	if got := describeDiffImpact(dashboardruntime.ConfigDiff{}); got != "Save these changes?" {
		t.Fatalf("default describeDiffImpact = %q", got)
	}
	for _, diff := range []dashboardruntime.ConfigDiff{
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyComposeRecreate, dashboardruntime.ApplyCredimiUpdateRequired}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired, dashboardruntime.ApplyCredimiUpdateRequired}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyComposeRecreate}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyCredimiUpdateRequired}},
	} {
		if got := describeDiffImpact(diff); got == "" {
			t.Fatalf("describeDiffImpact(%#v) returned empty string", diff)
		}
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
	if !strings.Contains(rec.Body.String(), "runner record in Credimi") {
		t.Fatalf("runner type change should require Credimi update: %s", rec.Body.String())
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

func TestFlashForDiffVariants(t *testing.T) {
	tests := []dashboardruntime.ConfigDiff{
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplySavedOnly}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyComposeRecreate, dashboardruntime.ApplyCredimiUpdateRequired}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired, dashboardruntime.ApplyCredimiUpdateRequired}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyComposeRecreate}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired}},
		{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyCredimiUpdateRequired}},
	}
	for _, diff := range tests {
		if got := flashForDiff(diff); got == "" {
			t.Fatalf("flashForDiff(%#v) returned empty string", diff)
		}
	}
}

func TestServerSetupRenderHelpers(t *testing.T) {
	s := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup", nil)
	req.Header.Set("HX-Request", "true")
	s.renderSetupComplete(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Redirect") != "/" {
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
		{"/runtime/down", s.runtimeDown},
		{"/runtime/update-image", s.runtimeUpdateImage},
	} {
		rec := httptest.NewRecorder()
		target.fn(rec, httptest.NewRequest(http.MethodPost, target.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", target.path, rec.Code, rec.Body.String())
		}
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
	if rec.Code != http.StatusOK || manager.status.PublicURL != "https://runner.example" {
		t.Fatalf("runtimeRegister = %d body=%s publicURL=%q", rec.Code, rec.Body.String(), manager.status.PublicURL)
	}

	manager.stopErr = errors.New("stop failed")
	rec = httptest.NewRecorder()
	s.runtimeStop(rec, httptest.NewRequest(http.MethodPost, "/runtime/stop", nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "stop failed") {
		t.Fatalf("runtimeStop error = %d %s", rec.Code, rec.Body.String())
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
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.finishSetup(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "runtime requirement check failed") {
		t.Fatalf("finishSetup requirements = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterCurrentAndWaitForRunnerReadyBranches(t *testing.T) {
	s := newTestServer(t)
	if err := s.registerCurrent(context.Background(), map[string]string{}); err == nil {
		t.Fatal("expected registerCurrent without API key to fail")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	addr := listener.Addr().String()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()

	values := map[string]string{
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
		"CREDIMI_RUNNER_TYPE":    "android_phone",
		"RUNNER_HOST":            host,
		"RUNNER_PORT":            port,
	}
	if err := s.waitForRunnerReady(context.Background(), values); err != nil {
		t.Fatalf("waitForRunnerReady = %v", err)
	}
	<-done
}

func TestResolveRegistrationEndpointBranches(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	if _, _, err := s.resolveRegistrationEndpoint(context.Background(), map[string]string{
		"CREDIMI_SERVICE_MODE": "auto",
	}); err == nil || !strings.Contains(err.Error(), "runtime manager unavailable") {
		t.Fatalf("expected runtime manager unavailable, got %v", err)
	}

	s.manager = &fakeManager{logLines: []dashboardruntime.LogLine{
		{Message: "INF quick tunnel ready at https://runner.example.trycloudflare.com"},
	}}
	url, port, err := s.resolveRegistrationEndpoint(context.Background(), map[string]string{
		"CREDIMI_SERVICE_MODE": "auto",
	})
	if err != nil || url != "https://runner.example.trycloudflare.com" || port != "" {
		t.Fatalf("resolveRegistrationEndpoint auto = %q %q %v", url, port, err)
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
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Redirect") != "/" {
		t.Fatalf("finishSetup = %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	waitForCondition(t, func() bool { return s.manager.(*fakeManager).startCalls > 0 })
	if fm := s.manager.(*fakeManager); fm.startCalls == 0 {
		t.Fatal("finishSetup should start runtime")
	}
}

func TestServerSaveConfigDescriptionUpdateUsesAppliedFlash(t *testing.T) {
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

	form := url.Values{}
	for _, field := range Registry {
		value := s.cfg.values[field.Key]
		if field.Type == TypeBool {
			if value == "true" {
				form.Set(field.Key, "on")
			}
			continue
		}
		form.Set(field.Key, value)
	}
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
	if !strings.Contains(rec.Body.String(), "Configuration saved. Credimi registration updated.") {
		t.Fatalf("saveConfig flash missing applied result: %s", rec.Body.String())
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
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	s.finishSetup(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Redirect") != "/" {
		t.Fatalf("finishSetup registration failure = %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	waitForCondition(t, func() bool { return hasApplyClass(s.pendingDiff, dashboardruntime.ApplyCredimiUpdateRequired) })
	if fm := s.manager.(*fakeManager); fm.startCalls == 0 {
		t.Fatal("finishSetup should keep the runtime start when registration fails")
	}
	if !strings.Contains(s.lastRegistrationStatus, "Credimi registration failed") {
		t.Fatalf("lastRegistrationStatus = %q", s.lastRegistrationStatus)
	}
}

func TestServerFinishSetupKeepsStartedRuntimeWhenReadinessFails(t *testing.T) {
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
		"CREDIMI_RUNNER_TYPE":         {"android_phone"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	s.finishSetup(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Redirect") != "/" {
		t.Fatalf("finishSetup readiness failure = %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	waitForCondition(t, func() bool { return strings.Contains(s.lastRegistrationStatus, "readiness was not confirmed") })
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
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Required.") {
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

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mobile-runner" {
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
	if rec.Code != http.StatusOK {
		t.Fatalf("runtimeApply = %d body=%s", rec.Code, rec.Body.String())
	}
	fm := s.manager.(*fakeManager)
	if fm.restartCalls == 0 {
		t.Fatal("runtimeApply should restart when restart is pending")
	}
	if len(s.pendingDiff.Classes) != 0 {
		t.Fatalf("pendingDiff not cleared: %#v", s.pendingDiff)
	}
}

func TestValidateRuntimeRequirements(t *testing.T) {
	s := newTestServer(t)
	values := map[string]string{
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "auto",
		"CREDIMI_RUNNER_TYPE":    "android_phone",
	}
	if err := s.validateRuntimeRequirements(values); err != nil {
		t.Fatalf("validateRuntimeRequirements = %v", err)
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

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mobile-runner" {
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
	if rec.Code != http.StatusOK {
		t.Fatalf("runtimeStart = %d body=%s", rec.Code, rec.Body.String())
	}
	fm := s.manager.(*fakeManager)
	if fm.startCalls == 0 {
		t.Fatal("runtimeStart should start runtime")
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
func (f fakeLogManager) Down(context.Context) error        { return nil }
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
