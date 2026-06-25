package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// HTTP server. Stdlib net/http + Go 1.22 pattern routing. htmx for interaction,
// SSE for live status. No third-party deps.
//
// Wire into the CLI via Command(args).
// ─────────────────────────────────────────────────────────────────────────────

//go:embed static/*
var staticFS embed.FS

type Server struct {
	cfg                    *Config
	hub                    *Hub
	render                 *Renderer
	composeDir             string
	authToken              string
	manager                dashboardruntime.Manager
	runnerReady            func(context.Context, map[string]string) error
	lastRegistrationStatus string
	pendingDiff            dashboardruntime.ConfigDiff
	mu                     sync.RWMutex
}

// NewHandler creates the dashboard HTTP handler for mounting into an existing
// server (e.g. the credimi-runner serve command). Callers should cancel the
// returned context on shutdown to stop the background poller.
func NewHandler(composeDir string) (http.Handler, context.CancelFunc, error) {
	return NewHandlerWithManager(composeDir, nil)
}

func NewHandlerWithManager(composeDir string, manager dashboardruntime.Manager) (http.Handler, context.CancelFunc, error) {
	cfg, err := LoadConfig(composeDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	render, err := NewRenderer()
	if err != nil {
		return nil, nil, fmt.Errorf("templates: %w", err)
	}
	hub := NewHub(cfg, composeDir, render)
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve executable: %w", err)
	}
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(cfg.Snapshot()), runtimeGOOS())
	if err != nil {
		return nil, nil, fmt.Errorf("normalize config: %w", err)
	}
	if manager == nil {
		manager = dashboardruntime.NewLifecycleManager(executable, composeDir, normalized, nil)
	} else {
		manager.Configure(normalized)
	}

	srv := &Server{
		cfg:        cfg,
		hub:        hub,
		render:     render,
		composeDir: composeDir,
		authToken:  strings.TrimSpace(cfg.Get("DASHBOARD_TOKEN")),
		manager:    manager,
	}
	srv.runnerReady = srv.waitForRunnerReady

	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx, 2*time.Second)

	mux := http.NewServeMux()
	srv.routes(mux)
	return srv.auth(mux), cancel, nil
}

func (s *Server) routes(mux *http.ServeMux) {
	// Static files served from embedded FS.
	staticHandler, err := staticHTTPHandler()
	if err != nil {
		log.Printf("static: %v", err)
	} else {
		mux.Handle("GET /static/", staticHandler)
	}

	mux.HandleFunc("GET /{$}", s.page("overview"))
	mux.HandleFunc("GET /setup", s.page("setup"))
	mux.HandleFunc("GET /devices", s.page("devices"))
	mux.HandleFunc("GET /workers", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /network", s.page("network"))
	mux.HandleFunc("GET /config", s.page("config"))

	// Config actions
	mux.HandleFunc("POST /config", s.saveConfig)
	mux.HandleFunc("POST /setup", s.finishSetup)
	mux.HandleFunc("POST /setup/organization", s.lookupSetupOrganization)
	mux.HandleFunc("POST /setup/runner-id", s.previewSetupRunnerID)
	mux.HandleFunc("POST /setup/canonify", s.canonifySetupName)
	mux.HandleFunc("GET /config/raw", s.rawConfig)
	mux.HandleFunc("GET /config/secret/{key}", s.revealSecret)
	mux.HandleFunc("POST /runtime/start", s.runtimeStart)
	mux.HandleFunc("POST /runtime/stop", s.runtimeStop)
	mux.HandleFunc("POST /runtime/restart", s.runtimeRestart)
	mux.HandleFunc("POST /runtime/down", s.runtimeDown)
	mux.HandleFunc("POST /runtime/update-image", s.runtimeUpdateImage)
	mux.HandleFunc("POST /runtime/register", s.runtimeRegister)
	mux.HandleFunc("POST /runtime/apply", s.runtimeApply)

	// Device actions
	mux.HandleFunc("POST /devices/connect", s.deviceConnect)
	mux.HandleFunc("POST /devices/{serial}/reconnect", s.deviceReconnect)
	mux.HandleFunc("POST /devices/{serial}/disconnect", s.deviceDisconnect)

	// SSE streams
	mux.HandleFunc("GET /events/health", s.sse("health"))
	mux.HandleFunc("GET /events/devices", s.sse("devices"))
	mux.HandleFunc("GET /events/runtime", s.sse("runtime"))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
}

// staticHTTPHandler returns a handler for the embedded static directory.
func staticHTTPHandler() (http.Handler, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub))), nil
}

