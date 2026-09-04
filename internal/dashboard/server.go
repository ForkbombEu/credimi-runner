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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/controller"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/maintenance"
	"github.com/forkbombeu/credimi-runner/internal/runtimesupervisor"
	"github.com/forkbombeu/credimi-runner/internal/servicecoordination"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
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
	lookupPath                   = exec.LookPath
	ensureCandidateEmulatorReady = androidtools.PrepareEmulatorReady
	dashboardNow                 = time.Now
	// beforeCandidateCommit is a deterministic test seam for the optimistic
	// concurrency window; production leaves it nil.
	beforeCandidateCommit func()
)

// RuntimeController is the Dashboard's single execution boundary. The
// application supplies the shared runtime supervisor; the Dashboard never
// owns a listener, worker, edge, or service process.
type RuntimeController interface {
	Start(context.Context) error
	Stop(context.Context) error
	Restart(context.Context) error
	Reconcile(context.Context, runnerconfig.Config) error
	ApplyInventory(context.Context, runnerconfig.Config) error
	ApplyEndpoint(context.Context, runnerconfig.Config) error
	Status() runtimesupervisor.Status
}

type runtimeStartRequester interface {
	RequestStart() error
}

type Server struct {
	cfg                     *Config
	hub                     *Hub
	render                  *Renderer
	composeDir              string
	ctx                     context.Context
	hubCtx                  context.Context
	hubStartOnce            sync.Once
	hubWG                   sync.WaitGroup
	controllerID            string
	controllerIdentityToken string
	controllerFingerprint   string
	appliedServerSettings   appliedServerSettings
	runtime                 RuntimeController
	operations              *controller.Coordinator
	lookupPath              func(string) (string, error)
	pendingDiff             dashboardruntime.ConfigDiff
	startup                 startupState
	maintenance             maintenance.Status
	maintenanceChecked      bool
	maintenanceChecker      func(context.Context, string, time.Time) maintenance.Status
	systemMonitor           *SystemMonitor
	mutationMu              sync.Mutex
	mu                      sync.RWMutex
}

type appliedServerSettings struct {
	DashboardListen   string
	ReadHeaderTimeout time.Duration
	ShutdownTimeout   time.Duration
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

const startupLogRetain = 2000

const capabilityProvisionTimeout = 10 * time.Minute

const serviceRestartManualMessage = "Configuration saved. These changes require the Credimi Runner service to restart. Run: credimi-runner service restart"

type startupState struct {
	Phase     StartupPhase
	Message   string
	Logs      []string
	LogBase   int64
	LogNextID int64
	running   bool
}

// NewHandler creates the dashboard around the application's one runtime
// controller. The dashboard owns neither execution generations nor services.
func NewHandler(parent context.Context, configDir, controllerID, identityToken, fingerprint string, runtime RuntimeController, operations *controller.Coordinator) (http.Handler, context.CancelFunc, error) {
	if runtime == nil {
		return nil, nil, errors.New("runtime controller is required")
	}
	if parent == nil {
		parent = context.Background()
	}
	composeDir := configDir
	cfg, err := LoadConfig(composeDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	render, err := NewRenderer()
	if err != nil {
		return nil, nil, fmt.Errorf("templates: %w", err)
	}
	hub := NewHub(cfg, render, func() dashboardruntime.RuntimeStatus {
		return dashboardRuntimeStatus(cfg, runtime.Status())
	})
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(cfg.Snapshot()), runtimeGOOS())
	if err != nil {
		return nil, nil, fmt.Errorf("normalize config: %w", err)
	}
	cfg.mu.Lock()
	cfg.values = map[string]string(normalized)
	cfg.mu.Unlock()
	typedCfg, err := dashboardruntime.TypedConfigFromValues(normalized)
	if err != nil {
		return nil, nil, fmt.Errorf("parse normalized config: %w", err)
	}
	if err := runnerconfig.ApplyDefaults(&typedCfg); err != nil {
		return nil, nil, fmt.Errorf("apply config defaults: %w", err)
	}
	srv := &Server{
		cfg:                     cfg,
		hub:                     hub,
		render:                  render,
		composeDir:              composeDir,
		ctx:                     parent,
		controllerID:            controllerID,
		controllerIdentityToken: identityToken,
		controllerFingerprint:   fingerprint,
		appliedServerSettings:   persistentServerSettings(typedCfg),
		runtime:                 runtime,
		operations:              operations,
		lookupPath:              lookupPath,
		startup: startupState{
			Phase: StartupIdle,
		},
	}
	if srv.operations == nil {
		srv.operations = controller.NewCoordinator(parent)
	}
	checker := maintenance.Checker{ConfigDir: composeDir}
	srv.maintenanceChecker = checker.Check

	hubCtx, cancel := context.WithCancel(parent)
	srv.hubCtx = hubCtx
	srv.systemMonitor = NewSystemMonitor(hubCtx, filepath.Dir(cfg.Path()), strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_VERBOSE_LOG_PATH")) != "")

	mux := http.NewServeMux()
	srv.routes(mux)
	srv.startHub()
	handler := recoveryCORS(srv.auth(mux))
	return handler, func() {
		cancel()
		srv.systemMonitor.Close()
		srv.hubWG.Wait()
	}, nil
}

// recoveryCORS permits the old Dashboard origin to read the replacement
// startup status when only the Dashboard port changes. It is deliberately
// limited to that status endpoint and to the same host and scheme.
func recoveryCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/startup/status" {
			if origin := sameDashboardOrigin(r); origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameDashboardOrigin(r *http.Request) string {
	if r == nil {
		return ""
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	if !strings.EqualFold(parsed.Scheme, scheme) || !strings.EqualFold(parsed.Hostname(), requestHostName(r.Host)) {
		return ""
	}
	return origin
}

func requestHostName(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		return name
	}
	return strings.Trim(host, "[]")
}

func (s *Server) startHub() {
	if s.hub == nil || s.hubCtx == nil {
		return
	}
	select {
	case <-s.hubCtx.Done():
		return
	default:
	}
	s.hubStartOnce.Do(func() {
		s.hubWG.Add(1)
		go func() {
			defer s.hubWG.Done()
			s.hub.Run(s.hubCtx, 2*time.Second)
		}()
	})
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
	mux.HandleFunc("GET /monitoring", s.page("monitoring"))
	mux.HandleFunc("GET /config", s.page("config"))

	// Config actions
	mux.HandleFunc("POST /config", s.saveConfig)
	mux.HandleFunc("POST /overview/config", s.saveOverviewConfig)
	mux.HandleFunc("POST /config/diff", s.configDiff)
	mux.HandleFunc("POST /config/normalize", s.normalizeConfigPreview)
	mux.HandleFunc("POST /setup", s.finishSetup)
	mux.HandleFunc("POST /setup/organization", s.lookupSetupOrganization)
	mux.HandleFunc("POST /setup/runner-id", s.previewSetupRunnerID)
	mux.HandleFunc("POST /setup/device-id", s.previewSetupDeviceID)
	mux.HandleFunc("POST /setup/canonify", s.canonifySetupName)
	mux.HandleFunc("GET /config/raw", s.rawConfig)
	mux.HandleFunc("GET /config/secret/{key}", s.revealSecret)
	mux.HandleFunc("POST /runtime/start", s.runtimeStart)
	mux.HandleFunc("POST /runtime/stop", s.runtimeStop)
	mux.HandleFunc("POST /runtime/restart", s.runtimeRestart)
	mux.HandleFunc("POST /maintenance/check", s.maintenanceCheck)
	mux.HandleFunc("GET /startup/status", s.startupStatus)
	mux.HandleFunc("GET /api/system/metrics", s.systemMetrics)

	// Device actions
	mux.HandleFunc("POST /devices/config", s.saveDevicesConfig)
	mux.HandleFunc("GET /devices/android/connected", s.connectedAndroidDevices)
	mux.HandleFunc("GET /devices/ios-simulator/status", s.iosSimulatorStatus)
	mux.HandleFunc("POST /devices/ios-simulator/create", s.iosSimulatorCreate)
	mux.HandleFunc("GET /devices/android-emulator/assets/status", s.androidEmulatorAssetsStatus)
	mux.HandleFunc("POST /devices/android-emulator/assets/select", s.androidEmulatorAssetsSelect)
	mux.HandleFunc("POST /devices/android-emulator/assets/download", s.androidEmulatorAssetsDownload)
	mux.HandleFunc("POST /devices/connect", s.deviceConnect)
	mux.HandleFunc("POST /devices/preview-id", s.devicePreviewID)
	mux.HandleFunc("POST /devices/enable", s.deviceEnable)
	mux.HandleFunc("POST /devices/disable", s.deviceDisable)
	mux.HandleFunc("POST /devices/remove", s.deviceRemove)
	mux.HandleFunc("POST /devices/{serial}/reconnect", s.deviceReconnect)
	mux.HandleFunc("POST /devices/{serial}/disconnect", s.deviceDisconnect)

	// SSE streams
	mux.HandleFunc("GET /events/health", s.sse("health"))
	mux.HandleFunc("GET /events/devices", s.sse("devices"))
	mux.HandleFunc("GET /events/runtime", s.sse("runtime"))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /internal/controller/identity", s.controllerIdentity)
	// Machine-readable lifecycle API. These endpoints return immediately and
	// can be polled safely by remote CLI clients without tying an operation to
	// the HTTP request lifetime.
	mux.HandleFunc("GET /api/controller/status", s.controllerStatus)
	mux.HandleFunc("GET /api/controller/operations/current", s.controllerOperationCurrent)
	mux.HandleFunc("GET /api/controller/operations/{id}", s.controllerOperation)
	mux.HandleFunc("POST /api/controller/runtime/{action}", s.controllerRuntimeAction)
}

func (s *Server) connectedAndroidDevices(w http.ResponseWriter, r *http.Request) {
	devices := probeAndroid(r.Context())
	filtered := make([]Device, 0, len(devices))
	for _, device := range devices {
		if device.Status == Online && device.Type == "android_phone" && device.Mode == "usb" {
			filtered = append(filtered, device)
		}
	}
	writeJSON(w, filtered)
}

func (s *Server) deviceEnable(w http.ResponseWriter, r *http.Request) { s.setDeviceEnabled(w, r, true) }
func (s *Server) deviceDisable(w http.ResponseWriter, r *http.Request) {
	s.setDeviceEnabled(w, r, false)
}

func (s *Server) devicePreviewID(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	values := s.cfg.Snapshot()
	if name == "" {
		http.Error(w, "name is required", 400)
		return
	}
	key := strings.TrimSpace(values["CREDIMI_USER_API_KEY"])
	if key == "" {
		key = strings.TrimSpace(values["CREDIMI_INTERNAL_ADMIN_KEY"])
	}
	client := &dashboardruntime.CredimiClient{BaseURL: values["CREDIMI_URL"], APIKey: key, HTTPClient: http.DefaultClient}
	preview, err := client.PreviewDeviceID(r.Context(), values["CREDIMI_RUNNER_ID"], name, values["CREDIMI_RUNNER_ORGANIZATION"])
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, preview)
}

func (s *Server) setDeviceEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	s.queueConfigMutation(w, r, "devices", func(_ context.Context, innerR *http.Request, progress func(string)) error {
		return s.setDeviceEnabledSync(innerR, enabled, progress)
	})
}

