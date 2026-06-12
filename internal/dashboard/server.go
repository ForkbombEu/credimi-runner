package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
	cfg        *Config
	hub        *Hub
	render     *Renderer
	composeDir string
	authToken  string
}

// NewHandler creates the dashboard HTTP handler for mounting into an existing
// server (e.g. the credimi-runner serve command). Callers should cancel the
// returned context on shutdown to stop the background poller.
func NewHandler(composeDir string) (http.Handler, context.CancelFunc, error) {
	cfg, err := LoadConfig(composeDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	render, err := NewRenderer()
	if err != nil {
		return nil, nil, fmt.Errorf("templates: %w", err)
	}
	hub := NewHub(cfg, composeDir, render)

	srv := &Server{
		cfg: cfg, hub: hub, render: render, composeDir: composeDir,
		authToken: "", // auth handled by the parent server / Caddy
	}

	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx, 2*time.Second)

	mux := http.NewServeMux()
	srv.routes(mux)
	return mux, cancel, nil
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
	mux.HandleFunc("GET /workers", s.page("workers"))
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

	// Device actions
	mux.HandleFunc("POST /devices/connect", s.deviceConnect)
	mux.HandleFunc("POST /devices/{serial}/reconnect", s.deviceReconnect)
	mux.HandleFunc("POST /devices/{serial}/disconnect", s.deviceDisconnect)

	// SSE streams
	mux.HandleFunc("GET /events/health", s.sse("health"))
	mux.HandleFunc("GET /events/devices", s.sse("devices"))
	mux.HandleFunc("GET /events/workers", s.sse("workers"))

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
	return PageData{
		Active:   active,
		Title:    titleCase(active),
		Runner:   s.cfg,
		Snapshot: snap,
		Workers:  s.hub.CurrentWorkers(),
		Pill:     s.hub.pillData(snap),
		Data:     payload,
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
	if errs, err := s.cfg.Apply(incoming); err != nil {
		// Re-render the form with inline errors.
		d := s.pageData("config", map[string]any{"Errors": errs})
		html, _ := s.render.FragmentPage("config", d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(html))
		return
	}
	if err := WriteComposeFile(s.composeDir, s.cfg.Snapshot()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Apply restart-requiring changes by reloading the runner service.
	go s.applyRuntime()

	w.Header().Set("HX-Trigger", `{"toast":"Configuration saved · runner reloading"}`)
	d := s.pageData("config", map[string]any{"Saved": true})
	html, _ := s.render.FragmentPage("config", d)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
	normalizeWizardValues(incoming)
	if errs, err := s.cfg.Apply(incoming); err != nil {
		d := s.pageData("setup", map[string]any{"Errors": errs})
		html, _ := s.render.FragmentPage("setup", d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(html))
		return
	}
	if err := WriteComposeFile(s.composeDir, s.cfg.Snapshot()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	go s.applySetup()
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
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

// applyRuntime restarts the runner container so new .env values take effect.
func (s *Server) applyRuntime() {
	if !has("docker") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "--project-directory", s.composeDir, "up", "-d", "runner")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("apply runtime: %v: %s", err, out)
	}
}

func (s *Server) applySetup() {
	if !has("docker") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	services := ComposeServices(s.cfg.Snapshot())
	args := []string{"compose", "--env-file", filepath.Join(s.composeDir, ".env"), "-f", filepath.Join(s.composeDir, "docker-compose.yaml"), "up", "-d"}
	args = append(args, services...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("apply setup: %v: %s", err, out)
	}
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
		case "workers":
			writeSSE(w, "rows", s.render.Fragment("worker_rows", s.hub.CurrentWorkers()))
		}
		flusher.Flush()

		ping := time.NewTicker(20 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev := <-c.ch:
				writeSSE(w, ev.name, ev.data)
				flusher.Flush()
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