// auth wraps the mux with optional bearer/basic protection.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authToken == "" || strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		// Accept either Authorization: Bearer <token> or ?token=
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if got != s.authToken {
			w.Header().Set("WWW-Authenticate", `Bearer`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── page rendering ───────────────────────────────────────────────────────────

func (s *Server) pageData(active string, payload any) PageData {
	snap := s.hub.CurrentSnapshot()
	runtimeStatus := dashboardruntime.RuntimeStatus{}
	if s.manager != nil {
		runtimeStatus = s.manager.Status(context.Background())
	}
	s.mu.RLock()
	runtimeStatus.PendingRestart = hasApplyClass(s.pendingDiff, dashboardruntime.ApplyRestartRequired)
	runtimeStatus.PendingRecreate = hasApplyClass(s.pendingDiff, dashboardruntime.ApplyComposeRecreate)
	runtimeStatus.PendingCredimiUpdate = hasApplyClass(s.pendingDiff, dashboardruntime.ApplyCredimiUpdateRequired)
	flash := s.lastRegistrationStatus
	s.mu.RUnlock()
	payloadMap := map[string]any{
		"RuntimeStatus": runtimeStatus,
	}
	if flash != "" {
		payloadMap["Flash"] = flash
	}
	if p, ok := payload.(map[string]any); ok {
		for key, value := range p {
			payloadMap[key] = value
		}
	}
	return PageData{
		Active:   active,
		Title:    titleCase(active),
		Runner:   s.cfg,
		Snapshot: snap,
		Workers:  s.hub.CurrentWorkers(),
		Pill:     s.hub.pillData(snap),
		Data:     payloadMap,
	}
}

func (s *Server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if name == "overview" && !s.cfg.Exists() {
			name = "setup"
		}
		d := s.pageData(name, nil)
		var (
			html string
			err  error
		)
		if r.Header.Get("HX-Request") == "true" {
			html, err = s.render.FragmentPage(name, d)
		} else {
			html, err = s.render.Page(name, d)
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	}
}

// ── config handlers ──────────────────────────────────────────────────────────

func (s *Server) saveConfig(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	incoming := map[string]string{}
	for k, v := range r.PostForm {
		if len(v) > 0 {
			incoming[k] = v[0]
		}
	}
	oldSnapshot := s.cfg.Snapshot()
	if errs, err := s.cfg.Apply(incoming); err != nil {
		// Re-render the form with inline errors.
		d := s.pageData("config", map[string]any{"Errors": errs})
		html, _ := s.render.FragmentPage("config", d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(html))
		return
	}
	newSnapshot := s.cfg.Snapshot()
	diff := dashboardruntime.DiffValues(dashboardruntime.Values(oldSnapshot), dashboardruntime.Values(newSnapshot))
	if s.manager != nil {
		s.manager.Configure(dashboardruntime.Values(newSnapshot))
	}
	if hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) {
		if err := WriteComposeFile(s.composeDir, newSnapshot); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	s.mu.Lock()
	s.pendingDiff = diff
	s.lastRegistrationStatus = ""
	s.mu.Unlock()

	flash := flashForDiff(diff)
	d := s.pageData("config", map[string]any{"Saved": true, "Flash": flash})
	html, _ := s.render.FragmentPage("config", d)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"toast":%q}`, flash))
	w.Write([]byte(html))
}

func (s *Server) finishSetup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	incoming := map[string]string{}
	for k, v := range r.PostForm {
		if len(v) > 0 {
			incoming[k] = v[0]
		}
	}
	if errs := validateSetupInput(incoming); len(errs) > 0 {
		d := s.pageData("setup", map[string]any{"Errors": errs, "SetupError": "Some fields need attention."})
		html, _ := s.render.FragmentPage("setup", d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(html))
		return
	}
	if err := s.resolveSetupIdentity(r.Context(), incoming); err != nil {
		s.renderSetupError(w, incoming, "identity resolution failed: "+err.Error())
		return
	}
	normalizeWizardValues(incoming)
	if errs, err := s.cfg.Apply(incoming); err != nil {
		d := s.pageData("setup", map[string]any{"Errors": errs, "SetupError": "Configuration validation failed."})
		html, _ := s.render.FragmentPage("setup", d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(html))
		return
	}
	values := s.cfg.Snapshot()
	if s.manager != nil {
		s.manager.Configure(dashboardruntime.Values(values))
	}
	if err := WriteComposeFile(s.composeDir, values); err != nil {
		s.renderSetupError(w, values, "compose generation failed: "+err.Error())
		return
	}
	if s.manager != nil {
		if err := s.manager.Start(r.Context()); err != nil {
			s.renderSetupError(w, values, "runtime start failed: "+err.Error())
			return
		}
	}
	if err := s.runnerReady(r.Context(), values); err != nil {
		s.renderSetupError(w, values, "runner readiness check failed: "+err.Error())
		return
	}
	if err := s.registerCurrent(r.Context(), values); err != nil {
		s.renderSetupError(w, values, "Credimi registration failed: "+err.Error())
		return
	}
	s.mu.Lock()
	s.pendingDiff = dashboardruntime.ConfigDiff{}
	s.lastRegistrationStatus = "Setup complete. Runner started and registered with Credimi."
	s.mu.Unlock()
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func validateSetupInput(values map[string]string) map[string]string {
	errs := map[string]string{}
	if strings.TrimSpace(values["CREDIMI_URL"]) == "" {
		errs["CREDIMI_URL"] = "Required."
	}
	if strings.TrimSpace(values["CREDIMI_USER_API_KEY"]) == "" && strings.TrimSpace(values["CREDIMI_INTERNAL_ADMIN_KEY"]) == "" {
		errs["CREDIMI_USER_API_KEY"] = "Required."
	}
	if strings.TrimSpace(values["CREDIMI_RUNNER_NAME"]) == "" && strings.TrimSpace(values["CREDIMI_RUNNER_ID"]) == "" {
		errs["CREDIMI_RUNNER_NAME"] = "Required."
	}
	if strings.TrimSpace(values["CREDIMI_SERVICE_MODE"]) == "manual" && strings.TrimSpace(values["RUNNER_PUBLIC_URL"]) == "" {
		errs["RUNNER_PUBLIC_URL"] = "Required."
	}
	return errs
}

func (s *Server) renderSetupError(w http.ResponseWriter, incoming map[string]string, message string) {
	s.cfg.mu.Lock()
	for key, value := range incoming {
		s.cfg.values[key] = value
	}
	s.cfg.mu.Unlock()
	d := s.pageData("setup", map[string]any{"SetupError": message})
	html, _ := s.render.FragmentPage("setup", d)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	w.Write([]byte(html))
}

func (s *Server) lookupSetupOrganization(w http.ResponseWriter, r *http.Request) {
	var req setupCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	req.InstanceURL = strings.TrimSpace(req.InstanceURL)
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.InstanceURL == "" || req.APIKey == "" {
		http.Error(w, "Credimi URL and user API key are required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	org, err := fetchCredimiOrganization(ctx, req.InstanceURL, req.APIKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, org)
}

func (s *Server) canonifySetupName(w http.ResponseWriter, r *http.Request) {
	var req setupCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	req.InstanceURL = strings.TrimSpace(req.InstanceURL)
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.InstanceURL == "" || req.APIKey == "" {
		http.Error(w, "Credimi URL and user API key are required", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "name query parameter is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	slug, err := fetchCredimiCanonify(ctx, req.InstanceURL, req.APIKey, name)
	if err != nil {
		slug = canonifyPlain(name)
	}
	writeJSON(w, map[string]string{"canonified": slug})
}

func (s *Server) previewSetupRunnerID(w http.ResponseWriter, r *http.Request) {
	var req setupRunnerPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	req.InstanceURL = strings.TrimSpace(req.InstanceURL)
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.Organization = strings.TrimSpace(req.Organization)
	req.Name = strings.TrimSpace(req.Name)
	if req.InstanceURL == "" || req.APIKey == "" || req.Organization == "" || req.Name == "" {
		http.Error(w, "Credimi URL, API key, organization, and runner name are required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	preview, err := fetchCredimiRunnerPreview(ctx, req)
	if err != nil {
		preview = setupRunnerPreview{
			Organization: req.Organization,
			RunnerID:     req.Organization + "/" + canonifyPlain(req.Name),
		}
	}
	writeJSON(w, preview)
}

func (s *Server) runtimeStart(w http.ResponseWriter, r *http.Request) {
	s.runtimeAction(w, r, "overview", func(ctx context.Context) error {
		if s.manager == nil {
			return nil
		}
		values := s.cfg.Snapshot()
		if err := s.manager.Start(ctx); err != nil {
			return err
		}
		if err := s.runnerReady(ctx, values); err != nil {
			return err
		}
		return s.registerCurrent(ctx, values)
	}, "Runtime started.")
}

func (s *Server) runtimeStop(w http.ResponseWriter, r *http.Request) {
	s.runtimeAction(w, r, "overview", func(ctx context.Context) error {
		if s.manager == nil {
			return nil
		}
		return s.manager.Stop(ctx)
	}, "Runtime stopped.")
}

func (s *Server) runtimeRestart(w http.ResponseWriter, r *http.Request) {
	s.runtimeAction(w, r, "overview", func(ctx context.Context) error {
		if s.manager == nil {
			return nil
		}
		return s.manager.Restart(ctx)
	}, "Runtime restarted.")
}

func (s *Server) runtimeDown(w http.ResponseWriter, r *http.Request) {
	s.runtimeAction(w, r, "overview", func(ctx context.Context) error {
		if s.manager == nil {
			return nil
		}
		return s.manager.Down(ctx)
	}, "Runtime brought down.")
}

func (s *Server) runtimeUpdateImage(w http.ResponseWriter, r *http.Request) {
	s.runtimeAction(w, r, "overview", func(ctx context.Context) error {
		if s.manager == nil {
			return nil
		}
		return s.manager.UpdateImage(ctx)
	}, "Runner image updated.")
}

func (s *Server) runtimeRegister(w http.ResponseWriter, r *http.Request) {
	s.runtimeAction(w, r, "overview", func(ctx context.Context) error {
		return s.registerCurrent(ctx, s.cfg.Snapshot())
	}, "Credimi runner registration updated.")
}

func (s *Server) runtimeApply(w http.ResponseWriter, r *http.Request) {
	s.runtimeAction(w, r, "overview", func(ctx context.Context) error {
		s.mu.RLock()
		diff := s.pendingDiff
		s.mu.RUnlock()
		values := s.cfg.Snapshot()
		if s.manager != nil {
			s.manager.Configure(dashboardruntime.Values(values))
		}
		if hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) {
			if err := WriteComposeFile(s.composeDir, values); err != nil {
				return err
			}
			if s.manager != nil {
				if err := s.manager.Down(ctx); err != nil {
					return err
				}
				if err := s.manager.Start(ctx); err != nil {
					return err
				}
			}
		} else if hasApplyClass(diff, dashboardruntime.ApplyRestartRequired) {
			if s.manager != nil {
				if err := s.manager.Restart(ctx); err != nil {
					return err
				}
			}
		}
		if hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) || hasApplyClass(diff, dashboardruntime.ApplyRestartRequired) {
			if err := s.runnerReady(ctx, values); err != nil {
				return err
			}
		}
		if hasApplyClass(diff, dashboardruntime.ApplyCredimiUpdateRequired) {
			if err := s.registerCurrent(ctx, values); err != nil {
				return err
			}
		}
		s.mu.Lock()
		s.pendingDiff = dashboardruntime.ConfigDiff{}
		s.mu.Unlock()
		return nil
	}, "Pending changes applied.")
}

func (s *Server) runtimeAction(w http.ResponseWriter, r *http.Request, page string, action func(context.Context) error, success string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := action(ctx); err != nil {
		d := s.pageData(page, map[string]any{"Flash": err.Error()})
		html, _ := s.render.FragmentPage(page, d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(html))
		return
	}
	s.mu.Lock()
	s.lastRegistrationStatus = success
	s.mu.Unlock()
	s.hub.poll(ctx)
	d := s.pageData(page, map[string]any{"Flash": success})
	html, _ := s.render.FragmentPage(page, d)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"toast":%q}`, success))
	w.Write([]byte(html))
}

