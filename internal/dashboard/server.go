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
	"net/url"
	"os"
	"os/exec"
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

var (
	lookupPath = exec.LookPath
	statPath   = os.Stat
)

type Server struct {
	cfg                    *Config
	hub                    *Hub
	render                 *Renderer
	composeDir             string
	ctx                    context.Context
	authToken              string
	manager                dashboardruntime.Manager
	runnerReady            func(context.Context, map[string]string) error
	lookupPath             func(string) (string, error)
	statPath               func(string) (os.FileInfo, error)
	lastRegistrationStatus string
	pendingDiff            dashboardruntime.ConfigDiff
	startup                startupState
	mu                     sync.RWMutex
}

type StartupPhase string

const (
	StartupIdle           StartupPhase = "idle"
	StartupStarting       StartupPhase = "starting"
	StartupWaitingRunner  StartupPhase = "waiting_for_runner"
	StartupRegistering    StartupPhase = "registering"
	StartupReady          StartupPhase = "ready"
	StartupNeedsAttention StartupPhase = "needs_attention"
)

const quickTunnelLogTail = -1000

type startupState struct {
	Phase   StartupPhase
	Message string
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewHandler creates the dashboard HTTP handler for mounting into an existing
// server (e.g. the credimi-runner serve command). Callers should cancel the
// returned context on shutdown to stop the background poller.
func NewHandler(composeDir string) (http.Handler, context.CancelFunc, error) {
	return NewHandlerWithManagerContext(context.Background(), composeDir, nil)
}

func NewHandlerWithManager(composeDir string, manager dashboardruntime.Manager) (http.Handler, context.CancelFunc, error) {
	return NewHandlerWithManagerContext(context.Background(), composeDir, manager)
}

func NewHandlerWithManagerContext(parent context.Context, composeDir string, manager dashboardruntime.Manager) (http.Handler, context.CancelFunc, error) {
	cfg, err := LoadConfig(composeDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	render, err := NewRenderer()
	if err != nil {
		return nil, nil, fmt.Errorf("templates: %w", err)
	}
	hub := NewHub(cfg, composeDir, render, func() dashboardruntime.RuntimeStatus {
		if manager == nil {
			return dashboardruntime.RuntimeStatus{}
		}
		return manager.Status(context.Background())
	})
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve executable: %w", err)
	}
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(cfg.Snapshot()), runtimeGOOS())
	if err != nil {
		return nil, nil, fmt.Errorf("normalize config: %w", err)
	}
	cfg.mu.Lock()
	cfg.values = map[string]string(normalized)
	cfg.mu.Unlock()
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
		ctx:        parent,
		authToken:  strings.TrimSpace(cfg.Get("DASHBOARD_TOKEN")),
		manager:    manager,
		lookupPath: lookupPath,
		statPath:   statPath,
		startup: startupState{
			Phase: StartupIdle,
		},
	}
	srv.runnerReady = srv.waitForRunnerReady

	ctx, cancel := context.WithCancel(parent)
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
	mux.HandleFunc("GET /workers", s.page("workers"))
	mux.HandleFunc("GET /network", s.page("network"))
	mux.HandleFunc("GET /config", s.page("config"))

	// Config actions
	mux.HandleFunc("POST /config", s.saveConfig)
	mux.HandleFunc("POST /overview/config", s.saveOverviewConfig)
	mux.HandleFunc("POST /config/diff", s.configDiff)
	mux.HandleFunc("POST /config/normalize", s.normalizeConfigPreview)
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
	mux.HandleFunc("GET /runtime/logs", s.runtimeLogs)

	// Device actions
	mux.HandleFunc("POST /devices/config", s.saveDevicesConfig)
	mux.HandleFunc("GET /devices/ios-simulator/status", s.iosSimulatorStatus)
	mux.HandleFunc("POST /devices/ios-simulator/create", s.iosSimulatorCreate)
	mux.HandleFunc("GET /devices/android-emulator/assets/status", s.androidEmulatorAssetsStatus)
	mux.HandleFunc("POST /devices/android-emulator/assets/select", s.androidEmulatorAssetsSelect)
	mux.HandleFunc("POST /devices/android-emulator/assets/download", s.androidEmulatorAssetsDownload)
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
		"Startup":       s.startupSnapshot(),
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
		pageName := name
		if pageName == "overview" && !s.cfg.Exists() {
			pageName = "setup"
		}
		d := s.pageData(pageName, nil)
		var (
			html string
			err  error
		)
		if r.Header.Get("HX-Request") == "true" {
			html, err = s.render.FragmentPage(pageName, d)
		} else {
			html, err = s.render.Page(pageName, d)
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	}
}

