package dashboard

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

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
	hub := NewHub(cfg, t.TempDir(), render)
	hub.snap = Snapshot{Services: []Service{{ID: "runner", Name: "runner", Status: Online}}}
	hub.workers = []Worker{{ID: "production-mr", Env: "production", Status: Online}}
	return &Server{cfg: cfg, hub: hub, render: render, composeDir: t.TempDir(), authToken: "token"}
}

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

func TestServerSaveAndFinishSetup(t *testing.T) {
	s := newTestServer(t)
	form := url.Values{
		"CREDIMI_URL":         {"https://credimi.io"},
		"CREDIMI_RUNNER_ID":   {"acme/runner"},
		"CREDIMI_RUNNER_TYPE": {"android_phone"},
		"RUNNER_PORT":         {"8050"},
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

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.finishSetup(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/" {
		t.Fatalf("finishSetup = %d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
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