func (s *Server) rawConfig(w http.ResponseWriter, r *http.Request) {
	mask := r.URL.Query().Get("reveal") != "1"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(s.cfg.RawEnv(mask)))
}

// revealSecret returns the cleartext for one key (authed session only).
func (s *Server) revealSecret(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	f, ok := fieldByKey[key]
	if !ok || !f.Secret {
		http.Error(w, "not a secret", 400)
		return
	}
	// Return as the input element htmx swaps in, value revealed.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<input class="inp mono" name="%s" value="%s">`,
		key, htmlAttr(s.cfg.Get(key)))
}

func (s *Server) resolveSetupIdentity(ctx context.Context, values map[string]string) error {
	apiKey := strings.TrimSpace(values["CREDIMI_USER_API_KEY"])
	if apiKey == "" {
		apiKey = strings.TrimSpace(values["CREDIMI_INTERNAL_ADMIN_KEY"])
	}
	if apiKey == "" {
		return errors.New("a Credimi API key is required")
	}

	baseURL := strings.TrimSpace(values["CREDIMI_URL"])
	organization := strings.TrimSpace(values["CREDIMI_RUNNER_ORGANIZATION"])
	client := &dashboardruntime.CredimiClient{BaseURL: baseURL, APIKey: apiKey, HTTPClient: http.DefaultClient}

	if organization == "" {
		if strings.TrimSpace(values["CREDIMI_USER_API_KEY"]) != "" {
			org, err := client.MyOrganization(ctx)
			if err != nil {
				return err
			}
			organization = org.Namespace
			values["CREDIMI_RUNNER_ORGANIZATION"] = organization
		} else {
			return errors.New("organization is required when using an internal admin key")
		}
	}

	if strings.TrimSpace(values["CREDIMI_RUNNER_ID"]) == "" {
		name := strings.TrimSpace(values["CREDIMI_RUNNER_NAME"])
		if name == "" {
			return errors.New("runner name is required")
		}
		preview, err := client.PreviewRunnerID(ctx, name, organization)
		if err != nil {
			return err
		}
		values["CREDIMI_RUNNER_ID"] = preview.RunnerID
	}

	if strings.TrimSpace(values["OTEL_SERVICE_NAME"]) == "" {
		values["OTEL_SERVICE_NAME"] = values["CREDIMI_RUNNER_ID"]
	}
	return nil
}