func (s *Server) setDeviceEnabledSync(r *http.Request, enabled bool, progress func(string)) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	deviceID := strings.TrimPrefix(strings.TrimSpace(r.FormValue("device_id")), "/")
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		return err
	}
	oldValues := dashboardruntime.Values(cloneStringMap(store.Snapshot()))
	if !store.Exists() {
		oldValues = dashboardruntime.Values(cloneStringMap(s.cfg.Snapshot()))
	}
	config, err := store.RuntimeConfig()
	if err != nil {
		return err
	}
	found := false
	for index := range config.Devices {
		if strings.TrimPrefix(config.Devices[index].ID, "/") != deviceID {
			continue
		}
		config.Devices[index].Enabled = enabled
		config.Devices[index].Values["ENABLED"] = strconv.FormatBool(enabled)
		found = true
	}
	if !found {
		return errors.New("unknown device")
	}
	candidateValues := dashboardruntime.ValuesWithRuntimeDevices(dashboardruntime.Values(oldValues), config.Devices)
	provisionCtx, cancelProvision := context.WithTimeout(r.Context(), capabilityProvisionTimeout)
	defer cancelProvision()
	if err := provisionCandidateCapabilitiesForChange(provisionCtx, oldValues, candidateValues, progress); err != nil {
		return fmt.Errorf("device state was not activated because Android capabilities are unavailable: %w", err)
	}
	if err := s.saveRuntimeCandidate(oldValues, store, config); err != nil {
		return err
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	newValues := dashboardruntime.Values(s.cfg.Snapshot())
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, runtimeGOOS())
	if err := s.applySavedConfig(r.Context(), diff); err != nil {
		return fmt.Errorf("device state was saved, but runtime reconciliation failed: %w", err)
	}
	return nil
}

func (s *Server) deviceRemove(w http.ResponseWriter, r *http.Request) {
	s.queueConfigMutation(w, r, "devices", func(_ context.Context, innerR *http.Request, progress func(string)) error {
		return s.deviceRemoveSync(innerR, progress)
	})
}

func (s *Server) deviceRemoveSync(r *http.Request, progress func(string)) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	if r.FormValue("confirm") != "true" {
		return errors.New("confirmation required")
	}
	deviceID := strings.TrimPrefix(strings.TrimSpace(r.FormValue("device_id")), "/")
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		return err
	}
	oldValues := dashboardruntime.Values(cloneStringMap(store.Snapshot()))
	if !store.Exists() {
		oldValues = dashboardruntime.Values(cloneStringMap(s.cfg.Snapshot()))
	}
	config, err := store.RuntimeConfig()
	if err != nil {
		return err
	}
	devices := config.Devices[:0]
	found := false
	for _, device := range config.Devices {
		if strings.TrimPrefix(device.ID, "/") == deviceID {
			found = true
			continue
		}
		device.Index = len(devices) + 1
		devices = append(devices, device)
	}
	if !found {
		return errors.New("unknown device")
	}
	config.Devices = devices
	candidateValues := dashboardruntime.ValuesWithRuntimeDevices(dashboardruntime.Values(oldValues), config.Devices)
	provisionCtx, cancelProvision := context.WithTimeout(r.Context(), capabilityProvisionTimeout)
	defer cancelProvision()
	if err := provisionCandidateCapabilitiesForChange(provisionCtx, oldValues, candidateValues, progress); err != nil {
		return fmt.Errorf("device removal was not activated because Android capabilities are unavailable: %w", err)
	}
	if err := s.saveRuntimeCandidate(oldValues, store, config); err != nil {
		return err
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	newValues := dashboardruntime.Values(s.cfg.Snapshot())
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, runtimeGOOS())
	if err := s.applySavedConfig(r.Context(), diff); err != nil {
		return fmt.Errorf("device removal was saved, but runtime reconciliation failed: %w", err)
	}
	return nil
}

func loadConfigSnapshot(store *dashboardruntime.Store, current *Config) *Config {
	current.mu.Lock()
	defer current.mu.Unlock()
	current.values = make(map[string]string, len(store.Values))
	for key, value := range store.Values {
		current.values[key] = value
	}
	return current
}

// provisionCandidateCapabilities validates and provisions an in-memory
// inventory before it can replace the active TOML. The temporary TOML is
// private scratch state; the active configuration is untouched on failure.
func provisionCandidateCapabilities(ctx context.Context, values dashboardruntime.Values, progress func(string)) error {
	if progress == nil {
		progress = func(string) {}
	}
	progress("Checking Android SDK")
	inventory, err := dashboardruntime.ParseRuntimeConfig(values)
	if err != nil {
		if strings.TrimSpace(values["CREDIMI_DEVICE_COUNT"]) == "" {
			return nil
		}
		return fmt.Errorf("load candidate runtime configuration: %w", err)
	}
	dir, err := os.MkdirTemp("", "credimi-runner-candidate-")
	if err != nil {
		return fmt.Errorf("create candidate configuration: %w", err)
	}
	defer os.RemoveAll(dir)
	store := &dashboardruntime.Store{Path: filepath.Join(dir, "config.toml"), Values: dashboardruntime.Values{}}
	if err := store.SaveRuntimeConfig(inventory); err != nil {
		return fmt.Errorf("write candidate configuration: %w", err)
	}
	cfg, err := runnerconfig.LoadFile(store.Path)
	if err != nil {
		return fmt.Errorf("load candidate typed configuration: %w", err)
	}
	return ensureCandidateEmulatorReady(ctx, cfg, runtime.GOOS, progress)
}

func provisionCandidateCapabilitiesForChange(ctx context.Context, oldValues, newValues dashboardruntime.Values, progress func(string)) error {
	oldConfig, oldErr := dashboardruntime.ParseRuntimeConfig(oldValues)
	newConfig, newErr := dashboardruntime.ParseRuntimeConfig(newValues)
	if oldErr != nil || newErr != nil {
		return provisionCandidateCapabilities(ctx, newValues, progress)
	}
	oldEmulators := make(map[string]dashboardruntime.DeviceRuntimeConfig)
	for _, device := range oldConfig.Devices {
		if device.Type == "android_emulator" && device.Enabled {
			oldEmulators[device.ID] = device
		}
	}
	for _, device := range newConfig.Devices {
		if device.Type != "android_emulator" || !device.Enabled {
			continue
		}
		old, exists := oldEmulators[device.ID]
		if !exists || old.Mode != device.Mode || old.Serial != device.Serial || !equalStringMaps(old.Values, device.Values) {
			return provisionCandidateCapabilities(ctx, newValues, progress)
		}
	}
	return nil
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
		authToken := ""
		if s.cfg != nil {
			if values := s.cfg.Snapshot(); values != nil {
				if configured, ok := values["DASHBOARD_TOKEN"]; ok {
					authToken = strings.TrimSpace(configured)
				}
			}
		}
		if authToken == "" || strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" || r.URL.Path == "/internal/controller/identity" {
			next.ServeHTTP(w, r)
			return
		}
		// Accept either Authorization: Bearer <token> or ?token=
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if got != authToken {
			w.Header().Set("WWW-Authenticate", `Bearer`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) controllerIdentity(w http.ResponseWriter, r *http.Request) {
	if s.controllerIdentityToken == "" || r.Header.Get("X-Credimi-Controller-Token") != s.controllerIdentityToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]string{"controller_id": s.controllerID, "config_fingerprint": s.controllerFingerprint})
}

// ── page rendering ───────────────────────────────────────────────────────────