func formValuesMap(values url.Values) map[string]string {
	out := make(map[string]string, len(values))
	for key, candidates := range values {
		if len(candidates) == 0 {
			continue
		}
		chosen := candidates[len(candidates)-1]
		for i := len(candidates) - 1; i >= 0; i-- {
			if strings.TrimSpace(candidates[i]) != "" {
				chosen = candidates[i]
				break
			}
		}
		out[key] = chosen
	}
	return out
}

// ── config handlers ──────────────────────────────────────────────────────────

func (s *Server) saveConfig(w http.ResponseWriter, r *http.Request) {
	s.saveConfigPage(w, r, "config")
}

func (s *Server) saveOverviewConfig(w http.ResponseWriter, r *http.Request) {
	s.saveConfigPage(w, r, "overview")
}

func (s *Server) saveDevicesConfig(w http.ResponseWriter, r *http.Request) {
	s.saveConfigPage(w, r, "devices")
}

func (s *Server) saveConfigPage(w http.ResponseWriter, r *http.Request, page string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	incoming := formValuesMap(r.PostForm)
	oldSnapshot := s.cfg.Snapshot()
	if err := s.resolveConfigIdentity(r.Context(), oldSnapshot, incoming); err != nil {
		d := s.pageData(page, map[string]any{"Errors": map[string]string{"CREDIMI_RUNNER_NAME": err.Error()}})
		html, _ := s.render.FragmentPage(page, d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(html))
		return
	}
	if errs, err := s.cfg.Apply(incoming); err != nil {
		d := s.pageData(page, map[string]any{"Errors": errs})
		html, _ := s.render.FragmentPage(page, d)
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
	message := "Configuration updated."
	appliedCleanly := true
	if s.manager != nil {
		applyCtx, applyCancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer applyCancel()
		if outcome, err := s.applySavedConfig(applyCtx, diff, newSnapshot); err != nil {
			message = "Configuration update failed: " + err.Error()
			appliedCleanly = false
		} else if outcome.Restarted {
			message = "Runner restarted with the new configuration."
		}
	} else {
		message = saveSuccessMessage(applyOutcome{})
	}
	s.mu.Lock()
	if appliedCleanly {
		s.pendingDiff = dashboardruntime.ConfigDiff{}
	} else {
		s.pendingDiff = diff
	}
	s.lastRegistrationStatus = ""
	s.mu.Unlock()

	d := s.pageData(page, map[string]any{"Saved": true})
	html, _ := s.render.FragmentPage(page, d)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"toast":%q}`, message))
	w.Write([]byte(html))
}

func (s *Server) configDiff(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	incoming := formValuesMap(r.PostForm)
	current := s.cfg.Snapshot()
	if err := s.resolveConfigIdentity(r.Context(), current, incoming); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	normalized, err := normalizedConfigValues(current, incoming, runtimeGOOS())
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	diff := dashboardruntime.DiffValues(dashboardruntime.Values(current), normalized)
	confirmRequired := diffRequiresRuntimeRestart(diff)
	writeJSON(w, map[string]any{
		"classes":          diff.Classes,
		"confirm_required": confirmRequired,
		"message":          describeDiffImpact(diff),
	})
}

func (s *Server) normalizeConfigPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	incoming := formValuesMap(r.PostForm)
	normalized, err := normalizedConfigValues(s.cfg.Snapshot(), incoming, runtimeGOOS())
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]any{"values": normalized})
}

type applyOutcome struct {
	Restarted      bool
	CredimiUpdated bool
}

func (s *Server) applySavedConfig(ctx context.Context, diff dashboardruntime.ConfigDiff, values map[string]string) (applyOutcome, error) {
	var outcome applyOutcome
	if len(diff.ChangedKeys) == 0 || hasApplyClass(diff, dashboardruntime.ApplySavedOnly) {
		return outcome, nil
	}
	status := s.manager.Status(ctx)
	runtimeRunning := status.RunnerRunning || status.ComposeRunning
	if runtimeRunning && (hasApplyClass(diff, dashboardruntime.ApplyRestartRequired) || hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate)) {
		if normalizedApplyServiceMode(values["CREDIMI_SERVICE_MODE"]) == "auto" {
			s.manager.SetPublicURL("")
		}
		if err := s.manager.Restart(ctx); err != nil {
			return outcome, err
		}
		outcome.Restarted = true
	}
	if shouldRegisterAfterApply(diff, values, outcome.Restarted) {
		if err := s.registerCurrent(ctx, values); err != nil {
			return outcome, err
		}
		outcome.CredimiUpdated = true
	}
	return outcome, nil
}

func shouldRegisterAfterApply(diff dashboardruntime.ConfigDiff, values map[string]string, restarted bool) bool {
	if hasApplyClass(diff, dashboardruntime.ApplyCredimiUpdateRequired) {
		return true
	}
	return restarted && normalizedApplyServiceMode(values["CREDIMI_SERVICE_MODE"]) == "auto"
}

func normalizedApplyServiceMode(value string) string {
	switch strings.TrimSpace(value) {
	case "quick":
		return "auto"
	case "direct":
		return "manual"
	case "named":
		return "cloudflare-managed"
	case "auto", "cloudflare-managed", "manual":
		return strings.TrimSpace(value)
	default:
		return "auto"
	}
}

func (s *Server) finishSetup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	incoming := formValuesMap(r.PostForm)
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
	if err := s.validateRuntimeRequirements(incoming); err != nil {
		s.renderSetupError(w, incoming, "runtime requirement check failed: "+err.Error())
		return
	}
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
	plan := dashboardruntime.BuildRuntimePlan(s.composeDir, dashboardruntime.Values(values))
	if len(plan.ComposeServices) > 0 {
		if err := WriteComposeFile(s.composeDir, values); err != nil {
			s.renderSetupError(w, values, "compose generation failed: "+err.Error())
			return
		}
	}
	s.startStartupJob(values)
	s.renderSetupComplete(w, r)
}

func (s *Server) renderSetupComplete(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Server) startStartupJob(values map[string]string) {
	s.mu.Lock()
	waitFor := s.startup.done
	if s.startup.running && s.startup.cancel != nil {
		s.startup.cancel()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.startup.running = true
	s.startup.cancel = cancel
	s.startup.done = done
	s.startup.Phase = StartupStarting
	s.startup.Message = "Setup saved. Starting runtime."
	s.lastRegistrationStatus = s.startup.Message
	s.mu.Unlock()

	s.hub.poll(context.Background())
	go func(wait <-chan struct{}, values map[string]string, ctx context.Context, done chan struct{}) {
		if wait != nil {
			<-wait
		}
		defer close(done)
		s.runStartupJob(ctx, values)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.startup.done == done {
			s.startup.running = false
			s.startup.cancel = nil
		}
	}(waitFor, cloneStringMap(values), ctx, done)
}

func (s *Server) runStartupJob(ctx context.Context, values map[string]string) {
	if s.manager == nil {
		s.setStartupState(StartupReady, "Setup saved.")
		return
	}
	if err := s.manager.Start(ctx); err != nil {
		s.setStartupState(StartupNeedsAttention, "Setup saved, but runtime start failed: "+err.Error())
		return
	}
	s.hub.poll(context.Background())
	if dashboardruntime.RunnerReadinessRequiredBeforeRegistration(dashboardruntime.Values(values), runtimeGOOS()) {
		s.setStartupState(StartupWaitingRunner, "Runtime started. Waiting for runner readiness.")
		readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.runnerReady(readyCtx, values)
		cancel()
		if err != nil {
			s.setStartupState(StartupNeedsAttention, "Setup saved, but runner readiness was not confirmed: "+s.runtimeStartupError(context.Background(), err).Error())
			return
		}
	}
	s.setStartupState(StartupRegistering, "Runtime started. Updating Credimi registration.")
	registerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := s.registerCurrent(registerCtx, values)
	cancel()
	if err != nil {
		s.mu.Lock()
		s.pendingDiff = dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyCredimiUpdateRequired}}
		s.mu.Unlock()
		s.setStartupState(StartupNeedsAttention, "Setup saved, but Credimi registration failed: "+err.Error())
		return
	}
	s.mu.Lock()
	s.pendingDiff = dashboardruntime.ConfigDiff{}
	s.mu.Unlock()
	s.setStartupState(StartupReady, "Setup complete. Runner started and registered with Credimi.")
}

func (s *Server) setStartupState(phase StartupPhase, message string) {
	s.mu.Lock()
	s.startup.Phase = phase
	s.startup.Message = message
	s.lastRegistrationStatus = message
	s.mu.Unlock()
	s.hub.poll(context.Background())
}

func (s *Server) startupSnapshot() startupState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return startupState{
		Phase:   s.startup.Phase,
		Message: s.startup.Message,
		running: s.startup.running,
	}
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
	if strings.TrimSpace(values["CREDIMI_RUNNER_TYPE"]) == "android_phone" &&
		strings.TrimSpace(values["CREDIMI_RUNNER_DEVICE_MODE"]) != "wifi" &&
		strings.TrimSpace(values["CREDIMI_RUNNER_SERIAL"]) == "" {
		errs["CREDIMI_RUNNER_SERIAL"] = "Select a connected Android device."
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

func (s *Server) validateRuntimeRequirements(values map[string]string) error {
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(values), runtimeGOOS())
	if err != nil {
		return err
	}
	plan := dashboardruntime.BuildRuntimePlan(s.composeDir, normalized)
	if plan.RequiresDocker {
		if _, err := s.lookupPath("docker"); err != nil {
			return errors.New("docker is required for this runner mode")
		}
	}
	if normalized["CREDIMI_RUNNER_TYPE"] == "ios_simulator" {
		if _, err := s.lookupPath("xcrun"); err != nil {
			return errors.New("xcrun simctl is required for iOS simulator runners")
		}
	}
	if normalized["CREDIMI_RUNNER_TYPE"] == "android_phone" &&
		normalized["CREDIMI_RUNNER_DEVICE_MODE"] == "usb" &&
		!s.androidPhoneSerialConnected(normalized["CREDIMI_RUNNER_SERIAL"]) {
		return errors.New("select a connected Android device")
	}
	if normalized["CREDIMI_RUNNER_TYPE"] == "android_emulator" && plan.Backend == dashboardruntime.DefaultContainerBackend {
		if _, err := s.statPath("/dev/kvm"); err != nil {
			return errors.New("/dev/kvm is required for Android emulator containers")
		}
		for _, path := range []string{
			strings.TrimSpace(normalized["ANDROID_KEYS_DIR"]),
			strings.TrimSpace(normalized["HOST_AVD_HOME_PATH"]),
			strings.TrimSpace(normalized["HOST_AVD_GOLDEN_PATH"]),
		} {
			if path == "" {
				return errors.New("android emulator assets are not configured")
			}
			if _, err := s.statPath(path); err != nil {
				return fmt.Errorf("required emulator asset path is missing: %s", path)
			}
		}
	}
	return nil
}

func (s *Server) androidPhoneSerialConnected(serial string) bool {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return false
	}
	for _, device := range s.hub.CurrentSnapshot().Devices {
		if device.Type == "android_phone" && device.Status == Online && device.Serial == serial {
			return true
		}
	}
	return false
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
			Organization:    req.Organization,
			BaseRunnerID:    req.Organization + "/" + canonifyPlain(req.Name),
			PreviewRunnerID: req.Organization + "/" + canonifyPlain(req.Name),
			RunnerID:        req.Organization + "/" + canonifyPlain(req.Name),
			DefaultAction:   "update",
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
		if dashboardruntime.RunnerReadinessRequiredBeforeRegistration(dashboardruntime.Values(values), runtimeGOOS()) {
			if err := s.runnerReady(ctx, values); err != nil {
				return s.runtimeStartupError(ctx, err)
			}
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

func (s *Server) runtimeLogs(w http.ResponseWriter, r *http.Request) {
	if s.manager == nil {
		writeJSON(w, map[string]any{"lines": []string{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	logs, err := s.manager.Logs(ctx, 80)
	if err != nil {
		writeJSON(w, map[string]any{"lines": []string{"runtime logs unavailable: " + err.Error()}})
		return
	}
	lines := make([]string, 0, len(logs))
	for _, line := range logs {
		message := strings.TrimSpace(line.Message)
		if message != "" {
			lines = append(lines, message)
		}
	}
	writeJSON(w, map[string]any{"lines": lines})
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
		restarted := false
		if hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) {
			if err := WriteComposeFile(s.composeDir, values); err != nil {
				return err
			}
			if s.manager != nil {
				if normalizedApplyServiceMode(values["CREDIMI_SERVICE_MODE"]) == "auto" {
					s.manager.SetPublicURL("")
				}
				if err := s.manager.Down(ctx); err != nil {
					return err
				}
				if err := s.manager.Start(ctx); err != nil {
					return err
				}
				restarted = true
			}
		} else if hasApplyClass(diff, dashboardruntime.ApplyRestartRequired) {
			if s.manager != nil {
				if normalizedApplyServiceMode(values["CREDIMI_SERVICE_MODE"]) == "auto" {
					s.manager.SetPublicURL("")
				}
				if err := s.manager.Restart(ctx); err != nil {
					return err
				}
				restarted = true
			}
		}
		if hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) || hasApplyClass(diff, dashboardruntime.ApplyRestartRequired) {
			if dashboardruntime.RunnerReadinessRequiredBeforeRegistration(dashboardruntime.Values(values), runtimeGOOS()) {
				if err := s.runnerReady(ctx, values); err != nil {
					return err
				}
			}
		}
		if shouldRegisterAfterApply(diff, values, restarted) {
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

func (s *Server) resolveConfigIdentity(ctx context.Context, current, incoming map[string]string) error {
	currentID := strings.TrimSpace(current["CREDIMI_RUNNER_ID"])
	if currentID == "" {
		return nil
	}
	incoming["CREDIMI_RUNNER_ID"] = currentID

	currentName := strings.TrimSpace(current["CREDIMI_RUNNER_NAME"])
	currentOrg := strings.TrimSpace(current["CREDIMI_RUNNER_ORGANIZATION"])
	nextName := strings.TrimSpace(currentName)
	if value, ok := incoming["CREDIMI_RUNNER_NAME"]; ok {
		nextName = strings.TrimSpace(value)
	}
	nextOrg := currentOrg
	if value, ok := incoming["CREDIMI_RUNNER_ORGANIZATION"]; ok {
		nextOrg = strings.TrimSpace(value)
	}

	if strings.TrimSpace(current["CREDIMI_INTERNAL_ADMIN_KEY"]) == "" {
		if nextOrg != "" && currentOrg != "" && nextOrg != currentOrg {
			return errors.New("organization cannot be changed for user-scoped runners")
		}
		if currentOrg != "" {
			nextOrg = currentOrg
			incoming["CREDIMI_RUNNER_ORGANIZATION"] = currentOrg
		}
	}

	if nextName == currentName && nextOrg == currentOrg {
		return nil
	}
	if nextName == "" {
		return errors.New("runner name is required")
	}
	if nextOrg == "" {
		return errors.New("organization is required")
	}

	apiKey := strings.TrimSpace(incoming["CREDIMI_USER_API_KEY"])
	if apiKey == "" {
		apiKey = strings.TrimSpace(current["CREDIMI_USER_API_KEY"])
	}
	if adminKey := strings.TrimSpace(incoming["CREDIMI_INTERNAL_ADMIN_KEY"]); adminKey != "" {
		apiKey = adminKey
	} else if currentAdminKey := strings.TrimSpace(current["CREDIMI_INTERNAL_ADMIN_KEY"]); currentAdminKey != "" {
		apiKey = currentAdminKey
	}
	if apiKey == "" {
		return errors.New("a Credimi API key is required to update runner identity")
	}

	baseURL := strings.TrimSpace(incoming["CREDIMI_URL"])
	if baseURL == "" {
		baseURL = strings.TrimSpace(current["CREDIMI_URL"])
	}
	client := &dashboardruntime.CredimiClient{BaseURL: baseURL, APIKey: apiKey, HTTPClient: http.DefaultClient}
	preview, err := client.PreviewRunnerID(ctx, nextName, nextOrg)
	if err != nil {
		return err
	}
	runnerID := strings.TrimPrefix(strings.TrimSpace(preview.RunnerID), "/")
	if runnerID == "" {
		runnerID = nextOrg + "/" + canonifyPlain(nextName)
	}
	incoming["CREDIMI_RUNNER_NAME"] = nextName
	incoming["CREDIMI_RUNNER_ORGANIZATION"] = nextOrg
	incoming["CREDIMI_RUNNER_ID"] = runnerID
	return nil
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
		baseRunnerID := organization + "/" + canonifyPlain(name)
		previewRunnerID := strings.TrimPrefix(strings.TrimSpace(preview.RunnerID), "/")
		if previewRunnerID == "" {
			previewRunnerID = baseRunnerID
		}
		action := strings.TrimSpace(values["CREDIMI_RUNNER_NAME_CONFLICT_ACTION"])
		if action == "" {
			action = "update"
		}
		switch {
		case previewRunnerID == baseRunnerID:
			values["CREDIMI_RUNNER_ID"] = baseRunnerID
		case action == "update":
			values["CREDIMI_RUNNER_ID"] = baseRunnerID
		case action == "create":
			values["CREDIMI_RUNNER_ID"] = previewRunnerID
		default:
			return fmt.Errorf("unsupported runner conflict action %q", action)
		}
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
	if s.manager != nil {
		s.manager.SetPublicURL(publicURL)
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
		Published:    boolPtr(isTruthyFormValue(values["CREDIMI_RUNNER_PUBLISHED"])),
	}
	if err := client.RegisterMobileRunnerResolvingName(ctx, req); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastRegistrationStatus = "Credimi runner registration updated."
	s.mu.Unlock()
	return nil
}

func (s *Server) waitForRunnerReady(ctx context.Context, values map[string]string) error {
	if !dashboardruntime.RunnerReadinessRequiredBeforeRegistration(dashboardruntime.Values(values), runtimeGOOS()) {
		return nil
	}
	host := strings.TrimSpace(values["RUNNER_HOST"])
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := strings.TrimSpace(values["RUNNER_PORT"])
	if port == "" {
		port = dashboardruntime.DefaultRunnerPort
	}
	address := net.JoinHostPort(host, port)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(deadline, "tcp", address)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("runner did not become ready on %s: %w", address, deadline.Err())
		case <-ticker.C:
		}
	}
}

func (s *Server) runtimeStartupError(ctx context.Context, cause error) error {
	if s.manager == nil {
		return cause
	}
	status := s.manager.Status(ctx)
	var details []string
	if status.LastError != "" {
		details = append(details, "last runtime error: "+status.LastError)
	}
	logs, err := s.manager.Logs(ctx, 40)
	if err == nil && len(logs) > 0 {
		start := 0
		if len(logs) > 8 {
			start = len(logs) - 8
		}
		var tail []string
		for _, line := range logs[start:] {
			tail = append(tail, strings.TrimSpace(line.Message))
		}
		details = append(details, "recent runtime logs: "+strings.Join(tail, " | "))
	}
	if len(details) == 0 {
		return cause
	}
	return fmt.Errorf("%w (%s)", cause, strings.Join(details, " ; "))
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
		status := s.manager.Status(ctx)
		if publicURL := strings.TrimSpace(status.PublicURL); publicURL != "" {
			return publicURL, "", nil
		}
		re := regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.trycloudflare\.com`)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var lastErr error
		for {
			logs, err := s.manager.Logs(deadline, quickTunnelLogTail)
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

func describeDiffImpact(diff dashboardruntime.ConfigDiff) string {
	switch {
	case hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) && hasApplyClass(diff, dashboardruntime.ApplyCredimiUpdateRequired):
		return "Save these changes? Runner services must restart and the runner record in Credimi will be updated."
	case hasApplyClass(diff, dashboardruntime.ApplyRestartRequired) && hasApplyClass(diff, dashboardruntime.ApplyCredimiUpdateRequired):
		return "Save these changes? The runner must restart and the runner record in Credimi must be updated."
	case hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate):
		return "Save these changes? Runner services must restart for them to take effect."
	case hasApplyClass(diff, dashboardruntime.ApplyRestartRequired):
		return "Save these changes? The runner must restart for them to take effect."
	default:
		return ""
	}
}