func (s *Server) registerCurrent(ctx context.Context, values map[string]string) error {
	apiKey := strings.TrimSpace(values["CREDIMI_USER_API_KEY"])
	if apiKey == "" {
		apiKey = strings.TrimSpace(values["CREDIMI_INTERNAL_ADMIN_KEY"])
	}
	if apiKey == "" {
		return errors.New("missing Credimi API key")
	}

	publicURL, publicPort, err := s.resolveRegistrationEndpoint(ctx, values)
	if err != nil {
		return err
	}
	client := &dashboardruntime.CredimiClient{
		BaseURL:    strings.TrimSpace(values["CREDIMI_URL"]),
		APIKey:     apiKey,
		HTTPClient: http.DefaultClient,
	}
	req := dashboardruntime.RegisterRunnerRequest{
		RunnerID:     strings.TrimSpace(values["CREDIMI_RUNNER_ID"]),
		Name:         strings.TrimSpace(values["CREDIMI_RUNNER_NAME"]),
		IP:           publicURL,
		Description:  strings.TrimSpace(values["CREDIMI_RUNNER_DESCRIPTION"]),
		Type:         strings.TrimSpace(values["CREDIMI_RUNNER_TYPE"]),
		Port:         publicPort,
		Serial:       strings.TrimSpace(values["CREDIMI_RUNNER_SERIAL"]),
		Organization: strings.TrimSpace(values["CREDIMI_RUNNER_ORGANIZATION"]),
	}
	if err := client.RegisterMobileRunner(ctx, req); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastRegistrationStatus = "Credimi runner registration updated."
	s.mu.Unlock()
	return nil
}