func (s *Server) pageData(active string, payload any) PageData {
	snap := s.hub.CurrentSnapshot()
	runtimeStatus := dashboardRuntimeStatus(s.cfg, s.runtime.Status())
	runtimeStatus.PendingServiceRestart = s.serviceRestartRequired()
	s.mu.RLock()
	runtimeStatus.PendingRestart = hasApplyClass(s.pendingDiff, dashboardruntime.ApplyRuntimeRestartRequired)
	runtimeStatus.PendingReconcile = hasApplyClass(s.pendingDiff, dashboardruntime.ApplyRuntimeReconcile)
	runtimeStatus.PendingCredimiUpdate = hasApplyClass(s.pendingDiff, dashboardruntime.ApplyCredimiUpdateRequired)
	maintenanceStatus := s.maintenance
	s.mu.RUnlock()
	payloadMap := map[string]any{
		"RuntimeStatus": runtimeStatus,
		"Startup":       s.startupSnapshot(),
		"RunnerVersion": buildinfo.String(),
		"Maintenance":   maintenanceStatus,
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

func dashboardRuntimeStatus(cfg *Config, status runtimesupervisor.Status) dashboardruntime.RuntimeStatus {
	return dashboardruntime.RuntimeStatus{
		Configured: cfg.Exists(), Desired: string(status.Desired), Actual: string(status.Actual),
		RunnerRunning: status.Actual == runtimesupervisor.ActualRunning,
		PublicURL:     status.PublicURL, LastError: status.LastError,
		APIListening: status.APIListening, WorkersRunning: status.WorkersRunning,
		EdgeRunning: status.EdgeRunning, HeartbeatRunning: status.HeartbeatRunning,
	}
}

func (s *Server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pageName := name
		if pageName == "overview" && !s.cfg.Exists() {
			pageName = "setup"
		}
		if pageName == "overview" {
			s.ensureMaintenanceChecked(r.Context(), false)
		}
		payload := any(nil)
		if pageName == "setup" {
			startup := s.startupSnapshot()
			if startup.Phase == StartupNeedsAttention && strings.TrimSpace(startup.Message) != "" {
				payload = map[string]any{"SetupError": startup.Message}
			}
		}
		d := s.pageData(pageName, payload)
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

func (s *Server) ensureMaintenanceChecked(ctx context.Context, force bool) {
	s.mu.Lock()
	if s.maintenanceChecked && !force {
		s.mu.Unlock()
		return
	}
	s.maintenanceChecked = true
	checker := s.maintenanceChecker
	s.mu.Unlock()
	if checker == nil {
		return
	}
	status := checker(ctx, buildinfo.String(), buildinfo.BuiltAt())
	s.mu.Lock()
	s.maintenance = status
	s.mu.Unlock()
}

func (s *Server) maintenanceCheck(w http.ResponseWriter, r *http.Request) {
	s.ensureMaintenanceChecked(r.Context(), true)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"checked":true}`))
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
	s.queueConfigPageSave(w, r, "config")
}

func (s *Server) saveOverviewConfig(w http.ResponseWriter, r *http.Request) {
	s.queueConfigPageSave(w, r, "overview")
}

func (s *Server) saveDevicesConfig(w http.ResponseWriter, r *http.Request) {
	s.queueConfigMutation(w, r, "devices", func(_ context.Context, innerR *http.Request, progress func(string)) error {
		return s.saveDevicesConfigSync(innerR, progress)
	})
}

func (s *Server) saveDevicesConfigSync(r *http.Request, progress func(string)) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		return err
	}
	oldValues := dashboardruntime.Values(cloneStringMap(store.Snapshot()))
	if !store.Exists() {
		oldValues = dashboardruntime.Values(cloneStringMap(s.cfg.Snapshot()))
	}
	values := map[string]string(oldValues)
	config, err := store.RuntimeConfig()
	if err != nil {
		config = dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values(oldValues)}
	}
	postedValue := func(keys ...string) (string, bool) {
		for _, key := range keys {
			if values, ok := r.PostForm[key]; ok {
				if len(values) == 0 {
					return "", true
				}
				return strings.TrimSpace(values[0]), true
			}
		}
		return "", false
	}
	valueAliases := []struct {
		key   string
		forms []string
	}{
		{"SERIAL", []string{"serial", "CREDIMI_RUNNER_SERIAL"}},
		{"WIFI_IP", []string{"wifi_ip", "CREDIMI_RUNNER_WIFI_IP"}},
		{"WIFI_PORT", []string{"wifi_port", "CREDIMI_RUNNER_WIFI_PORT"}},
		{"BASE_NAME", []string{"base_name", "BASE_NAME"}},
		{"ANDROID_KEYS_DIR", []string{"android_keys_dir", "ANDROID_KEYS_DIR"}},
		{"GOLDEN_PATH", []string{"golden_path", "GOLDEN_PATH"}},
		{"HOST_AVD_HOME_PATH", []string{"host_avd_home_path", "HOST_AVD_HOME_PATH"}},
		{"HOST_AVD_GOLDEN_PATH", []string{"host_avd_golden_path", "HOST_AVD_GOLDEN_PATH"}},
		{"REDROID_DATA_DIR", []string{"redroid_data_dir", "REDROID_DATA_DIR"}},
		{"REDROID_DATA_TAR", []string{"redroid_data_tar", "REDROID_DATA_TAR"}},
		{"AVDCTL_SSH_TARGET", []string{"avdctl_ssh_target", "AVDCTL_SSH_TARGET"}},
		{"AVDCTL_SSH_PASSWORD", []string{"avdctl_ssh_password", "AVDCTL_SSH_PASSWORD"}},
		{"AVDCTL_SSH_KNOWN_HOSTS_PATH", []string{"avdctl_ssh_known_hosts_path", "AVDCTL_SSH_KNOWN_HOSTS_PATH"}},
		{"AVDCTL_SUDO", []string{"avdctl_sudo", "AVDCTL_SUDO"}},
		{"AVDCTL_SUDO_PASSWORD", []string{"avdctl_sudo_password", "AVDCTL_SUDO_PASSWORD"}},
		{"IOS_UDID", []string{"ios_udid", "IOS_UDID"}},
	}
	name, namePosted := postedValue("name", "CREDIMI_DEVICE_NAME")
	description, descriptionPosted := postedValue("description", "CREDIMI_DEVICE_DESCRIPTION")
	typeValue, typePosted := postedValue("type", "CREDIMI_RUNNER_TYPE")
	modeValue, modePosted := postedValue("mode", "CREDIMI_RUNNER_DEVICE_MODE")
	deviceIDValue, _ := postedValue("device_id", "CREDIMI_DEVICE_ID")
	deviceID := strings.TrimPrefix(deviceIDValue, "/")
	conflictAction, _ := postedValue("device_conflict_action", "CREDIMI_DEVICE_CONFLICT_ACTION")
	device := dashboardruntime.DeviceRuntimeConfig{ID: deviceID, Enabled: true, Values: dashboardruntime.Values{}}
	if namePosted {
		device.Name = name
	}
	if descriptionPosted {
		device.Description = description
	}
	if typePosted {
		device.Type = typeValue
	}
	if modePosted {
		device.Mode = modeValue
	}
	if enabled, ok := postedValue("enabled", "CREDIMI_DEVICE_ENABLED"); ok {
		device.Enabled = enabled != "false"
	}
	for _, field := range valueAliases {
		if value, ok := postedValue(field.forms...); ok {
			device.Values[field.key] = value
		}
	}
	normalizeDeviceAddress := func() {
		device.WiFiIP = strings.TrimSpace(device.Values["WIFI_IP"])
		device.WiFiPort = strings.TrimSpace(device.Values["WIFI_PORT"])
		switch {
		case device.Type == "android_phone" && device.Mode == "wifi":
			if device.WiFiPort == "" {
				device.WiFiPort = dashboardruntime.DefaultWiFiPort
			}
			device.Serial = dashboardruntime.AndroidWiFiSerial(device.WiFiIP, device.WiFiPort)
			device.Values["WIFI_IP"], device.Values["WIFI_PORT"] = device.WiFiIP, device.WiFiPort
			device.Values["SERIAL"] = device.Serial
		case device.Type == "android_phone" && device.Mode == "no_device":
			device.Serial, device.WiFiIP, device.WiFiPort = "", "", ""
			delete(device.Values, "SERIAL")
			delete(device.Values, "WIFI_IP")
			delete(device.Values, "WIFI_PORT")
		case device.Type == "android_phone" && device.Mode == "usb":
			device.Serial = strings.TrimSpace(device.Values["SERIAL"])
			device.WiFiIP, device.WiFiPort = "", ""
			delete(device.Values, "WIFI_IP")
			delete(device.Values, "WIFI_PORT")
		case device.Type == "redroid":
			if device.WiFiPort == "" {
				device.WiFiPort = dashboardruntime.DefaultWiFiPort
			}
			device.Serial = dashboardruntime.AndroidWiFiSerial(device.WiFiIP, device.WiFiPort)
			device.Values["WIFI_IP"], device.Values["WIFI_PORT"] = device.WiFiIP, device.WiFiPort
			device.Values["SERIAL"] = device.Serial
		default:
			device.Serial, device.WiFiIP, device.WiFiPort = "", "", ""
			delete(device.Values, "SERIAL")
			delete(device.Values, "WIFI_IP")
			delete(device.Values, "WIFI_PORT")
		}
	}
	created := true
	if deviceID != "" {
		for index := range config.Devices {
			if config.Devices[index].ID != deviceID {
				continue
			}
			existing := config.Devices[index]
			device = existing
			device.ID = deviceID
			if namePosted {
				device.Name = name
			}
			if descriptionPosted {
				device.Description = description
			}
			if typePosted {
				device.Type = typeValue
			}
			if modePosted {
				device.Mode = modeValue
			}
			if enabled, ok := postedValue("enabled", "CREDIMI_DEVICE_ENABLED"); ok {
				device.Enabled = enabled != "false"
			}
			device.Values = dashboardruntime.Values(cloneStringMap(existing.Values))
			if typePosted && typeValue != existing.Type {
				for _, key := range []string{"AVDCTL_SSH_TARGET", "AVDCTL_SSH_PASSWORD", "AVDCTL_SSH_KNOWN_HOSTS_PATH", "AVDCTL_SSH_ARGS", "AVDCTL_SUDO", "AVDCTL_SUDO_PASSWORD", "REDROID_IMAGE", "REDROID_DATA_DIR", "REDROID_DATA_TAR"} {
					delete(device.Values, key)
				}
			}
			for _, field := range valueAliases {
				if value, ok := postedValue(field.forms...); ok {
					if value == "" {
						// Edit forms intentionally leave secret fields blank. Preserve
						// those values unless the user explicitly disables SSH (the
						// client then clears all SSH fields before submitting).
						if field.key != "AVDCTL_SSH_PASSWORD" && field.key != "AVDCTL_SUDO_PASSWORD" {
							delete(device.Values, field.key)
						}
					} else {
						device.Values[field.key] = value
					}
				}
			}
			if strings.TrimSpace(device.Values["AVDCTL_SSH_TARGET"]) == "" {
				for _, key := range []string{"AVDCTL_SSH_TARGET", "AVDCTL_SSH_PASSWORD", "AVDCTL_SSH_KNOWN_HOSTS_PATH", "AVDCTL_SSH_ARGS", "AVDCTL_SUDO", "AVDCTL_SUDO_PASSWORD"} {
					delete(device.Values, key)
				}
				device.Values["AVDCTL_SUDO"] = "false"
			} else if strings.EqualFold(strings.TrimSpace(device.Values["AVDCTL_SUDO"]), "false") {
				delete(device.Values, "AVDCTL_SUDO_PASSWORD")
			}
			if namePosted && strings.TrimSpace(name) != strings.TrimSpace(existing.Name) {
				key := strings.TrimSpace(values["CREDIMI_USER_API_KEY"])
				if key == "" {
					key = strings.TrimSpace(values["CREDIMI_INTERNAL_ADMIN_KEY"])
				}
				if key == "" {
					return errors.New("a Credimi API key is required to rename a device")
				}
				preview, err := (&dashboardruntime.CredimiClient{BaseURL: values["CREDIMI_URL"], APIKey: key, HTTPClient: http.DefaultClient}).PreviewDeviceID(r.Context(), values["CREDIMI_RUNNER_ID"], device.Name, values["CREDIMI_RUNNER_ORGANIZATION"])
				if err != nil {
					return fmt.Errorf("device rename ID resolution failed: %w", err)
				}
				newID := strings.TrimPrefix(strings.TrimSpace(preview.DeviceID), "/")
				if preview.Conflict {
					switch conflictAction {
					case "create":
						// Keep the newly previewed identity and reconcile the old one after registration.
					case "update":
						newID = strings.TrimPrefix(strings.TrimSpace(preview.ExistingDeviceID), "/")
					default:
						return errors.New("device rename conflicts with an existing Credimi device; choose create or update explicitly")
					}
				}
				if newID == "" {
					return errors.New("device rename did not return a canonical device ID")
				}
				device.ID = newID
			}
			normalizeDeviceAddress()
			config.Devices[index] = device
			created = false
			break
		}
	}
	if created {
		normalizeDeviceAddress()
	}
	if created {
		// Previewing an available canonical ID is part of normal creation. It
		// is only an edit when the form explicitly selected an existing record.
		if deviceID != "" && conflictAction != "create" {
			return errors.New("device not found")
		}
		if err := dashboardruntime.ValidateDeviceRegistration(device); err != nil {
			return err
		}
		key := strings.TrimSpace(values["CREDIMI_USER_API_KEY"])
		if key == "" {
			key = strings.TrimSpace(values["CREDIMI_INTERNAL_ADMIN_KEY"])
		}
		if deviceID == "" {
			preview, err := (&dashboardruntime.CredimiClient{BaseURL: values["CREDIMI_URL"], APIKey: key, HTTPClient: http.DefaultClient}).PreviewDeviceID(r.Context(), values["CREDIMI_RUNNER_ID"], device.Name, values["CREDIMI_RUNNER_ORGANIZATION"])
			if err != nil {
				return err
			}
			deviceID = preview.DeviceID
		}
		device.ID = strings.TrimPrefix(deviceID, "/")
		applyDeviceDefaults(&device)
		config.Devices = append(config.Devices, device)
	}
	if err := dashboardruntime.ValidateDeviceConstraints(config.Devices); err != nil {
		return err
	}
	candidateValues := dashboardruntime.ValuesWithRuntimeDevices(dashboardruntime.Values(values), config.Devices)
	provisionCtx, cancelProvision := context.WithTimeout(r.Context(), capabilityProvisionTimeout)
	defer cancelProvision()
	if err := provisionCandidateCapabilitiesForChange(provisionCtx, oldValues, candidateValues, progress); err != nil {
		return fmt.Errorf("device configuration was not activated because Android capabilities are unavailable: %w", err)
	}
	if err := s.saveRuntimeCandidate(oldValues, store, config); err != nil {
		return err
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	newValues := dashboardruntime.Values(s.cfg.Snapshot())
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, runtimeGOOS())
	return s.applySavedConfig(r.Context(), diff)
}

// applyDeviceDefaults keeps the dashboard form concise while making every
// indexed block independently runnable. Explicit user input always wins.
func applyDeviceDefaults(device *dashboardruntime.DeviceRuntimeConfig) {
	if device.Values == nil {
		device.Values = dashboardruntime.Values{}
	}
	set := func(key, value string) {
		if strings.TrimSpace(device.Values[key]) == "" {
			device.Values[key] = value
		}
	}
	switch device.Type {
	case "android_emulator":
		androidKeysDir, avdHome, goldenRoot := emulatorAssetPaths()
		set("BASE_NAME", "credimi")
		set("ANDROID_KEYS_DIR", androidKeysDir)
		set("HOST_AVD_HOME_PATH", avdHome)
		set("HOST_AVD_GOLDEN_PATH", goldenRoot)
		set("GOLDEN_PATH", "/avd-golden/credimi-golden")
	case "android_phone", "redroid":
	}
	if device.Mode == "wifi" || device.Type == "redroid" {
		set("WIFI_PORT", "5555")
	}
	if device.Type == "redroid" {
		set("REDROID_DATA_DIR", dashboardruntime.DefaultRedroidDataDir)
		set("REDROID_DATA_TAR", dashboardruntime.DefaultRedroidDataTar)
		if strings.TrimSpace(device.Values["AVDCTL_SSH_TARGET"]) != "" {
			set("AVDCTL_SSH_KNOWN_HOSTS_PATH", dashboardruntime.EffectiveSSHKnownHostsPath(device.Values["AVDCTL_SSH_TARGET"], device.Values["AVDCTL_SSH_KNOWN_HOSTS_PATH"]))
		}
	}
}

// emulatorAssetPaths returns paths inside the runner. In Linux bootstrap mode
// these are explicit mount targets backed by the launching user's home; using
// os.UserHomeDir here would incorrectly resolve the container user's HOME.
func emulatorAssetPaths() (androidKeysDir, avdHome, goldenRoot string) {
	androidKeysDir = strings.TrimSpace(os.Getenv(dashboardruntime.ContainerAndroidDirEnv))
	avdHome = strings.TrimSpace(os.Getenv(dashboardruntime.ContainerAVDHomeEnv))
	goldenRoot = strings.TrimSpace(os.Getenv(dashboardruntime.ContainerGoldenRootEnv))
	if androidKeysDir == "" || avdHome == "" || goldenRoot == "" {
		homeDir, _ := os.UserHomeDir()
		if androidKeysDir == "" {
			androidKeysDir = filepath.Join(homeDir, ".android")
		}
		if avdHome == "" {
			avdHome = filepath.Join(androidKeysDir, "avd")
		}
		if goldenRoot == "" {
			goldenRoot = filepath.Join(homeDir, "avd-golden")
		}
	}
	return androidKeysDir, avdHome, goldenRoot
}

func (s *Server) saveConfigPageSync(r *http.Request, page string, progress func(string)) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	incoming := formValuesMap(r.PostForm)
	baselineStore, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		return err
	}
	oldSnapshot := dashboardruntime.Values(cloneStringMap(baselineStore.Snapshot()))
	if !baselineStore.Exists() {
		oldSnapshot = dashboardruntime.Values(cloneStringMap(s.cfg.Snapshot()))
	}
	if err := s.resolveConfigIdentity(r.Context(), oldSnapshot, incoming); err != nil {
		return err
	}
	candidateSnapshot, err := normalizedConfigValues(oldSnapshot, incoming, runtimeGOOS())
	if err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}
	provisionCtx, cancelProvision := context.WithTimeout(r.Context(), capabilityProvisionTimeout)
	defer cancelProvision()
	if err := provisionCandidateCapabilitiesForChange(provisionCtx, oldSnapshot, candidateSnapshot, progress); err != nil {
		return fmt.Errorf("configuration was not activated because Android capabilities are unavailable: %w", err)
	}
	if beforeCandidateCommit != nil {
		beforeCandidateCommit()
	}
	s.mutationMu.Lock()
	currentStore, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		s.mutationMu.Unlock()
		return err
	}
	changed := baselineStore.Exists() != currentStore.Exists()
	if !changed && baselineStore.Exists() {
		baseCanonical, err := canonicalCompatibilityValues(map[string]string(baselineStore.Snapshot()))
		if err != nil {
			s.mutationMu.Unlock()
			return err
		}
		persistedCanonical, err := canonicalCompatibilityValues(map[string]string(currentStore.Snapshot()))
		if err != nil {
			s.mutationMu.Unlock()
			return err
		}
		changed = !equalStringMaps(baseCanonical, persistedCanonical)
	}
	if changed {
		s.mutationMu.Unlock()
		return errors.New("configuration changed while preparing the update; retry")
	}
	if err := currentStore.Save(candidateSnapshot); err != nil {
		s.mutationMu.Unlock()
		return fmt.Errorf("configuration validation failed: %w", err)
	}
	s.cfg = loadConfigSnapshot(currentStore, s.cfg)
	s.mutationMu.Unlock()
	newSnapshot := s.cfg.Snapshot()
	diff := dashboardruntime.DiffValuesForOS(dashboardruntime.Values(oldSnapshot), dashboardruntime.Values(newSnapshot), runtimeGOOS())
	if err := s.applySavedConfig(r.Context(), diff); err != nil {
		return fmt.Errorf("configuration was saved but reconciliation failed: %w", err)
	}
	return nil
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
	diff := dashboardruntime.DiffValuesForOS(dashboardruntime.Values(current), normalized, runtimeGOOS())
	confirmRequired := diffNeedsConfirmation(diff)
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

func diffNeedsRuntimeApply(diff dashboardruntime.ConfigDiff) bool {
	return hasApplyClass(diff, dashboardruntime.ApplyRuntimeReconcile) ||
		hasApplyClass(diff, dashboardruntime.ApplyRuntimeRestartRequired) ||
		hasApplyClass(diff, dashboardruntime.ApplyCredimiUpdateRequired)
}

func diffNeedsConfirmation(diff dashboardruntime.ConfigDiff) bool {
	for _, class := range diff.Classes {
		if class != dashboardruntime.ApplySavedOnly {
			return true
		}
	}
	return false
}

func withoutServiceClass(diff dashboardruntime.ConfigDiff) dashboardruntime.ConfigDiff {
	filtered := dashboardruntime.ConfigDiff{ChangedKeys: append([]string(nil), diff.ChangedKeys...)}
	for _, class := range diff.Classes {
		if class != dashboardruntime.ApplyServiceRestartRequired {
			filtered.Classes = append(filtered.Classes, class)
		}
	}
	return filtered
}

func pendingDiffForPlatform(diff dashboardruntime.ConfigDiff, goos string) dashboardruntime.ConfigDiff {
	if goos == "darwin" {
		return diff
	}
	return withoutServiceClass(diff)
}

func mergeConfigDiff(left, right dashboardruntime.ConfigDiff) dashboardruntime.ConfigDiff {
	merged := dashboardruntime.ConfigDiff{}
	for _, diff := range []dashboardruntime.ConfigDiff{left, right} {
		for _, key := range diff.ChangedKeys {
			if !containsString(merged.ChangedKeys, key) {
				merged.ChangedKeys = append(merged.ChangedKeys, key)
			}
		}
		for _, class := range diff.Classes {
			if !hasApplyClass(merged, class) {
				merged.Classes = append(merged.Classes, class)
			}
		}
	}
	return merged
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *Server) serviceRestartRequired() bool {
	return s.serviceRestartRequiredFor(s.currentPendingDiff())
}

func (s *Server) serviceRestartRequiredFor(pending dashboardruntime.ConfigDiff) bool {
	if runtimeGOOS() == "darwin" {
		desired, err := persistentServerSettingsFromValues(dashboardruntime.Values(s.cfg.Snapshot()))
		return err != nil || desired != s.appliedServerSettings
	}
	return dashboardruntime.ServiceRestartRequired(dashboardruntime.Values(s.cfg.Snapshot()), s.cfg.Exists())
}

func serviceRestartResultApplied(configDir, configPath string) bool {
	request, err := servicecoordination.ReadRestartRequest(configDir)
	if err != nil {
		return false
	}
	result, err := servicecoordination.ReadRestartResult(configDir)
	if err != nil || !result.Success || result.RequestID != request.RequestID {
		return false
	}
	cfg, err := runnerconfig.LoadFile(configPath)
	return err == nil && result.AppliedFingerprint == dashboardruntime.ServiceConfigFingerprintForCurrentHost(cfg, true)
}

func serviceRestartResultFailure(configDir string) string {
	request, err := servicecoordination.ReadRestartRequest(configDir)
	if err != nil {
		return ""
	}
	result, err := servicecoordination.ReadRestartResult(configDir)
	if err != nil || result.Success || result.RequestID != request.RequestID {
		return ""
	}
	return strings.TrimSpace(result.Error)
}

func (s *Server) requestServiceRestart() error {
	if strings.TrimSpace(s.composeDir) == "" {
		return errors.New("service coordination directory is empty")
	}
	cfg, err := runnerconfig.LoadFile(s.cfg.Path())
	if err != nil {
		return fmt.Errorf("load service configuration: %w", err)
	}
	digest, err := runnerconfig.ConfigFileDigest(s.cfg.Path())
	if err != nil {
		return fmt.Errorf("digest persisted service configuration: %w", err)
	}
	forceRestart := servicemanager.ServiceHostLocalityUnknown(cfg, dashboardruntime.AppliedServiceHostContext())
	request, err := servicecoordination.NewRestartRequest(digest, forceRestart, dashboardNow())
	if err != nil {
		return err
	}
	if err := servicecoordination.WriteRestartRequest(s.composeDir, request); err != nil {
		return fmt.Errorf("write service restart request: %w", err)
	}
	active, activeErr := servicecoordination.CoordinatorActive(s.composeDir, dashboardNow())
	if activeErr == nil && active {
		s.setStartupState(StartupStarting, "Configuration saved. Waiting for the attached Credimi Runner to restart the service.")
	} else {
		s.setStartupState(StartupNeedsAttention, serviceRestartManualMessage)
	}
	return nil
}

func (s *Server) recoveryOrigin(request *http.Request) string {
	values := s.cfg.Snapshot()
	if request != nil {
		for _, key := range []string{"DASHBOARD_HOST", "DASHBOARD_PORT"} {
			if entries := request.PostForm[key]; len(entries) > 0 {
				values[key] = entries[0]
			}
		}
	}
	cfg, err := dashboardruntime.TypedConfigFromValues(dashboardruntime.Values(values))
	if err != nil {
		return ""
	}
	_, port, err := net.SplitHostPort(cfg.Server.DashboardListen)
	if err != nil || strings.TrimSpace(port) == "" {
		return ""
	}
	host := strings.TrimSpace(request.Host)
	if parsedHost, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = parsedHost
	} else {
		host = strings.Trim(host, "[]")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	} else if forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return (&url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}).String()
}

func dashboardListen(values dashboardruntime.Values) string {
	cfg, err := dashboardruntime.TypedConfigFromValues(values)
	if err != nil {
		return ""
	}
	host, port, err := net.SplitHostPort(cfg.Server.DashboardListen)
	if err != nil {
		return strings.TrimSpace(cfg.Server.DashboardListen)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func persistentServerSettings(cfg runnerconfig.Config) appliedServerSettings {
	return appliedServerSettings{
		DashboardListen:   dashboardListen(dashboardruntime.ValuesFromTypedConfig(cfg)),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Duration(),
		ShutdownTimeout:   cfg.Server.ShutdownTimeout.Duration(),
	}
}

func persistentServerSettingsFromValues(values dashboardruntime.Values) (appliedServerSettings, error) {
	cfg, err := dashboardruntime.TypedConfigFromValues(values)
	if err != nil {
		return appliedServerSettings{}, err
	}
	if err := runnerconfig.ApplyDefaults(&cfg); err != nil {
		return appliedServerSettings{}, err
	}
	return persistentServerSettings(cfg), nil
}

func (s *Server) desiredDashboardListen() string {
	return dashboardListen(dashboardruntime.Values(s.cfg.Snapshot()))
}

func (s *Server) requireCurrentServiceConfig() error {
	if !s.serviceRestartRequired() {
		return nil
	}
	return errors.New("the persistent Credimi Runner service must be restarted before the saved configuration can be activated; run: credimi-runner service restart")
}

func (s *Server) currentPendingDiff() dashboardruntime.ConfigDiff {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingDiff
}

func (s *Server) setPendingDiff(diff dashboardruntime.ConfigDiff) {
	s.mu.Lock()
	s.pendingDiff = diff
	s.mu.Unlock()
}

func (s *Server) applySavedConfig(ctx context.Context, diff dashboardruntime.ConfigDiff) error {
	pending := mergeConfigDiff(s.currentPendingDiff(), pendingDiffForPlatform(diff, runtimeGOOS()))
	if s.serviceRestartRequiredFor(pending) {
		s.setPendingDiff(pending)
		return s.requestServiceRestart()
	}
	if runtimeGOOS() == "darwin" {
		pending = withoutServiceClass(pending)
	}
	if !diffNeedsRuntimeApply(pending) {
		s.setPendingDiff(dashboardruntime.ConfigDiff{})
		return nil
	}
	if s.runtime.Status().Desired != runtimesupervisor.DesiredRunning {
		s.setPendingDiff(pending)
		return nil
	}
	cfg, err := runnerconfig.LoadFile(s.cfg.Path())
	if err != nil {
		s.setPendingDiff(pending)
		return fmt.Errorf("load typed configuration: %w", err)
	}
	if inactiveExposureOnlyDiff(pending, cfg) {
		s.setPendingDiff(dashboardruntime.ConfigDiff{})
		return nil
	}
	apply := s.runtime.Reconcile
	if activeManualEndpointDiff(pending, cfg) {
		apply = s.runtime.ApplyEndpoint
	} else if inventoryOnlyDiff(pending, cfg) {
		apply = s.runtime.ApplyInventory
	}
	if err := apply(ctx, cfg); err != nil {
		s.setPendingDiff(pending)
		return err
	}
	s.setPendingDiff(dashboardruntime.ConfigDiff{})
	return nil
}

func inventoryOnlyDiff(diff dashboardruntime.ConfigDiff, cfg runnerconfig.Config) bool {
	if len(diff.ChangedKeys) == 0 {
		return false
	}
	for _, key := range diff.ChangedKeys {
		if strings.HasPrefix(key, "CREDIMI_DEVICE_") || key == "CREDIMI_RUNNER_DESCRIPTION" || inactiveExposureKey(key, cfg.Exposure.Mode) {
			continue
		}
		return false
	}
	return true
}

func activeManualEndpointDiff(diff dashboardruntime.ConfigDiff, cfg runnerconfig.Config) bool {
	if cfg.Exposure.Mode != "manual" || len(diff.ChangedKeys) == 0 {
		return false
	}
	for _, key := range diff.ChangedKeys {
		if key != "RUNNER_PUBLIC_URL" && key != "RUNNER_PUBLIC_PORT" {
			return false
		}
	}
	return true
}

func inactiveExposureOnlyDiff(diff dashboardruntime.ConfigDiff, cfg runnerconfig.Config) bool {
	if len(diff.ChangedKeys) == 0 {
		return false
	}
	for _, key := range diff.ChangedKeys {
		if !inactiveExposureKey(key, cfg.Exposure.Mode) {
			return false
		}
	}
	return true
}

func inactiveExposureKey(key, mode string) bool {
	switch {
	case key == "RUNNER_PUBLIC_URL" || key == "RUNNER_PUBLIC_PORT":
		return mode != "manual"
	case key == "RUNNER_DOMAIN":
		return mode != "named_tunnel"
	default:
		return false
	}
}

func (s *Server) finishSetup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	incoming := formValuesMap(r.PostForm)
	if errs := validateSetupInput(incoming); len(errs) > 0 {
		d := s.pageData("setup", map[string]any{"Errors": errs, "SetupError": "Some fields need attention."})
		html, _ := s.render.FragmentPage("setup", d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(html))
		return
	}
	s.queueConfigMutation(w, r, "setup", func(_ context.Context, innerR *http.Request, progress func(string)) error {
		return s.finishSetupSync(innerR, progress, true)
	})
}

func (s *Server) finishSetupSync(r *http.Request, progress func(string), deferStart bool) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	incoming := formValuesMap(r.PostForm)
	oldValues := dashboardruntime.Values(cloneStringMap(s.cfg.Snapshot()))
	// Setup resolves the runner and every device through Credimi before it can
	// persist the inventory. Do not let an unavailable Credimi endpoint leave
	// the wizard's request open forever while its startup status stays idle.
	setupCtx, cancelSetup := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancelSetup()
	if errs := validateSetupInput(incoming); len(errs) > 0 {
		return fmt.Errorf("setup validation failed: %v", errs)
	}
	if err := s.resolveSetupIdentity(setupCtx, incoming); err != nil {
		return fmt.Errorf("identity resolution failed: %w", err)
	}
	candidate, err := normalizedConfigValues(s.cfg.Snapshot(), incoming, runtimeGOOS())
	if err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}
	if errs := Validate(map[string]string(candidate)); len(errs) > 0 {
		return fmt.Errorf("configuration validation failed: %v", errs)
	}
	devices, err := s.setupDevices(r.WithContext(setupCtx), map[string]string(candidate))
	if err != nil {
		return fmt.Errorf("device setup failed: %w", err)
	}
	values := dashboardruntime.ValuesWithRuntimeDevices(candidate, devices)
	if err := s.validateRuntimeRequirements(map[string]string(values)); err != nil {
		return fmt.Errorf("runtime requirement check failed: %w", err)
	}
	provisionCtx, cancelProvision := context.WithTimeout(r.Context(), capabilityProvisionTimeout)
	defer cancelProvision()
	if err := provisionCandidateCapabilities(provisionCtx, values, progress); err != nil {
		return fmt.Errorf("Android capabilities are unavailable: %w", err)
	}
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		return fmt.Errorf("configuration store failed: %w", err)
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: candidate, Devices: devices}); err != nil {
		return fmt.Errorf("device inventory failed: %w", err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	cfg, err := runnerconfig.LoadFile(s.cfg.Path())
	if err != nil {
		return fmt.Errorf("load saved typed configuration: %w", err)
	}
	newValues := dashboardruntime.Values(s.cfg.Snapshot())
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, runtimeGOOS())
	if s.serviceRestartRequiredFor(diff) {
		if requester, ok := s.runtime.(runtimeStartRequester); ok {
			if err := requester.RequestStart(); err != nil {
				return fmt.Errorf("persist runtime start request: %w", err)
			}
		}
		s.setPendingDiff(pendingDiffForPlatform(diff, runtimeGOOS()))
		if err := s.requestServiceRestart(); err != nil {
			return err
		}
		return nil
	}
	if deferStart {
		if err := s.runtime.Start(r.Context()); err != nil {
			return fmt.Errorf("runtime start failed: %w", err)
		}
	} else if err := s.runtime.Reconcile(r.Context(), cfg); err != nil {
		return fmt.Errorf("runtime reconcile failed: %w", err)
	}
	s.mu.Lock()
	s.pendingDiff = dashboardruntime.ConfigDiff{}
	s.mu.Unlock()
	s.setStartupState(StartupReady, "Setup complete. Runner started and registered with Credimi.")
	return nil
}

func (s *Server) setupDevices(r *http.Request, values map[string]string) ([]dashboardruntime.DeviceRuntimeConfig, error) {
	count, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get("SETUP_DEVICE_COUNT")))
	if err != nil || count < 1 {
		return nil, errors.New("add at least one device")
	}
	apiKey := strings.TrimSpace(values["CREDIMI_USER_API_KEY"])
	if apiKey == "" {
		apiKey = strings.TrimSpace(values["CREDIMI_INTERNAL_ADMIN_KEY"])
	}
	client := &dashboardruntime.CredimiClient{BaseURL: values["CREDIMI_URL"], APIKey: apiKey, HTTPClient: http.DefaultClient}
	devices := make([]dashboardruntime.DeviceRuntimeConfig, 0, count)
	for index := 1; index <= count; index++ {
		prefix := fmt.Sprintf("SETUP_DEVICE_%d_", index)
		value := func(field string) string {
			candidates := r.PostForm[prefix+field]
			for i := len(candidates) - 1; i >= 0; i-- {
				if value := strings.TrimSpace(candidates[i]); value != "" {
					return value
				}
			}
			return ""
		}
		name := value("NAME")
		if name == "" {
			return nil, fmt.Errorf("device %d name is required", index)
		}
		device := dashboardruntime.DeviceRuntimeConfig{Name: name, Description: value("DESCRIPTION"), Type: value("TYPE"), Mode: value("MODE"), Enabled: true, Serial: value("SERIAL"), Values: dashboardruntime.Values{}}
		if device.Type == "" {
			device.Type = "android_phone"
		}
		if device.Mode == "" {
			device.Mode = "usb"
		}
		device.WiFiIP = value("WIFI_IP")
		device.WiFiPort = value("WIFI_PORT")
		switch {
		case device.Type == "android_phone" && device.Mode == "wifi":
			if device.WiFiPort == "" {
				device.WiFiPort = dashboardruntime.DefaultWiFiPort
			}
			device.Serial = dashboardruntime.AndroidWiFiSerial(device.WiFiIP, device.WiFiPort)
		case device.Type == "android_phone" && device.Mode == "no_device":
			device.Serial, device.WiFiIP, device.WiFiPort = "", "", ""
		case device.Type == "redroid":
			if device.WiFiPort == "" {
				device.WiFiPort = dashboardruntime.DefaultWiFiPort
			}
			device.Serial = dashboardruntime.AndroidWiFiSerial(device.WiFiIP, device.WiFiPort)
		case device.Type == "android_phone" && device.Mode == "usb":
			device.WiFiIP, device.WiFiPort = "", ""
		}
		if device.Serial != "" {
			device.Values["SERIAL"] = device.Serial
		}
		if device.WiFiIP != "" {
			device.Values["WIFI_IP"] = device.WiFiIP
		}
		if device.WiFiPort != "" {
			device.Values["WIFI_PORT"] = device.WiFiPort
		}
		for _, field := range []string{"BASE_NAME", "ANDROID_KEYS_DIR", "GOLDEN_PATH", "HOST_AVD_HOME_PATH", "HOST_AVD_GOLDEN_PATH", "REDROID_DATA_DIR", "REDROID_DATA_TAR", "AVDCTL_SSH_TARGET", "AVDCTL_SSH_PASSWORD", "AVDCTL_SSH_KNOWN_HOSTS_PATH", "AVDCTL_SUDO", "AVDCTL_SUDO_PASSWORD", "IOS_UDID"} {
			if fieldValue := value(field); fieldValue != "" {
				device.Values[field] = fieldValue
			}
		}
		if device.Type == "android_phone" && device.Mode == "usb" && device.Serial == "" {
			return nil, fmt.Errorf("device %d requires a USB Android serial", index)
		}
		if (device.Type == "android_phone" && device.Mode == "wifi") || device.Type == "redroid" {
			if device.Values["WIFI_IP"] == "" {
				return nil, fmt.Errorf("device %d requires a Wi-Fi or Redroid IP address", index)
			}
		}
		applyDeviceDefaults(&device)
		if err := dashboardruntime.ValidateDeviceRegistration(device); err != nil {
			return nil, err
		}
		preview, err := client.PreviewDeviceID(r.Context(), values["CREDIMI_RUNNER_ID"], device.Name, values["CREDIMI_RUNNER_ORGANIZATION"])
		if err != nil {
			return nil, fmt.Errorf("resolve device %q ID: %w", device.Name, err)
		}
		action := value("CONFLICT_ACTION")
		if preview.Conflict && action == "update" {
			device.ID = strings.TrimPrefix(preview.ExistingDeviceID, "/")
		} else {
			device.ID = strings.TrimPrefix(preview.DeviceID, "/")
		}
		devices = append(devices, device)
	}
	if err := dashboardruntime.ValidateDeviceConstraints(devices); err != nil {
		return nil, err
	}
	return devices, nil
}

func (s *Server) renderSetupComplete(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Server) setStartupState(phase StartupPhase, message string) {
	s.mu.Lock()
	s.startup.Phase = phase
	s.startup.Message = message
	if strings.TrimSpace(message) != "" {
		s.appendStartupLogLocked(message)
	}
	s.mu.Unlock()
}

func (s *Server) appendStartupLog(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	s.mu.Lock()
	s.appendStartupLogLocked(message)
	s.mu.Unlock()
}

func (s *Server) appendStartupLogLocked(message string) {
	if s.startup.LogBase == 0 {
		s.startup.LogBase = 1
	}
	if s.startup.LogNextID == 0 {
		s.startup.LogNextID = s.startup.LogBase + int64(len(s.startup.Logs))
	}
	if len(s.startup.Logs) > 0 && s.startup.Logs[len(s.startup.Logs)-1] == message {
		return
	}
	s.startup.Logs = append(s.startup.Logs, message)
	s.startup.LogNextID++
	if extra := len(s.startup.Logs) - startupLogRetain; extra > 0 {
		s.startup.Logs = append([]string(nil), s.startup.Logs[extra:]...)
		s.startup.LogBase += int64(extra)
	}
}

func (s *Server) startupSnapshot() startupState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return startupState{
		Phase:     s.startup.Phase,
		Message:   s.startup.Message,
		Logs:      append([]string(nil), s.startup.Logs...),
		LogBase:   s.startup.LogBase,
		LogNextID: s.startup.LogNextID,
		running:   s.startup.running,
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
	if strings.TrimSpace(values["CREDIMI_SERVICE_MODE"]) == "manual" {
		if strings.TrimSpace(values["RUNNER_PUBLIC_URL"]) == "" {
			errs["RUNNER_PUBLIC_URL"] = "Required."
		} else if message := Validate(map[string]string{"RUNNER_PUBLIC_URL": values["RUNNER_PUBLIC_URL"]})["RUNNER_PUBLIC_URL"]; message != "" {
			errs["RUNNER_PUBLIC_URL"] = message
		}
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
	inventory, err := dashboardruntime.ParseRuntimeConfig(normalized)
	if err != nil {
		return err
	}
	for _, device := range inventory.Devices {
		if !device.Enabled {
			continue
		}
		switch device.Type {
		case "ios_simulator":
			if _, err := s.lookupPath("xcrun"); err != nil {
				return errors.New("xcrun simctl is required for iOS simulator devices")
			}
		case "android_phone":
			// USB serials may be entered before this service has USB access.
			// The replacement runtime performs the authoritative adb readiness
			// check after the required host-ADB/USB topology is active.
		case "android_emulator":
			// KVM belongs to the active service topology. Candidate assets may be
			// prepared before that topology is recreated with /dev/kvm.
			if plan.Backend != dashboardruntime.DefaultContainerBackend {
				continue
			}
		}
	}
	return nil
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
			Organization:  req.Organization,
			RunnerID:      req.Organization + "/" + canonifyPlain(req.Name),
			DefaultAction: "update",
		}
	}
	writeJSON(w, preview)
}

func (s *Server) previewSetupDeviceID(w http.ResponseWriter, r *http.Request) {
	var req setupDevicePreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	for _, value := range []*string{&req.InstanceURL, &req.APIKey, &req.Organization, &req.RunnerID, &req.Name} {
		*value = strings.TrimSpace(*value)
	}
	if req.InstanceURL == "" || req.APIKey == "" || req.Organization == "" || req.RunnerID == "" || req.Name == "" {
		http.Error(w, "Credimi URL, API key, organization, runner ID, and device name are required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	preview, err := (&dashboardruntime.CredimiClient{BaseURL: req.InstanceURL, APIKey: req.APIKey, HTTPClient: http.DefaultClient}).PreviewDeviceID(ctx, req.RunnerID, req.Name, req.Organization)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, preview)
}

func (s *Server) runtimeStart(w http.ResponseWriter, r *http.Request) {
	s.queueDashboardRuntimeAction(w, r, "start")
}

func (s *Server) controllerStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"runtime": s.runtime.Status(), "operation": s.operations.Current()})
}

func (s *Server) controllerOperationCurrent(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.operations.Current())
}

func (s *Server) systemMetrics(w http.ResponseWriter, r *http.Request) {
	if s.systemMonitor == nil {
		writeJSON(w, map[string]any{"samples": []SystemMetrics{}, "interval_ms": 2000})
		return
	}
	samples := s.systemMonitor.Live()
	if r.URL.Query().Get("range") == "hourly" {
		samples = s.systemMonitor.Hourly()
	}
	writeJSON(w, map[string]any{"samples": samples, "interval_ms": s.systemMonitor.Interval().Milliseconds()})
}

func (s *Server) controllerOperation(w http.ResponseWriter, r *http.Request) {
	snapshot, ok := s.operations.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, snapshot)
}

func (s *Server) controllerRuntimeAction(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.submitRuntimeAction(r.PathValue("action"))
	if err != nil {
		if errors.Is(err, controller.ErrOperationConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, snapshot)
}

func (s *Server) submitRuntimeAction(action string) (controller.Snapshot, error) {
	var kind controller.OperationKind
	var operation func(context.Context) error
	switch action {
	case "start":
		kind, operation = controller.OperationRuntimeStart, s.runtime.Start
	case "stop":
		kind, operation = controller.OperationRuntimeStop, s.runtime.Stop
	case "restart":
		kind, operation = controller.OperationRuntimeRestart, s.runtime.Restart
	default:
		return controller.Snapshot{}, fmt.Errorf("unsupported runtime action %q", action)
	}
	if s.operations == nil {
		s.operations = controller.NewCoordinator(s.ctx)
	}
	return s.operations.Submit(kind, func(ctx context.Context, _ func(controller.Progress)) error {
		if action != "stop" {
			if err := s.requireCurrentServiceConfig(); err != nil {
				return err
			}
		}
		if err := operation(ctx); err != nil {
			return err
		}
		if action != "stop" {
			s.setPendingDiff(dashboardruntime.ConfigDiff{})
		}
		return nil
	})
}

func (s *Server) runtimeStop(w http.ResponseWriter, r *http.Request) {
	s.queueDashboardRuntimeAction(w, r, "stop")
}

func (s *Server) runtimeRestart(w http.ResponseWriter, r *http.Request) {
	s.queueDashboardRuntimeAction(w, r, "restart")
}

func (s *Server) startupStatus(w http.ResponseWriter, r *http.Request) {
	startup := s.startupSnapshot()
	if s.operations != nil {
		op := s.operations.Current()
		if op.Phase == controller.PhaseQueued || op.Phase == controller.PhaseRunning {
			startup.Phase = StartupStarting
			startup.Message = op.Message
			startup.running = true
		}
		if op.Phase == controller.PhaseFailed || op.Phase == controller.PhaseCancelled {
			startup.Phase = StartupNeedsAttention
			startup.Message = op.Error
			if startup.Message == "" {
				startup.Message = op.Message
			}
			startup.running = false
		}
	}
	restartFailure := serviceRestartResultFailure(s.composeDir)
	restartApplied := serviceRestartResultApplied(s.composeDir, s.cfg.Path())
	if restartFailure != "" {
		startup.Phase = StartupNeedsAttention
		startup.Message = restartFailure
		if startup.Message == "" {
			startup.Message = "Service restart failed. Run: credimi-runner service restart"
		}
	}
	if !startup.running && restartFailure == "" {
		if s.serviceRestartRequired() && !restartApplied {
			active, err := servicecoordination.CoordinatorActive(s.composeDir, dashboardNow())
			if err != nil || !active {
				startup.Phase = StartupNeedsAttention
				startup.Message = serviceRestartManualMessage
			} else if startup.Phase == StartupIdle || startup.Phase == StartupStarting {
				startup.Phase = StartupStarting
				startup.Message = "Configuration saved. Waiting for the attached Credimi Runner to restart the service."
			}
		} else if s.cfg.Exists() {
			status := s.runtime.Status()
			switch {
			case status.Actual == runtimesupervisor.ActualRunning:
				startup.Phase, startup.Message = StartupReady, "Runner is ready."
			case status.Actual == runtimesupervisor.ActualStarting:
				startup.Phase, startup.Message = StartupStarting, "Starting runner services."
			case status.Actual == runtimesupervisor.ActualFailed:
				startup.Phase, startup.Message = StartupNeedsAttention, status.LastError
			case status.Desired == runtimesupervisor.DesiredStopped && status.Actual == runtimesupervisor.ActualStopped && restartApplied:
				startup.Phase, startup.Message = StartupReady, "Service replacement completed."
			case status.Desired == runtimesupervisor.DesiredRunning && status.Actual == runtimesupervisor.ActualStopped && restartApplied:
				startup.Phase, startup.Message = StartupStarting, "Service replacement completed. Waiting for the runner to start."
			}
		}
	}
	lines := startup.Logs
	if since, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("since")), 10, 64); err == nil && since > 0 {
		start := 0
		if startup.LogBase > 0 && since >= startup.LogBase {
			start = int(since - startup.LogBase)
			if start > len(lines) {
				start = len(lines)
			}
		}
		lines = lines[start:]
	}
	writeJSON(w, map[string]any{
		"phase":   startup.Phase,
		"message": startup.Message,
		"lines":   lines,
		"next_id": startup.LogNextID,
		"running": startup.running,
	})
}

func (s *Server) queueDashboardRuntimeAction(w http.ResponseWriter, r *http.Request, action string) {
	snapshot, err := s.submitRuntimeAction(action)
	if err != nil {
		s.renderRuntimeActionError(w, "overview", err)
		return
	}
	s.writeQueuedRuntimeAction(w, r, snapshot, runtimeActionSuccessMessage(action), "/", false)
}

func (s *Server) renderRuntimeActionError(w http.ResponseWriter, page string, err error) {
	d := s.pageData(page, map[string]any{"Flash": err.Error()})
	html, _ := s.render.FragmentPage(page, d)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errors.Is(err, controller.ErrOperationConflict) {
		w.WriteHeader(http.StatusConflict)
	} else {
		w.WriteHeader(http.StatusBadRequest)
	}
	_, _ = w.Write([]byte(html))
}

func (s *Server) writeQueuedRuntimeAction(w http.ResponseWriter, r *http.Request, snapshot controller.Snapshot, success, refresh string, recovery bool) {
	recoveryOrigin := ""
	if recovery && r != nil {
		recoveryOrigin = s.recoveryOrigin(r)
	}
	trigger, _ := json.Marshal(map[string]any{"runtimeOperation": map[string]string{
		"id":             snapshot.ID,
		"success":        success,
		"refresh":        refresh,
		"recovery":       strconv.FormatBool(recovery),
		"recoveryOrigin": recoveryOrigin,
	}})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("HX-Reswap", "none")
	w.Header().Set("HX-Trigger", string(trigger))
	w.WriteHeader(http.StatusAccepted)
}

type configMutation func(context.Context, *http.Request, func(string)) error

func clonePostForm(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, entries := range values {
		cloned[key] = append([]string(nil), entries...)
	}
	return cloned
}

func equalStringMaps(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func canonicalCompatibilityValues(values map[string]string) (map[string]string, error) {
	cfg, err := dashboardruntime.TypedConfigFromValues(dashboardruntime.Values(values))
	if err != nil {
		return nil, err
	}
	return map[string]string(dashboardruntime.ValuesFromTypedConfig(cfg)), nil
}

// saveRuntimeCandidate makes the persisted configuration check and replacement
// one short critical section. Provisioning happens before this lock is taken.
func (s *Server) saveRuntimeCandidate(base dashboardruntime.Values, store *dashboardruntime.Store, candidate dashboardruntime.RunnerRuntimeConfig) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	current, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		return err
	}
	baseCanonical, err := canonicalCompatibilityValues(map[string]string(base))
	if err != nil {
		return err
	}
	persistedCanonical, err := canonicalCompatibilityValues(map[string]string(current.Snapshot()))
	if err != nil {
		return err
	}
	if store.Exists() != current.Exists() || (store.Exists() && !equalStringMaps(baseCanonical, persistedCanonical)) {
		return errors.New("configuration changed while preparing the device; retry")
	}
	return store.SaveRuntimeConfig(candidate)
}

func (s *Server) queueConfigMutation(w http.ResponseWriter, r *http.Request, page string, action configMutation) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.operations == nil {
		s.operations = controller.NewCoordinator(s.ctx)
	}
	form := clonePostForm(r.PostForm)
	snapshot, err := s.operations.Submit(controller.OperationConfigApply, func(ctx context.Context, progress func(controller.Progress)) error {
		request := r.Clone(ctx)
		request.PostForm = clonePostForm(form)
		request.Form = clonePostForm(form)
		err := action(ctx, request, func(message string) {
			progress(controller.Progress{Message: message})
		})
		if err != nil && page == "setup" {
			s.setStartupState(StartupNeedsAttention, "Setup could not complete: "+err.Error())
		}
		return err
	})
	if err != nil {
		if page == "setup" {
			s.setStartupState(StartupNeedsAttention, "Setup could not complete: "+err.Error())
		}
		s.renderRuntimeActionError(w, page, err)
		return
	}
	s.writeQueuedRuntimeAction(w, r, snapshot, "Configuration updated.", dashboardRefreshPath(page), true)
}

func (s *Server) queueConfigPageSave(w http.ResponseWriter, r *http.Request, page string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	incoming := formValuesMap(r.PostForm)
	if err := s.resolveConfigIdentity(r.Context(), s.cfg.Snapshot(), incoming); err != nil {
		http.Error(w, "configuration validation failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if normalized, err := normalizedConfigValues(s.cfg.Snapshot(), incoming, runtime.GOOS); err != nil {
		http.Error(w, "configuration validation failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	} else if errs := Validate(map[string]string(normalized)); len(errs) > 0 {
		http.Error(w, "configuration validation failed", http.StatusUnprocessableEntity)
		return
	}
	s.queueConfigMutation(w, r, page, func(_ context.Context, innerR *http.Request, progress func(string)) error {
		return s.saveConfigPageSync(innerR, page, progress)
	})
}

func dashboardRefreshPath(page string) string {
	switch page {
	case "setup":
		return "/setup"
	case "devices":
		return "/devices"
	case "config":
		return "/config"
	default:
		return "/"
	}
}

func runtimeActionSuccessMessage(action string) string {
	switch action {
	case "start":
		return "Runner started successfully."
	case "stop":
		return "Runner stopped successfully."
	case "restart":
		return "Runner restarted successfully."
	default:
		return "Runner operation completed successfully."
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

func (s *Server) resolveConfigIdentity(ctx context.Context, current, incoming map[string]string) error {
	currentID := strings.TrimSpace(current["CREDIMI_RUNNER_ID"])
	if currentID == "" {
		return nil
	}
	for _, key := range []string{"CREDIMI_RUNNER_ID", "CREDIMI_RUNNER_NAME", "CREDIMI_RUNNER_ORGANIZATION"} {
		if value, ok := incoming[key]; ok && strings.TrimSpace(value) != strings.TrimSpace(current[key]) {
			return fmt.Errorf("%s cannot be changed after runner setup", key)
		}
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

func describeDiffImpact(diff dashboardruntime.ConfigDiff) string {
	switch {
	case hasApplyClass(diff, dashboardruntime.ApplyServiceRestartRequired):
		return "Save these changes? The persistent service must restart for topology changes to take effect."
	case hasApplyClass(diff, dashboardruntime.ApplyRuntimeRestartRequired):
		return "Save these changes? The runtime generation must restart for them to take effect."
	case hasApplyClass(diff, dashboardruntime.ApplyRuntimeReconcile):
		return "Save these changes? The runtime generation will be reconciled."
	case hasApplyClass(diff, dashboardruntime.ApplyCredimiUpdateRequired):
		return "Save these changes? The runner record in Credimi will be updated."
	default:
		return ""
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
	exists, udid, err := iosSimulatorExists(ctx, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	status.Exists = exists
	status.UDID = udid
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
	udid, err := createIOSSimulator(ctx, strings.TrimSpace(req.Name), strings.TrimSpace(req.DeviceTypeIdentifier), strings.TrimSpace(req.RuntimeIdentifier))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"status": "created", "udid": udid})
}

func (s *Server) androidEmulatorAssetsStatus(w http.ResponseWriter, r *http.Request) {
	baseName := strings.TrimSpace(r.URL.Query().Get("base_name"))
	avdHome := strings.TrimSpace(r.URL.Query().Get("avd_home"))
	goldenRoot := strings.TrimSpace(r.URL.Query().Get("golden_root"))
	goldenPath := strings.TrimSpace(r.URL.Query().Get("golden_path"))
	if baseName == "" {
		baseName = dashboardruntime.DefaultBaseName
	}
	// A phone-only inventory has no emulator-specific indexed block yet. Use
	// the mounted runner paths so adding the first emulator discovers assets
	// already present in the launching user's home directory.
	androidKeysDir, defaultAVDHome, defaultGoldenRoot := emulatorAssetPaths()
	if avdHome == "" {
		avdHome = defaultAVDHome
	}
	if goldenRoot == "" {
		goldenRoot = defaultGoldenRoot
	}
	goldenLeaf := goldenLeafFromPath(goldenPath, baseName)
	status := AndroidEmulatorAssetsStatus{
		BaseName:       baseName,
		AndroidKeysDir: androidKeysDir,
		AVDHome:        avdHome,
		GoldenRoot:     goldenRoot,
		GoldenLeaf:     goldenLeaf,
		AVDPresent:     avdAssetsExistForName(avdHome, baseName),
		GoldenPresent:  goldenAssetsPresentForLeaf(goldenRoot, goldenLeaf),
		AVDOptions:     listAVDOptions(avdHome),
		GoldenOptions:  listGoldenOptions(goldenRoot),
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
	_, defaultAVDHome, defaultGoldenRoot := emulatorAssetPaths()
	if avdHome == "" {
		avdHome = defaultAVDHome
	}
	if goldenRoot == "" {
		goldenRoot = defaultGoldenRoot
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
		if err := downloadAndExtractTarball(ctx, androidtools.DefaultBaseAVDArchiveURL, avdHome, stageProgress("base_avd")); err != nil {
			writeProgress(DownloadProgress{Phase: "error", Error: err.Error()})
			return
		}
	}
	if !goldenAssetsPresentForLeaf(goldenRoot, goldenLeaf) {
		if err := downloadAndExtractTarball(ctx, androidtools.DefaultGoldenArchiveURL, goldenRoot, stageProgress("golden")); err != nil {
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
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"toast":{"value":%q,"tone":"error"}}`, msg))
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
			writeSSE(w, "configured", s.render.Fragment("configured_device_rows", PageData{Runner: s.cfg, Snapshot: s.hub.CurrentSnapshot()}))
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
	if override := strings.ToLower(strings.TrimSpace(os.Getenv("GOOS_OVERRIDE"))); override != "" {
		return override
	}
	return runtime.GOOS
}