func diffRequiresRuntimeRestart(diff dashboardruntime.ConfigDiff) bool {
	return hasApplyClass(diff, dashboardruntime.ApplyRestartRequired) ||
		hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate)
}

func saveSuccessMessage(outcome applyOutcome) string {
	if outcome.Restarted {
		return "Runner restarted with the new configuration."
	}
	return "Configuration updated."
}

func hasApplyClass(diff dashboardruntime.ConfigDiff, class dashboardruntime.ApplyClass) bool {
	for _, candidate := range diff.Classes {
		if candidate == class {
			return true
		}
	}
	return false
}

type iosSimulatorCreateRequest struct {
	Name                 string `json:"name"`
	DeviceTypeIdentifier string `json:"device_type_identifier"`
	RuntimeIdentifier    string `json:"runtime_identifier"`
}

type androidEmulatorAssetsRequest struct {
	BaseName   string `json:"base_name"`
	AVDHome    string `json:"avd_home"`
	GoldenRoot string `json:"golden_root"`
	GoldenPath string `json:"golden_path"`
}

func (s *Server) iosSimulatorStatus(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	status := IOSSimulatorStatus{Supported: false, Exists: false, Name: name}
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if _, err := s.lookupPath("xcrun"); err != nil {
		writeJSON(w, status)
		return
	}
	status.Supported = true
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	exists, err := iosSimulatorExists(ctx, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	status.Exists = exists
	if !exists {
		status.DeviceTypes, err = listIOSSimulatorDeviceTypes(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		status.Runtimes, err = listIOSSimulatorRuntimes(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	writeJSON(w, status)
}

func (s *Server) iosSimulatorCreate(w http.ResponseWriter, r *http.Request) {
	if _, err := s.lookupPath("xcrun"); err != nil {
		http.Error(w, "xcrun simctl is required for iOS simulator runners", http.StatusUnprocessableEntity)
		return
	}
	var req iosSimulatorCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := createIOSSimulator(ctx, strings.TrimSpace(req.Name), strings.TrimSpace(req.DeviceTypeIdentifier), strings.TrimSpace(req.RuntimeIdentifier)); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"status": "created"})
}

func (s *Server) androidEmulatorAssetsStatus(w http.ResponseWriter, r *http.Request) {
	baseName := strings.TrimSpace(r.URL.Query().Get("base_name"))
	avdHome := strings.TrimSpace(r.URL.Query().Get("avd_home"))
	goldenRoot := strings.TrimSpace(r.URL.Query().Get("golden_root"))
	goldenPath := strings.TrimSpace(r.URL.Query().Get("golden_path"))
	if baseName == "" {
		baseName = dashboardruntime.DefaultBaseName
	}
	goldenLeaf := goldenLeafFromPath(goldenPath, baseName)
	status := AndroidEmulatorAssetsStatus{
		BaseName:      baseName,
		AVDHome:       avdHome,
		GoldenRoot:    goldenRoot,
		GoldenLeaf:    goldenLeaf,
		AVDPresent:    avdAssetsExistForName(avdHome, baseName),
		GoldenPresent: goldenAssetsPresentForLeaf(goldenRoot, goldenLeaf),
		AVDOptions:    listAVDOptions(avdHome),
		GoldenOptions: listGoldenOptions(goldenRoot),
	}
	writeJSON(w, status)
}

func (s *Server) androidEmulatorAssetsSelect(w http.ResponseWriter, r *http.Request) {
	var req androidEmulatorAssetsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.BaseName) == "" {
		req.BaseName = dashboardruntime.DefaultBaseName
	}
	goldenLeaf := goldenLeafFromPath(req.GoldenPath, req.BaseName)
	writeJSON(w, AndroidEmulatorAssetsStatus{
		BaseName:      strings.TrimSpace(req.BaseName),
		AVDHome:       strings.TrimSpace(req.AVDHome),
		GoldenRoot:    strings.TrimSpace(req.GoldenRoot),
		GoldenLeaf:    goldenLeaf,
		AVDPresent:    avdAssetsExistForName(strings.TrimSpace(req.AVDHome), strings.TrimSpace(req.BaseName)),
		GoldenPresent: goldenAssetsPresentForLeaf(strings.TrimSpace(req.GoldenRoot), goldenLeaf),
		AVDOptions:    listAVDOptions(strings.TrimSpace(req.AVDHome)),
		GoldenOptions: listGoldenOptions(strings.TrimSpace(req.GoldenRoot)),
	})
}

func (s *Server) androidEmulatorAssetsDownload(w http.ResponseWriter, r *http.Request) {
	var req androidEmulatorAssetsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	baseName := strings.TrimSpace(req.BaseName)
	if baseName == "" {
		baseName = dashboardruntime.DefaultBaseName
	}
	avdHome := strings.TrimSpace(req.AVDHome)
	goldenRoot := strings.TrimSpace(req.GoldenRoot)
	if avdHome == "" || goldenRoot == "" {
		http.Error(w, "avd_home and golden_root are required", http.StatusBadRequest)
		return
	}
	goldenLeaf := goldenLeafFromPath(req.GoldenPath, baseName)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	flusher, _ := w.(http.Flusher)
	writeProgress := func(progress DownloadProgress) {
		line, _ := json.Marshal(progress)
		w.Write(line)
		w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	writeProgress(DownloadProgress{Phase: "starting"})
	stageProgress := func(stage string) func(DownloadProgress) {
		return func(progress DownloadProgress) {
			if progress.Phase != "" {
				progress.Phase = stage + "_" + progress.Phase
			} else {
				progress.Phase = stage
			}
			writeProgress(progress)
		}
	}
	if !avdAssetsExistForName(avdHome, baseName) {
		if err := downloadAndExtractTarball(ctx, defaultBaseAVDArchiveURL, avdHome, stageProgress("base_avd")); err != nil {
			writeProgress(DownloadProgress{Phase: "error", Error: err.Error()})
			return
		}
	}
	if !goldenAssetsPresentForLeaf(goldenRoot, goldenLeaf) {
		if err := downloadAndExtractTarball(ctx, defaultGoldenArchiveURL, goldenRoot, stageProgress("golden")); err != nil {
			writeProgress(DownloadProgress{Phase: "error", Error: err.Error()})
			return
		}
	}
	writeProgress(DownloadProgress{Phase: "complete"})
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
			case <-s.ctx.Done():
				return
			case ev, ok := <-c.ch:
				if !ok {
					return
				}
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

func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func runtimeGOOS() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("GOOS_OVERRIDE")))
}