func (s *Server) waitForRunnerReady(ctx context.Context, values map[string]string) error {
	host := strings.TrimSpace(values["RUNNER_HOST"])
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := strings.TrimSpace(values["RUNNER_PORT"])
	if port == "" {
		port = dashboardruntime.DefaultRunnerPort
	}
	endpoint := "http://" + net.JoinHostPort(host, port) + "/healthz"
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(deadline, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("runner did not become ready at %s: %w", endpoint, deadline.Err())
		case <-ticker.C:
		}
	}
}

func (s *Server) resolveRegistrationEndpoint(ctx context.Context, values map[string]string) (string, string, error) {
	switch strings.TrimSpace(values["CREDIMI_SERVICE_MODE"]) {
	case "manual":
		url := strings.TrimSpace(values["RUNNER_PUBLIC_URL"])
		if url == "" {
			return "", "", errors.New("RUNNER_PUBLIC_URL is required for manual service mode")
		}
		return url, strings.TrimSpace(values["RUNNER_PUBLIC_PORT"]), nil
	case "cloudflare-managed":
		domain := strings.TrimSpace(values["RUNNER_DOMAIN"])
		if domain == "" {
			return "", "", errors.New("RUNNER_DOMAIN is required for managed tunnel mode")
		}
		if !strings.Contains(domain, "://") {
			domain = "https://" + domain
		}
		return domain, "", nil
	default:
		if s.manager == nil {
			return "", "", errors.New("runtime manager unavailable")
		}
		re := regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.trycloudflare\.com`)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var lastErr error
		for {
			logs, err := s.manager.Logs(deadline, 200)
			if err != nil {
				lastErr = err
			} else {
				for i := len(logs) - 1; i >= 0; i-- {
					if found := re.FindString(logs[i].Message); found != "" {
						return found, "", nil
					}
				}
				lastErr = errors.New("no trycloudflare URL found in runtime logs")
			}
			select {
			case <-deadline.Done():
				return "", "", lastErr
			case <-ticker.C:
			}
		}
	}
}

func flashForDiff(diff dashboardruntime.ConfigDiff) string {
	switch {
	case len(diff.Classes) == 1 && diff.Classes[0] == dashboardruntime.ApplySavedOnly:
		return "Configuration saved."
	case hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) && hasApplyClass(diff, dashboardruntime.ApplyCredimiUpdateRequired):
		return "Configuration saved. Runtime must be recreated and Credimi registration must be updated."
	case hasApplyClass(diff, dashboardruntime.ApplyRestartRequired) && hasApplyClass(diff, dashboardruntime.ApplyCredimiUpdateRequired):
		return "Configuration saved. Runner restart required and Credimi registration must be updated."
	case hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate):
		return "Configuration saved. Runtime must be recreated before this takes effect."
	case hasApplyClass(diff, dashboardruntime.ApplyRestartRequired):
		return "Configuration saved. Runner restart required before this takes effect."
	case hasApplyClass(diff, dashboardruntime.ApplyCredimiUpdateRequired):
		return "Configuration saved. Credimi runner registration must be updated."
	default:
		return "Configuration saved."
	}
}

func hasApplyClass(diff dashboardruntime.ConfigDiff, class dashboardruntime.ApplyClass) bool {
	for _, candidate := range diff.Classes {
		if candidate == class {
			return true
		}
	}
	return false
}

// ── device handlers ──────────────────────────────────────────────────────────

func (s *Server) deviceConnect(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	dtype := r.PostForm.Get("type")
	mode := r.PostForm.Get("mode")
	addr := strings.TrimSpace(r.PostForm.Get("address"))
	pair := strings.TrimSpace(r.PostForm.Get("pair_code"))

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var msg string
	switch {
	case dtype == "android_phone" && mode == "wifi":
		if pair != "" {
			if out, err := run(ctx, "adb", "pair", addr, pair); err != nil {
				s.deviceError(w, "adb pair failed: "+strings.TrimSpace(out)+err.Error())
				return
			}
		}
		if out, err := run(ctx, "adb", "connect", addr); err != nil {
			s.deviceError(w, "adb connect failed: "+strings.TrimSpace(out))
			return
		}
		msg = "Connected " + addr
	case dtype == "android_phone" && mode == "usb":
		msg = "USB devices auto-detected"
	case dtype == "ios_simulator":
		if addr != "" {
			run(ctx, "xcrun", "simctl", "boot", addr)
		}
		msg = "Simulator booting"
	case dtype == "android_emulator":
		msg = "Emulator boot requested"
	default:
		msg = "Device queued"
	}
	s.hub.poll(ctx) // refresh snapshot immediately
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"toast":%q,"closeModal":true}`, msg))
	w.Write([]byte(s.render.Fragment("device_rows", s.hub.CurrentSnapshot().Devices)))
}

func (s *Server) deviceReconnect(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if strings.Contains(serial, ":") {
		run(ctx, "adb", "connect", serial)
	}
	s.hub.poll(ctx)
	w.Write([]byte(s.render.Fragment("device_rows", s.hub.CurrentSnapshot().Devices)))
}

func (s *Server) deviceDisconnect(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if strings.Contains(serial, ":") {
		run(ctx, "adb", "disconnect", serial)
	}
	s.hub.poll(ctx)
	w.Write([]byte(s.render.Fragment("device_rows", s.hub.CurrentSnapshot().Devices)))
}

func (s *Server) deviceError(w http.ResponseWriter, msg string) {
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"toast":%q}`, msg))
	w.WriteHeader(http.StatusUnprocessableEntity)
	w.Write([]byte(`<div class="callout danger" style="margin:14px 0 0">` + htmlAttr(msg) + `</div>`))
}

// ── SSE ──────────────────────────────────────────────────────────────────────

func (s *Server) sse(stream string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		c := &client{ch: make(chan event, 8), stream: stream}
		s.hub.add(c)
		defer s.hub.remove(c)

		// Prime the client with the current state.
		switch stream {
		case "health":
			writeSSE(w, "pill", s.render.Fragment("pill", s.hub.pillData(s.hub.CurrentSnapshot())))
		case "devices":
			writeSSE(w, "rows", s.render.Fragment("device_rows", s.hub.CurrentSnapshot().Devices))
		case "runtime":
			writeSSE(w, "runtime", s.render.Fragment("runtime_status", s.pageData("overview", nil)))
		case "workers":
			writeSSE(w, "rows", s.render.Fragment("worker_rows", s.hub.CurrentWorkers()))
		}
		flusher.Flush()

		ping := time.NewTicker(20 * time.Second)
		runtimeTick := time.NewTicker(2 * time.Second)
		defer ping.Stop()
		defer runtimeTick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev := <-c.ch:
				writeSSE(w, ev.name, ev.data)
				flusher.Flush()
			case <-runtimeTick.C:
				if stream == "runtime" {
					writeSSE(w, "runtime", s.render.Fragment("runtime_status", s.pageData("overview", nil)))
					flusher.Flush()
				}
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, name, data string) {
	fmt.Fprintf(w, "event: %s\n", name)
	// SSE: each line of the payload needs its own data: prefix.
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}

func htmlAttr(s string) string {
	r := strings.NewReplacer(`&`, "&amp;", `<`, "&lt;", `>`, "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func runtimeGOOS() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("GOOS_OVERRIDE")))
}
