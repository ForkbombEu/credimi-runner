package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/controller/driver"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/launcher"
	"github.com/forkbombeu/credimi-runner/internal/maintenance"
)

// RuntimeControlFileEnv names the private file channel used by the application
// to pause/resume its own execution workers while keeping the dashboard alive.
// Linux runtime-owned setup also uses this private channel to hand execution
// startup to the already-running server after Credimi registration succeeds.
const RuntimeControlFileEnv = "CREDIMI_RUNNER_RUNTIME_CONTROL_FILE"

// ─────────────────────────────────────────────────────────────────────────────
// HTTP server. Stdlib net/http + Go 1.22 pattern routing. htmx for interaction,
// SSE for live status. No third-party deps.
//
// Wire into the CLI via Command(args).
// ─────────────────────────────────────────────────────────────────────────────

//go:embed static/*
var staticFS embed.FS

var (
	lookupPath                  = exec.LookPath
	statPath                    = os.Stat
	dashboardExecutable         = os.Executable
	startDashboardRestartHelper = func(name string, args ...string) error {
		helper := exec.Command(name, args...)
		helper.Stdout = os.Stdout
		helper.Stderr = os.Stderr
		return helper.Start()
	}
	terminateDashboardAfter = func(delay time.Duration, pid int) {
		time.AfterFunc(delay, func() { _ = syscall.Kill(pid, syscall.SIGTERM) })
	}
)

type Server struct {
	cfg                     *Config
	hub                     *Hub
	render                  *Renderer
	composeDir              string
	ctx                     context.Context
	hubCtx                  context.Context
	hubStartOnce            sync.Once
	hubWG                   sync.WaitGroup
	authToken               string
	controllerID            string
	controllerIdentityToken string
	controllerFingerprint   string
	manager                 dashboardruntime.Manager
	operations              *controller.Coordinator
	runnerReady             func(context.Context, map[string]string) error
	lookupPath              func(string) (string, error)
	statPath                func(string) (os.FileInfo, error)
	lastRegistrationStatus  string
	pendingDiff             dashboardruntime.ConfigDiff
	startup                 startupState
	maintenance             maintenance.Status
	maintenanceChecked      bool
	maintenanceChecker      func(context.Context, string, time.Time, string) maintenance.Status
	systemMonitor           *SystemMonitor
	binaryPath              string
	downloadBinary          func(context.Context, *http.Client, string, func(string)) error
	restartDashboard        func(string) error
	startupProgress         func(string)
	runtimeOwned            bool
	launcherSocket          string
	runtimeControlFile      string
	publicURL               string
	mutationMu              sync.Mutex
	mu                      sync.RWMutex
}

type StartupPhase string

const (
	StartupIdle           StartupPhase = "idle"
	StartupStarting       StartupPhase = "starting"
	StartupWaitingRunner  StartupPhase = "waiting_for_runner"
	StartupRegistering    StartupPhase = "registering"
	StartupUpgrading      StartupPhase = "upgrading"
	StartupReady          StartupPhase = "ready"
	StartupNeedsAttention StartupPhase = "needs_attention"
)

const startupLogRetain = 2000

const setupOperationFile = "setup-operation"
const configOperationFile = "config-operation"
const capabilityProvisionTimeout = 10 * time.Minute

// setup-operation is a replacement handoff owned by the setup recovery flow.
// Once reconciliation is accepted, the submitting container may disappear.
// Therefore the submitting container must not delete this file on cancellation.
// Only a terminal recovery path may clear it.
type launcherOperationTerminalError struct{ err error }

func (e *launcherOperationTerminalError) Error() string { return e.err.Error() }

func (e *launcherOperationTerminalError) Unwrap() error { return e.err }

type startupState struct {
	Phase     StartupPhase
	Message   string
	Logs      []string
	LogBase   int64
	LogNextID int64
	running   bool
	done      chan struct{}
}

type managerImageUpgrader interface {
	UpgradeRunnerImage(context.Context, func(string)) error
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
	return NewHandlerWithManagerContextAndIdentity(parent, composeDir, manager, "", "", "")
}

func NewHandlerWithManagerContextAndIdentity(parent context.Context, composeDir string, manager dashboardruntime.Manager, controllerID, identityToken, fingerprint string) (http.Handler, context.CancelFunc, error) {
	return NewHandlerWithManagerContextAndIdentityAndCoordinator(parent, composeDir, manager, controllerID, identityToken, fingerprint, nil)
}

// NewHandlerWithManagerContextAndIdentityAndCoordinator makes all dashboard
// actions and non-HTTP entrypoints share one operation owner.
func NewHandlerWithManagerContextAndIdentityAndCoordinator(parent context.Context, composeDir string, manager dashboardruntime.Manager, controllerID, identityToken, fingerprint string, operations *controller.Coordinator) (http.Handler, context.CancelFunc, error) {
	return newHandlerWithManagerContextAndIdentityAndCoordinator(parent, composeDir, manager, controllerID, identityToken, fingerprint, operations, false, nil)
}

// NewHandlerWithManagerContextAndIdentityAndCoordinatorAndBootstrap starts a
// configured runtime through the dashboard lifecycle controller after the
// handler is ready. It is used only by the plain credimi-runner command.
func NewHandlerWithManagerContextAndIdentityAndCoordinatorAndBootstrap(parent context.Context, composeDir string, manager dashboardruntime.Manager, controllerID, identityToken, fingerprint string, operations *controller.Coordinator) (http.Handler, context.CancelFunc, error) {
	return newHandlerWithManagerContextAndIdentityAndCoordinator(parent, composeDir, manager, controllerID, identityToken, fingerprint, operations, true, nil)
}

// NewHandlerWithManagerContextAndIdentityAndCoordinatorAndBootstrapProgress
// additionally mirrors controller bootstrap progress to the process that
// launched the dashboard.
func NewHandlerWithManagerContextAndIdentityAndCoordinatorAndBootstrapProgress(parent context.Context, composeDir string, manager dashboardruntime.Manager, controllerID, identityToken, fingerprint string, operations *controller.Coordinator, progress func(string)) (http.Handler, context.CancelFunc, error) {
	return newHandlerWithManagerContextAndIdentityAndCoordinator(parent, composeDir, manager, controllerID, identityToken, fingerprint, operations, true, progress)
}

// NewRuntimeOwnedHandler starts the dashboard as part of the already-running
// runner application. It deliberately has no lifecycle manager: container
// ownership belongs to the outer launcher, so dashboard actions cannot create
// a second runner container from inside the managed container.
func NewRuntimeOwnedHandler(parent context.Context, composeDir string, controllerID, identityToken, fingerprint string, operations *controller.Coordinator) (http.Handler, context.CancelFunc, error) {
	return newHandlerWithManagerContextAndIdentityAndCoordinator(parent, composeDir, nil, controllerID, identityToken, fingerprint, operations, false, nil, true)
}

func newHandlerWithManagerContextAndIdentityAndCoordinator(parent context.Context, composeDir string, manager dashboardruntime.Manager, controllerID, identityToken, fingerprint string, operations *controller.Coordinator, bootstrap bool, progress func(string), runtimeOwned ...bool) (http.Handler, context.CancelFunc, error) {
	owned := len(runtimeOwned) > 0 && runtimeOwned[0]
	cfg, err := LoadConfig(composeDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	render, err := NewRenderer()
	if err != nil {
		return nil, nil, fmt.Errorf("templates: %w", err)
	}
	observer := controller.Observer{Drivers: []driver.Driver{driver.Compose{}}}
	hub := NewHubWithObservation(cfg, composeDir, render, func() dashboardruntime.RuntimeStatus {
		if manager == nil && !owned {
			return dashboardruntime.RuntimeStatus{}
		}
		if owned {
			return dashboardruntime.RuntimeStatus{Configured: cfg.Exists(), RunnerRunning: runtimeOperational(filepath.Dir(cfg.Path()))}
		}
		return manager.Status(context.Background())
	}, func(ctx context.Context, values dashboardruntime.Values) controller.ObservedRuntime {
		return observer.Observe(ctx, composeDir, values)
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
	if manager == nil && !owned {
		manager = dashboardruntime.NewLifecycleManager(executable, composeDir, normalized, nil)
	} else if manager != nil {
		manager.Configure(normalized)
	}

	srv := &Server{
		cfg:                     cfg,
		hub:                     hub,
		render:                  render,
		composeDir:              composeDir,
		ctx:                     parent,
		authToken:               strings.TrimSpace(cfg.Get("DASHBOARD_TOKEN")),
		controllerID:            controllerID,
		controllerIdentityToken: identityToken,
		controllerFingerprint:   fingerprint,
		manager:                 manager,
		runtimeOwned:            owned,
		launcherSocket:          strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_LAUNCHER_SOCKET")),
		runtimeControlFile:      strings.TrimSpace(os.Getenv(RuntimeControlFileEnv)),
		operations:              operations,
		lookupPath:              lookupPath,
		statPath:                statPath,
		startup: startupState{
			Phase: StartupIdle,
		},
		startupProgress: progress,
	}
	if srv.operations == nil {
		srv.operations = controller.NewCoordinator(parent)
	}
	srv.runnerReady = func(ctx context.Context, values map[string]string) error {
		return controller.WaitForRunnerReady(ctx, dashboardruntime.Values(values))
	}
	srv.binaryPath = executable
	srv.downloadBinary = maintenance.DownloadLatestBinary
	srv.restartDashboard = scheduleDashboardRestart
	checker := maintenance.Checker{}
	srv.maintenanceChecker = checker.Check

	hubCtx, cancel := context.WithCancel(parent)
	srv.hubCtx = hubCtx
	srv.systemMonitor = NewSystemMonitor(hubCtx, filepath.Dir(cfg.Path()), strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_VERBOSE_LOG_PATH")) != "")

	mux := http.NewServeMux()
	srv.routes(mux)
	if owned && cfg.Exists() && strings.TrimSpace(cfg.Get("CREDIMI_RUNNER_ID")) != "" {
		srv.startExistingRuntimeJob(cfg.Snapshot())
	} else if !owned && bootstrap && cfg.Exists() && strings.TrimSpace(cfg.Get("CREDIMI_RUNNER_ID")) != "" {
		if !srv.bootstrapConfiguredRuntime() {
			srv.startHub()
		}
	} else {
		srv.startHub()
	}
	return srv.auth(mux), func() {
		cancel()
		srv.systemMonitor.Close()
		srv.hubWG.Wait()
	}, nil
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
	mux.HandleFunc("POST /maintenance/upgrade", s.maintenanceUpgrade)
	mux.HandleFunc("POST /maintenance/check", s.maintenanceCheck)
	mux.HandleFunc("POST /runtime/register", s.runtimeRegister)
	mux.HandleFunc("GET /runtime/logs", s.runtimeLogs)
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
	mux.HandleFunc("POST /api/controller/maintenance/upgrade-image", s.controllerUpgradeImage)
}

func (s *Server) connectedAndroidDevices(w http.ResponseWriter, r *http.Request) {
	devices := probeAndroid(r.Context())
	if devices == nil {
		devices = []Device{}
	}
	writeJSON(w, devices)
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
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	deviceID := strings.TrimPrefix(strings.TrimSpace(r.FormValue("device_id")), "/")
	oldValues := dashboardruntime.Values(s.cfg.Snapshot())
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	config, err := store.RuntimeConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
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
		http.Error(w, "unknown device", http.StatusNotFound)
		return
	}
	candidateValues := dashboardruntime.ValuesWithRuntimeDevices(dashboardruntime.Values(oldValues), config.Devices)
	provisionCtx, cancelProvision := context.WithTimeout(s.ctx, capabilityProvisionTimeout)
	defer cancelProvision()
	if err := s.provisionCandidateCapabilities(provisionCtx, candidateValues); err != nil {
		http.Error(w, "device state was not activated because Android capabilities are unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := store.SaveRuntimeConfig(config); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	newValues := dashboardruntime.Values(s.cfg.Snapshot())
	if err := s.applyDeviceChange(r.Context(), oldValues, newValues); err != nil {
		http.Error(w, "device state was saved, but runtime reconciliation failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	// These controls are ordinary dashboard forms as well as API endpoints.
	// Returning JSON to a browser form produced a blank page, which made the
	// enable/disable action look broken even though the file had changed.
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, map[string]any{"device_id": deviceID, "enabled": enabled})
		return
	}
	http.Redirect(w, r, "/devices", http.StatusSeeOther)
}

func (s *Server) deviceRemove(w http.ResponseWriter, r *http.Request) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.FormValue("confirm") != "true" {
		http.Error(w, "confirmation required", http.StatusBadRequest)
		return
	}
	oldValues := dashboardruntime.Values(s.cfg.Snapshot())
	deviceID := strings.TrimPrefix(strings.TrimSpace(r.FormValue("device_id")), "/")
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	config, err := store.RuntimeConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
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
		http.Error(w, "unknown device", http.StatusNotFound)
		return
	}
	config.Devices = devices
	candidateValues := dashboardruntime.ValuesWithRuntimeDevices(dashboardruntime.Values(oldValues), config.Devices)
	provisionCtx, cancelProvision := context.WithTimeout(s.ctx, capabilityProvisionTimeout)
	defer cancelProvision()
	if err := s.provisionCandidateCapabilities(provisionCtx, candidateValues); err != nil {
		http.Error(w, "device removal was not activated because Android capabilities are unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := store.SaveRuntimeConfig(config); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	newValues := dashboardruntime.Values(s.cfg.Snapshot())
	if err := s.applyDeviceChange(r.Context(), oldValues, newValues); err != nil {
		http.Error(w, "device removal was saved, but runtime reconciliation failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	// Device actions are submitted from the dashboard, not a raw API client.
	// Redirecting keeps the user inside the dashboard instead of rendering a
	// transport JSON response after removal.
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, map[string]any{"device_id": deviceID, "removed": true})
		return
	}
	http.Redirect(w, r, "/devices", http.StatusSeeOther)
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

func (s *Server) provisionRuntimeCapabilities(ctx context.Context) error {
	cfg, err := runnerconfig.LoadFile(s.cfg.Path())
	if err != nil {
		return fmt.Errorf("load typed runtime configuration: %w", err)
	}
	return androidtools.EnsureEmulatorReady(ctx, cfg, runtime.GOOS, s.appendStartupLog)
}

// provisionCandidateCapabilities validates and provisions an in-memory
// inventory before it can replace the active TOML. The temporary TOML is
// private scratch state; the active configuration is untouched on failure.
func (s *Server) provisionCandidateCapabilities(_ context.Context, values dashboardruntime.Values) error {
	if s.operations == nil {
		s.operations = controller.NewCoordinator(s.ctx)
	}
	snapshot, err := s.operations.Submit(controller.OperationConfigApply, func(ctx context.Context, progress func(controller.Progress)) error {
		progressFn := func(message string) { progress(controller.Progress{Message: message}) }
		progressFn("Checking Android SDK")
		return provisionCandidateCapabilitiesWithProgress(ctx, values, progressFn)
	})
	if err != nil {
		return err
	}
	completed, err := s.operations.Wait(context.Background(), snapshot.ID)
	if err != nil {
		return err
	}
	if completed.Phase != controller.PhaseSucceeded {
		if completed.Error != "" {
			return errors.New(completed.Error)
		}
		return errors.New(completed.Message)
	}
	return nil
}

func provisionCandidateCapabilitiesWithProgress(ctx context.Context, values dashboardruntime.Values, progress func(string)) error {
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
	return androidtools.EnsureEmulatorReady(ctx, cfg, runtime.GOOS, progress)
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
		if s.authToken == "" || strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" || r.URL.Path == "/internal/controller/identity" {
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
	runtimeStatus := dashboardruntime.RuntimeStatus{}
	if s.manager != nil {
		runtimeStatus = s.manager.Status(context.Background())
	} else if s.runtimeOwned {
		runtimeStatus = dashboardruntime.RuntimeStatus{Configured: s.cfg.Exists(), RunnerRunning: runtimeOperational(filepath.Dir(s.cfg.Path())), PublicURL: s.runtimeOwnedPublicURL()}
	}
	s.mu.RLock()
	runtimeStatus.PendingRestart = hasApplyClass(s.pendingDiff, dashboardruntime.ApplyRestartRequired)
	runtimeStatus.PendingRecreate = hasApplyClass(s.pendingDiff, dashboardruntime.ApplyComposeRecreate)
	runtimeStatus.PendingCredimiUpdate = hasApplyClass(s.pendingDiff, dashboardruntime.ApplyCredimiUpdateRequired)
	maintenanceStatus := s.maintenance
	s.mu.RUnlock()
	payloadMap := map[string]any{
		"RuntimeStatus":                 runtimeStatus,
		"RuntimeOwned":                  s.runtimeOwned,
		"LauncherControlAvailable":      s.launcherSocket != "",
		"NativeRuntimeControlAvailable": s.runtimeControlFile != "" && s.launcherSocket == "",
		"Startup":                       s.startupSnapshot(),
		"RunnerVersion":                 buildinfo.String(),
		"Maintenance":                   maintenanceStatus,
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

// runtimeOwnedPublicURL reloads the launcher-owned quick-tunnel state after a
// runner container replacement. The URL is ephemeral launcher state, not TOML,
// so a newly created Dashboard cannot rely only on its in-memory value.
func (s *Server) runtimeOwnedPublicURL() string {
	if !s.runtimeOwned || s.launcherSocket == "" || normalizedApplyServiceMode(s.cfg.Get("CREDIMI_SERVICE_MODE")) != "auto" {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.publicURL
	}

	publicURL, err := launcher.ReadQuickTunnelURL(filepath.Dir(s.cfg.Path()))
	if err != nil {
		publicURL = ""
	}
	s.mu.Lock()
	s.publicURL = publicURL
	s.mu.Unlock()
	return publicURL
}

type executionState string

const (
	executionStateRunning    executionState = "running"
	executionStateStarting   executionState = "starting"
	executionStateRestarting executionState = "restarting"
	executionStateStopped    executionState = "stopped"
	executionStateFailed     executionState = "failed"
)

func readExecutionState(configDir string) executionState {
	raw, err := os.ReadFile(filepath.Join(configDir, "runtime-state"))
	if err != nil {
		// A configured runner with no state marker is the normal state on the
		// first process start. The marker is only authoritative after an explicit
		// lifecycle transition has written it.
		return executionStateRunning
	}
	state := strings.TrimSpace(string(raw))
	if strings.HasPrefix(state, "failed:") {
		return executionStateFailed
	}
	switch executionState(state) {
	case executionStateRunning, executionStateStarting, executionStateRestarting, executionStateStopped:
		return executionState(state)
	default:
		return executionStateFailed
	}
}

func runtimeOperational(configDir string) bool {
	switch readExecutionState(configDir) {
	case executionStateRunning, executionStateStarting, executionStateRestarting:
		return true
	default:
		return false
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func setupOperationPath(configDir string) string {
	return filepath.Join(configDir, setupOperationFile)
}

func configOperationPath(configDir string) string {
	return filepath.Join(configDir, configOperationFile)
}

func readSetupOperation(configDir string) (string, error) {
	contents, err := os.ReadFile(setupOperationPath(configDir))
	if err != nil {
		return "", fmt.Errorf("read setup operation state: %w", err)
	}
	operationID := strings.TrimSpace(string(contents))
	if operationID == "" {
		return "", errors.New("setup operation state is empty")
	}
	return operationID, nil
}

func clearSetupOperation(configDir string) error {
	err := os.Remove(setupOperationPath(configDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove setup operation state: %w", err)
	}
	return nil
}

func readConfigOperation(configDir string) (string, error) {
	contents, err := os.ReadFile(configOperationPath(configDir))
	if err != nil {
		return "", fmt.Errorf("read config operation state: %w", err)
	}
	operationID := strings.TrimSpace(string(contents))
	if operationID == "" {
		return "", errors.New("config operation state is empty")
	}
	return operationID, nil
}

func clearConfigOperation(configDir string) error {
	err := os.Remove(configOperationPath(configDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove config operation state: %w", err)
	}
	return nil
}

func clearSetupPending(configDir string) error {
	err := os.Remove(filepath.Join(configDir, "setup-pending"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove setup pending state: %w", err)
	}
	return nil
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
	image := ""
	if runtime.GOOS != "darwin" {
		var pullPolicy string
		var err error
		image, pullPolicy, err = dashboardruntime.SharedRunnerImage(s.cfg.Snapshot(), runtime.GOOS)
		if err != nil || pullPolicy == "never" {
			image = ""
		}
	}
	status := checker(ctx, buildinfo.String(), buildinfo.BuiltAt(), image)
	s.mu.Lock()
	s.maintenance = status
	s.mu.Unlock()
}

func (s *Server) maintenanceCheck(w http.ResponseWriter, r *http.Request) {
	s.ensureMaintenanceChecked(r.Context(), true)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"checked":true}`))
}

func scheduleDashboardRestart(stagedBinary string) error {
	target, err := dashboardExecutable()
	if err != nil {
		return err
	}
	args := []string{"restart-dashboard-helper", "--wait-pid", strconv.Itoa(os.Getpid()), "--target", target, "--staged", stagedBinary}
	for _, arg := range os.Args[1:] {
		args = append(args, "--restart-arg", arg)
	}
	if err := startDashboardRestartHelper(target, args...); err != nil {
		return err
	}
	terminateDashboardAfter(2*time.Second, os.Getpid())
	return nil
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
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	values := s.cfg.Snapshot()
	oldValues := dashboardruntime.Values(cloneStringMap(values))
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	config, err := store.RuntimeConfig()
	if err != nil {
		config = dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values(values)}
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
			for _, field := range valueAliases {
				if value, ok := postedValue(field.forms...); ok {
					if value == "" {
						delete(device.Values, field.key)
					} else {
						device.Values[field.key] = value
					}
				}
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
			http.Error(w, "device not found", http.StatusNotFound)
			return
		}
		if err := dashboardruntime.ValidateDeviceRegistration(device); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		key := strings.TrimSpace(values["CREDIMI_USER_API_KEY"])
		if key == "" {
			key = strings.TrimSpace(values["CREDIMI_INTERNAL_ADMIN_KEY"])
		}
		if deviceID == "" {
			preview, err := (&dashboardruntime.CredimiClient{BaseURL: values["CREDIMI_URL"], APIKey: key, HTTPClient: http.DefaultClient}).PreviewDeviceID(r.Context(), values["CREDIMI_RUNNER_ID"], device.Name, values["CREDIMI_RUNNER_ORGANIZATION"])
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			deviceID = preview.DeviceID
		}
		device.ID = strings.TrimPrefix(deviceID, "/")
		applyDeviceDefaults(&device)
		config.Devices = append(config.Devices, device)
	}
	if err := dashboardruntime.ValidateDeviceConstraints(config.Devices); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	candidateValues := dashboardruntime.ValuesWithRuntimeDevices(dashboardruntime.Values(values), config.Devices)
	provisionCtx, cancelProvision := context.WithTimeout(s.ctx, capabilityProvisionTimeout)
	defer cancelProvision()
	if err := s.provisionCandidateCapabilities(provisionCtx, candidateValues); err != nil {
		http.Error(w, "device configuration was not activated because Android capabilities are unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := store.SaveRuntimeConfig(config); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	newValues := dashboardruntime.Values(s.cfg.Snapshot())
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, runtimeGOOS())
	runtimeRunning := false
	if s.runtimeOwned {
		runtimeRunning = runtimeOperational(filepath.Dir(s.cfg.Path()))
	}
	if s.manager != nil {
		status := s.manager.Status(r.Context())
		runtimeRunning = status.RunnerRunning || status.ComposeRunning
		s.manager.Configure(newValues)
	}
	if hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) && !s.runtimeOwned {
		if err := dashboardruntime.WriteComposeFile(s.composeDir, newValues); err != nil {
			http.Error(w, "device configuration was saved, but compose generation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if s.runtimeOwned {
		if err := s.applyRuntimeOwnedConfig(diff, newValues); err != nil {
			http.Error(w, "device configuration was saved, but applying it to the running runner failed: "+err.Error(), http.StatusBadGateway)
			return
		}
	} else if runtimeRunning {
		// The live activity provider reads the saved inventory on each target
		// activity; registration is the only immediate runtime action needed.
		if _, err := s.applySavedConfig(diff, map[string]string(newValues)); err != nil {
			http.Error(w, "device configuration was saved, but applying it to the running runner failed: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	http.Redirect(w, r, "/devices", http.StatusSeeOther)
}

func (s *Server) applyDeviceChange(ctx context.Context, oldValues, newValues dashboardruntime.Values) error {
	diff := dashboardruntime.DiffValuesForOS(oldValues, newValues, runtimeGOOS())
	if s.runtimeOwned {
		if !runtimeOperational(filepath.Dir(s.cfg.Path())) && !hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) && !hasApplyClass(diff, dashboardruntime.ApplyRestartRequired) {
			return nil
		}
		return s.applyRuntimeOwnedConfig(diff, map[string]string(newValues))
	}
	if s.manager == nil {
		return nil
	}
	s.manager.Configure(newValues)
	status := s.manager.Status(ctx)
	if !status.RunnerRunning && !status.ComposeRunning {
		return nil
	}
	_, err := s.applySavedConfig(diff, map[string]string(newValues))
	return err
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
		set("REDROID_DATA_DIR", "/home/credimi/redroid-data")
		set("REDROID_DATA_TAR", "/home/credimi/redroid-data.tar")
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

func (s *Server) saveConfigPage(w http.ResponseWriter, r *http.Request, page string) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
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
	candidateSnapshot, err := normalizedConfigValues(oldSnapshot, incoming, runtimeGOOS())
	if err != nil {
		http.Error(w, "configuration validation failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	provisionCtx, cancelProvision := context.WithTimeout(s.ctx, capabilityProvisionTimeout)
	defer cancelProvision()
	if err := s.provisionCandidateCapabilities(provisionCtx, candidateSnapshot); err != nil {
		http.Error(w, "configuration was not activated because Android capabilities are unavailable: "+err.Error(), http.StatusBadGateway)
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
	diff := dashboardruntime.DiffValuesForOS(dashboardruntime.Values(oldSnapshot), dashboardruntime.Values(newSnapshot), runtimeGOOS())
	if s.manager != nil {
		s.manager.Configure(dashboardruntime.Values(newSnapshot))
	}
	if hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) && !s.runtimeOwned {
		if err := WriteComposeFile(s.composeDir, newSnapshot); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	message := "Configuration updated."
	appliedCleanly := true
	if s.manager != nil {
		if outcome, err := s.applySavedConfig(diff, newSnapshot); err != nil {
			message = "Configuration update failed: " + err.Error()
			appliedCleanly = false
		} else if outcome.Restarted {
			message = "Runner restarted with the new configuration."
		}
	} else if s.runtimeOwned && len(diff.ChangedKeys) > 0 {
		if err := s.applyRuntimeOwnedConfig(diff, newSnapshot); err != nil {
			message = "Configuration saved, but applying it to the outer launcher failed: " + err.Error()
			appliedCleanly = false
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
	diff := dashboardruntime.DiffValuesForOS(dashboardruntime.Values(current), normalized, runtimeGOOS())
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

func (s *Server) applySavedConfig(diff dashboardruntime.ConfigDiff, values map[string]string) (applyOutcome, error) {
	var outcome applyOutcome
	if len(diff.ChangedKeys) == 0 || hasApplyClass(diff, dashboardruntime.ApplySavedOnly) {
		return outcome, nil
	}
	status := s.manager.Status(context.Background())
	runtimeRunning := status.RunnerRunning || status.ComposeRunning
	restartRequired := runtimeRunning && (hasApplyClass(diff, dashboardruntime.ApplyRestartRequired) || hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate))
	registerRequired := shouldRegisterAfterApply(diff, values, restartRequired)
	if !restartRequired && !registerRequired {
		return outcome, nil
	}
	kind := controller.OperationRegistration
	if restartRequired {
		kind = controller.OperationRuntimeRestart
	}
	err := s.runLifecycleOperation(kind, func(ctx context.Context) error {
		if restartRequired {
			if err := s.runtimeLifecycle(values).Restart(ctx, nil); err != nil {
				return err
			}
			outcome.Restarted = true
			// Restart performs registration itself. Do not register a second time.
			outcome.CredimiUpdated = true
			return nil
		}
		if err := s.registerCurrent(ctx, values); err != nil {
			return err
		}
		outcome.CredimiUpdated = true
		return nil
	})
	return outcome, err
}

func (s *Server) runLifecycleOperation(kind controller.OperationKind, action func(context.Context) error) error {
	if s.operations == nil {
		s.operations = controller.NewCoordinator(s.ctx)
	}
	snapshot, err := s.operations.Submit(kind, func(ctx context.Context, _ func(controller.Progress)) error {
		return action(ctx)
	})
	if err != nil {
		return err
	}
	completed, err := s.operations.Wait(context.Background(), snapshot.ID)
	if err != nil {
		return err
	}
	if completed.Phase != controller.PhaseSucceeded {
		if completed.Error != "" {
			return errors.New(completed.Error)
		}
		return errors.New(completed.Message)
	}
	return nil
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
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	incoming := formValuesMap(r.PostForm)
	// Setup resolves the runner and every device through Credimi before it can
	// persist the inventory. Do not let an unavailable Credimi endpoint leave
	// the wizard's request open forever while its startup status stays idle.
	setupCtx, cancelSetup := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancelSetup()
	if errs := validateSetupInput(incoming); len(errs) > 0 {
		d := s.pageData("setup", map[string]any{"Errors": errs, "SetupError": "Some fields need attention."})
		html, _ := s.render.FragmentPage("setup", d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(html))
		return
	}
	if err := s.resolveSetupIdentity(setupCtx, incoming); err != nil {
		s.renderSetupError(w, incoming, "identity resolution failed: "+err.Error())
		return
	}
	candidate, err := normalizedConfigValues(s.cfg.Snapshot(), incoming, runtimeGOOS())
	if err != nil {
		s.renderSetupError(w, incoming, "configuration validation failed: "+err.Error())
		return
	}
	if errs := Validate(map[string]string(candidate)); len(errs) > 0 {
		d := s.pageData("setup", map[string]any{"Errors": errs, "SetupError": "Configuration validation failed."})
		html, _ := s.render.FragmentPage("setup", d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(html))
		return
	}
	devices, err := s.setupDevices(r.WithContext(setupCtx), map[string]string(candidate))
	if err != nil {
		s.renderSetupError(w, incoming, "device setup failed: "+err.Error())
		return
	}
	values := dashboardruntime.ValuesWithRuntimeDevices(candidate, devices)
	if err := s.validateRuntimeRequirements(map[string]string(values)); err != nil {
		s.renderSetupError(w, map[string]string(values), "runtime requirement check failed: "+err.Error())
		return
	}
	provisionCtx, cancelProvision := context.WithTimeout(s.ctx, capabilityProvisionTimeout)
	defer cancelProvision()
	if err := s.provisionCandidateCapabilities(provisionCtx, values); err != nil {
		s.renderSetupError(w, map[string]string(values), "Android capabilities are unavailable: "+err.Error())
		return
	}
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		s.renderSetupError(w, incoming, "configuration store failed: "+err.Error())
		return
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: candidate, Devices: devices}); err != nil {
		s.renderSetupError(w, incoming, "device inventory failed: "+err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.cfg.Path()), "setup-pending"), []byte("pending\n"), 0o600); err != nil {
		s.renderSetupError(w, map[string]string(values), "setup state could not be recorded: "+err.Error())
		return
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	values = dashboardruntime.Values(s.cfg.Snapshot())
	if s.manager != nil {
		s.manager.Configure(values)
	}
	if !s.runtimeOwned {
		if err := dashboardruntime.WriteComposeFile(s.composeDir, values); err != nil {
			s.renderSetupError(w, map[string]string(values), "compose generation failed: "+err.Error())
			return
		}
	}
	if s.runtimeOwned {
		s.startRuntimeOwnedRegistration(map[string]string(values))
	} else {
		s.startStartupJob(map[string]string(values))
	}
	s.renderSetupComplete(w, r)
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
			return strings.TrimSpace(r.PostForm.Get(prefix + field))
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
		for _, field := range []string{"BASE_NAME", "ANDROID_KEYS_DIR", "GOLDEN_PATH", "HOST_AVD_HOME_PATH", "HOST_AVD_GOLDEN_PATH", "REDROID_DATA_DIR", "REDROID_DATA_TAR", "IOS_UDID"} {
			if fieldValue := value(field); fieldValue != "" {
				device.Values[field] = fieldValue
			}
		}
		if device.Type == "android_phone" && device.Mode == "usb" && device.Serial == "" {
			return nil, fmt.Errorf("device %d requires a selected USB Android device", index)
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

func (s *Server) startStartupJob(values map[string]string) {
	s.startTrackedStartupOperation(controller.OperationRuntimeStart, startupState{
		Phase:     StartupStarting,
		Message:   "Setup saved. Starting runtime.",
		LogBase:   1,
		LogNextID: 1,
	}, func(ctx context.Context) error {
		s.appendStartupLog("Starting runtime services.")
		if err := s.runtimeLifecycle(cloneStringMap(values)).Start(ctx, s.appendStartupLog); err != nil {
			s.setStartupState(StartupNeedsAttention, "Setup saved, but runtime start failed: "+err.Error())
			return err
		}
		s.mu.Lock()
		s.pendingDiff = dashboardruntime.ConfigDiff{}
		s.mu.Unlock()
		s.setStartupState(StartupReady, "Setup complete. Runner started and registered with Credimi.")
		return nil
	})
}

func (s *Server) startRuntimeOwnedRegistration(values map[string]string) {
	s.startTrackedStartupOperation(controller.OperationRegistration, startupState{
		Phase:     StartupRegistering,
		Message:   "Setup saved. Registering the in-container runner.",
		LogBase:   1,
		LogNextID: 1,
	}, func(ctx context.Context) error {
		operationID := ""
		if s.launcherSocket != "" {
			s.setStartupState(StartupWaitingRunner, "Setup saved. Waiting for the outer launcher to apply the final runtime topology.")
			handle, err := launcher.RequestSetupReconcileAsync(ctx, s.launcherSocket)
			if err != nil {
				s.setStartupState(StartupNeedsAttention, "Setup saved, but launcher reconciliation failed: "+err.Error())
				return err
			}
			operationID = handle.ID
		}
		return s.finishSetupRegistration(ctx, values, operationID, false)
	})
}

func (s *Server) finishSetupRegistration(ctx context.Context, values map[string]string, operationID string, consumeHandoff bool) error {
	if operationID != "" {
		if err := s.waitForLauncherOperation(ctx, operationID); err != nil {
			if !consumeHandoff && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				// The submitting dashboard is commonly cancelled because the
				// launcher is replacing its container. The handoff remains owned
				// by the replacement and must not be turned into a failure here.
				return nil
			}
			if consumeHandoff {
				var terminalErr *launcherOperationTerminalError
				if errors.As(err, &terminalErr) {
					if cleanupErr := clearSetupOperation(filepath.Dir(s.cfg.Path())); cleanupErr != nil {
						err = fmt.Errorf("%w; also could not clear setup operation state: %v", err, cleanupErr)
					}
				}
			}
			s.setStartupState(StartupNeedsAttention, "Setup saved, but launcher reconciliation failed: "+err.Error())
			return err
		}
		s.setStartupState(StartupRegistering, "Runtime is ready. Resolving the public URL and registering the runner.")
	}
	if err := s.runtimeLifecycle(cloneStringMap(values)).RegisterRunning(ctx); err != nil {
		s.setStartupState(StartupNeedsAttention, "Setup saved, but runner registration failed: "+err.Error())
		if operationID != "" && consumeHandoff {
			if cleanupErr := clearSetupOperation(filepath.Dir(s.cfg.Path())); cleanupErr != nil {
				return fmt.Errorf("%w; also could not clear setup operation state: %v", err, cleanupErr)
			}
		}
		return err
	}
	if err := s.startSetupRuntime(ctx); err != nil {
		s.setStartupState(StartupNeedsAttention, "Runner registered, but execution runtime could not start: "+err.Error())
		if operationID != "" && consumeHandoff {
			if cleanupErr := clearSetupOperation(filepath.Dir(s.cfg.Path())); cleanupErr != nil {
				return fmt.Errorf("%w; also could not clear setup operation state: %v", err, cleanupErr)
			}
		}
		return err
	}
	if err := clearSetupOperation(filepath.Dir(s.cfg.Path())); err != nil {
		s.setStartupState(StartupNeedsAttention, "Setup completed, but setup state could not be cleared: "+err.Error())
		return err
	}
	if err := clearSetupPending(filepath.Dir(s.cfg.Path())); err != nil {
		s.setStartupState(StartupNeedsAttention, "Setup completed, but setup state could not be cleared: "+err.Error())
		return err
	}
	s.setStartupState(StartupReady, "Setup complete. Runner registered with Credimi.")
	return nil
}

func (s *Server) waitForLauncherOperation(ctx context.Context, operationID string) error {
	if s.launcherSocket == "" {
		return errors.New("launcher control socket is not configured")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		result, err := launcher.RequestOperationStatus(waitCtx, s.launcherSocket, operationID)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "operation not found") {
				return &launcherOperationTerminalError{err: err}
			}
			return err
		}
		switch result.Phase {
		case launcher.PhaseSucceeded:
			return nil
		case launcher.PhaseFailed:
			if result.Error != "" {
				return &launcherOperationTerminalError{err: errors.New(result.Error)}
			}
			if result.Message != "" {
				return &launcherOperationTerminalError{err: errors.New(result.Message)}
			}
			return &launcherOperationTerminalError{err: errors.New("launcher operation failed")}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("timed out waiting for launcher operation %s: %w", operationID, waitCtx.Err())
		case <-timer.C:
		}
	}
}

func (s *Server) startSetupRuntime(ctx context.Context) error {
	return s.startRegisteredRuntime(ctx, "setup-ready")
}

func (s *Server) startRegisteredRuntime(ctx context.Context, controlAction string) error {
	if strings.TrimSpace(s.runtimeControlFile) == "" {
		return nil
	}
	configDir := filepath.Dir(s.cfg.Path())
	if err := os.WriteFile(filepath.Join(configDir, "runtime-state"), []byte("starting\n"), 0o600); err != nil {
		return fmt.Errorf("mark runtime startup: %w", err)
	}
	var controlErr error
	if controlAction == "setup-ready" {
		controlErr = writeSetupRuntimeControl(s.runtimeControlFile)
	} else {
		controlErr = writeRuntimeReadyControl(s.runtimeControlFile, controlAction)
	}
	if controlErr != nil {
		return controlErr
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, err := os.ReadFile(filepath.Join(configDir, "runtime-state"))
		if err == nil {
			state := strings.TrimSpace(string(raw))
			if state == "running" {
				return nil
			}
			if strings.HasPrefix(state, "failed:") {
				return fmt.Errorf("runtime startup failed: %s", strings.TrimSpace(strings.TrimPrefix(state, "failed:")))
			}
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timed out waiting for execution runtime: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Server) applyRuntimeOwnedRegistration(values map[string]string) error {
	return s.runLifecycleOperation(controller.OperationRegistration, func(ctx context.Context) error {
		return s.registerCurrent(ctx, values)
	})
}

func (s *Server) applyRuntimeOwnedConfig(diff dashboardruntime.ConfigDiff, values map[string]string) error {
	if s.launcherSocket != "" && (hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate) || hasApplyClass(diff, dashboardruntime.ApplyRestartRequired)) {
		handle, err := launcher.RequestReconcileAsync(context.Background(), s.launcherSocket)
		if err != nil {
			return err
		}
		s.startConfigReconcileRecovery(values, handle.ID, false)
		return nil
	}
	if s.runtimeOwned && !runtimeOperational(filepath.Dir(s.cfg.Path())) {
		return nil
	}
	return s.applyRuntimeOwnedRegistration(values)
}

func (s *Server) bootstrapConfiguredRuntime() bool {
	if s.manager == nil {
		return false
	}
	values := s.cfg.Snapshot()
	status := s.manager.Status(context.Background())
	if !status.RunnerRunning && !status.ComposeRunning {
		s.startStartupJob(values)
		return true
	}
	if follower, ok := s.manager.(interface{ StartLogFollower() }); ok {
		follower.StartLogFollower()
	}
	s.startExistingRuntimeJob(values)
	return true
}

func (s *Server) startExistingRuntimeJob(values map[string]string) {
	s.startTrackedStartupOperation(controller.OperationRegistration, startupState{
		Phase:     StartupWaitingRunner,
		Message:   "Runner already running. Verifying runtime and registration.",
		LogBase:   1,
		LogNextID: 1,
	}, func(ctx context.Context) error {
		setupPending := fileExists(filepath.Join(filepath.Dir(s.cfg.Path()), "setup-pending"))
		if s.runtimeOwned && s.launcherSocket != "" && setupPending {
			operationID, err := readSetupOperation(filepath.Dir(s.cfg.Path()))
			if err != nil {
				message := "Setup cannot resume because the launcher reconciliation operation is unavailable: " + err.Error()
				if !errors.Is(err, os.ErrNotExist) {
					_ = clearSetupOperation(filepath.Dir(s.cfg.Path()))
				}
				s.setStartupState(StartupNeedsAttention, message)
				return errors.New(message)
			}
			return s.finishSetupRegistration(ctx, values, operationID, true)
		}
		configDir := filepath.Dir(s.cfg.Path())
		if s.runtimeOwned && s.launcherSocket != "" && fileExists(configOperationPath(configDir)) {
			operationID, err := readConfigOperation(configDir)
			if err != nil {
				message := "Configuration cannot resume because the launcher reconciliation operation is unavailable: " + err.Error()
				_ = clearConfigOperation(configDir)
				s.setStartupState(StartupNeedsAttention, message)
				return errors.New(message)
			}
			return s.finishConfigReconcileRecovery(ctx, values, operationID, true)
		}
		if s.runtimeOwned && !runtimeOperational(configDir) {
			s.setStartupState(StartupReady, "Runner is stopped. Start Runner to begin execution.")
			return nil
		}
		s.setStartupState(StartupRegistering, "Runner already running. Updating Credimi registration.")
		if err := s.runtimeLifecycle(cloneStringMap(values)).RegisterRunning(ctx); err != nil {
			s.mu.Lock()
			s.pendingDiff = dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyCredimiUpdateRequired}}
			s.mu.Unlock()
			s.setStartupState(StartupNeedsAttention, "Runner is already running, but Credimi registration failed: "+err.Error())
			return err
		}
		if err := s.startRegisteredRuntime(ctx, "registration-ready"); err != nil {
			s.setStartupState(StartupNeedsAttention, "Runner registered, but execution runtime could not start: "+err.Error())
			return err
		}
		s.mu.Lock()
		s.pendingDiff = dashboardruntime.ConfigDiff{}
		s.mu.Unlock()
		s.setStartupState(StartupReady, "Runner already running and registered with Credimi.")
		if s.runtimeOwned {
			if err := clearSetupPending(filepath.Dir(s.cfg.Path())); err != nil {
				s.setStartupState(StartupNeedsAttention, "Runner registered, but setup state could not be cleared: "+err.Error())
				return err
			}
		}
		return nil
	})
}

func (s *Server) startConfigReconcileRecovery(values map[string]string, operationID string, consumeHandoff bool) {
	s.startTrackedStartupOperation(controller.OperationRegistration, startupState{
		Phase:     StartupWaitingRunner,
		Message:   "Configuration saved. Waiting for the outer launcher to apply the runtime topology.",
		LogBase:   1,
		LogNextID: 1,
	}, func(ctx context.Context) error {
		return s.finishConfigReconcileRecovery(ctx, values, operationID, consumeHandoff)
	})
}

func (s *Server) finishConfigReconcileRecovery(ctx context.Context, values map[string]string, operationID string, consumeHandoff bool) error {
	configDir := filepath.Dir(s.cfg.Path())
	if err := s.waitForLauncherOperation(ctx, operationID); err != nil {
		if !consumeHandoff && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return nil
		}
		var terminalErr *launcherOperationTerminalError
		if consumeHandoff && errors.As(err, &terminalErr) {
			_ = clearConfigOperation(configDir)
		}
		message := "Configuration reconciliation failed: " + err.Error()
		s.setStartupState(StartupNeedsAttention, message)
		return errors.New(message)
	}
	if runtimeOperational(configDir) {
		if err := s.runtimeLifecycle(cloneStringMap(values)).RegisterRunning(ctx); err != nil {
			if consumeHandoff {
				_ = clearConfigOperation(configDir)
			}
			message := "Configuration was applied, but runner registration failed: " + err.Error()
			s.setStartupState(StartupNeedsAttention, message)
			return errors.New(message)
		}
		if err := s.startRegisteredRuntime(ctx, "registration-ready"); err != nil {
			if consumeHandoff {
				_ = clearConfigOperation(configDir)
			}
			message := "Configuration was applied, but execution runtime could not start: " + err.Error()
			s.setStartupState(StartupNeedsAttention, message)
			return errors.New(message)
		}
	}
	if err := clearConfigOperation(configDir); err != nil {
		s.setStartupState(StartupNeedsAttention, "Configuration applied, but operation state could not be cleared: "+err.Error())
		return err
	}
	s.mu.Lock()
	s.pendingDiff = dashboardruntime.ConfigDiff{}
	s.mu.Unlock()
	s.setStartupState(StartupReady, "Configuration applied successfully.")
	return nil
}

func (s *Server) startTrackedStartupOperation(kind controller.OperationKind, state startupState, action func(context.Context) error) {
	s.mu.Lock()
	done := make(chan struct{})
	state.running = true
	state.done = done
	s.startup = state
	s.appendStartupLogLocked(s.startup.Message)
	s.lastRegistrationStatus = s.startup.Message
	s.mu.Unlock()

	if s.operations == nil {
		s.operations = controller.NewCoordinator(s.ctx)
	}
	snapshot, err := s.operations.Submit(kind, func(ctx context.Context, _ func(controller.Progress)) error {
		return action(ctx)
	})
	if err != nil {
		s.setStartupState(StartupNeedsAttention, "Runtime operation could not start: "+err.Error())
		s.mu.Lock()
		if s.startup.done == done {
			s.startup.running = false
		}
		s.mu.Unlock()
		close(done)
		s.startHub()
		return
	}
	go func() {
		defer close(done)
		completed, err := s.operations.Wait(context.Background(), snapshot.ID)
		if err != nil {
			if s.startupSnapshot().Phase != StartupNeedsAttention {
				s.setStartupState(StartupNeedsAttention, "Runtime operation failed: "+err.Error())
			}
		} else if completed.Phase == controller.PhaseFailed && completed.Error != "" {
			if s.startupSnapshot().Phase != StartupNeedsAttention {
				s.setStartupState(StartupNeedsAttention, completed.Error)
			}
		}
		s.mu.Lock()
		if s.startup.done == done {
			s.startup.running = false
		}
		s.mu.Unlock()
		s.startHub()
	}()
}

func (s *Server) setStartupState(phase StartupPhase, message string) {
	s.mu.Lock()
	s.startup.Phase = phase
	s.startup.Message = message
	if strings.TrimSpace(message) != "" {
		s.appendStartupLogLocked(message)
	}
	s.lastRegistrationStatus = message
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
	if s.startupProgress != nil {
		s.startupProgress(message)
	}
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
	if plan.RequiresDocker && !s.runtimeOwned {
		if _, err := s.lookupPath("docker"); err != nil {
			return errors.New("docker is required for this runner mode")
		}
	}
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
			if device.Mode == "usb" && !s.androidSerialConnected(device.Serial) {
				return fmt.Errorf("device %q is not connected", device.ID)
			}
		case "android_emulator":
			if plan.Backend != dashboardruntime.DefaultContainerBackend {
				continue
			}
			if _, err := s.statPath("/dev/kvm"); err != nil {
				return errors.New("/dev/kvm is required for Android emulator containers")
			}
		}
	}
	return nil
}

func (s *Server) androidSerialConnected(serial string) bool {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return false
	}
	for _, device := range s.hub.CurrentSnapshot().Devices {
		if isAndroidADBDevice(device) && device.Status == Online && device.Serial == serial {
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
	s.queueDashboardRuntimeAction(w, "start")
}

func (s *Server) controllerStatus(w http.ResponseWriter, r *http.Request) {
	status := dashboardruntime.RuntimeStatus{}
	if s.manager != nil {
		status = s.manager.Status(r.Context())
	}
	writeJSON(w, map[string]any{"runtime": status, "operation": s.operations.Current()})
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
		if errors.Is(err, errRuntimeManagerUnavailable) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, snapshot)
}

func (s *Server) controllerUpgradeImage(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.submitImageUpgrade()
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

var errRuntimeManagerUnavailable = errors.New("runtime manager unavailable")

func (s *Server) submitRuntimeAction(action string) (controller.Snapshot, error) {
	if s.manager == nil && (!s.runtimeOwned || (s.launcherSocket == "" && s.runtimeControlFile == "")) {
		return controller.Snapshot{}, errRuntimeManagerUnavailable
	}
	var kind controller.OperationKind
	switch action {
	case "start":
		kind = controller.OperationRuntimeStart
	case "stop":
		kind = controller.OperationRuntimeStop
	case "restart":
		kind = controller.OperationRuntimeRestart
	default:
		return controller.Snapshot{}, fmt.Errorf("unsupported runtime action %q", action)
	}
	if s.operations == nil {
		s.operations = controller.NewCoordinator(s.ctx)
	}
	values := s.cfg.Snapshot()
	snapshot, err := s.operations.Submit(kind, func(ctx context.Context, progress func(controller.Progress)) error {
		if s.manager == nil {
			if s.launcherSocket != "" {
				if err := launcher.RequestRuntimeAction(ctx, s.launcherSocket, action); err != nil {
					return err
				}
				if action == "start" || action == "restart" {
					// The outer launcher may have created a fresh quick-tunnel URL
					// while restoring the execution runtime. Re-register only after
					// it has confirmed the runtime transition.
					if err := s.runtimeLifecycle(values).RegisterRunning(ctx); err != nil {
						return err
					}
					return s.startRegisteredRuntime(ctx, "registration-ready")
				}
				return nil
			}
			return writeNativeRuntimeControl(s.runtimeControlFile, action)
		}
		lifecycle := s.runtimeLifecycle(values)
		progressFn := func(message string) { progress(controller.Progress{Message: message}) }
		switch kind {
		case controller.OperationRuntimeStart:
			if err := lifecycle.Start(ctx, progressFn); err != nil {
				return err
			}
			return s.startRegisteredRuntime(ctx, "registration-ready")
		case controller.OperationRuntimeStop:
			return lifecycle.Stop(ctx)
		case controller.OperationRuntimeRestart:
			if err := lifecycle.Restart(ctx, progressFn); err != nil {
				return err
			}
			return s.startRegisteredRuntime(ctx, "registration-ready")
		default:
			return fmt.Errorf("unsupported runtime operation %q", kind)
		}
	})
	return snapshot, err
}

func writeNativeRuntimeControl(path, action string) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("unsupported runtime action %q", action)
	}
	if strings.TrimSpace(path) == "" {
		return errRuntimeManagerUnavailable
	}
	if err := os.WriteFile(path, []byte(action+"\n"), 0o600); err != nil {
		return fmt.Errorf("write native runtime control: %w", err)
	}
	return nil
}

func writeSetupRuntimeControl(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte("setup-ready\n"), 0o600); err != nil {
		return fmt.Errorf("write setup runtime control: %w", err)
	}
	return nil
}

func writeRuntimeReadyControl(path, action string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if action != "setup-ready" && action != "registration-ready" {
		return fmt.Errorf("unsupported runtime ready action %q", action)
	}
	if err := os.WriteFile(path, []byte(action+"\n"), 0o600); err != nil {
		return fmt.Errorf("write runtime ready control: %w", err)
	}
	return nil
}

func (s *Server) submitImageUpgrade() (controller.Snapshot, error) {
	upgrader, ok := s.manager.(managerImageUpgrader)
	delegated := !ok && s.launcherSocket != ""
	if !ok && !delegated {
		return controller.Snapshot{}, errors.New("runner image upgrade is unavailable")
	}
	if s.operations == nil {
		s.operations = controller.NewCoordinator(s.ctx)
	}
	values := s.cfg.Snapshot()
	return s.operations.Submit(controller.OperationRuntimeRestart, func(ctx context.Context, progress func(controller.Progress)) error {
		progressFn := func(message string) { progress(controller.Progress{Message: message}) }
		if delegated {
			return launcher.RequestUpgrade(ctx, s.launcherSocket)
		}
		if err := upgrader.UpgradeRunnerImage(ctx, progressFn); err != nil {
			return err
		}
		return s.runtimeLifecycle(values).RegisterRunning(ctx)
	})
}

func (s *Server) runtimeLifecycle(values map[string]string) controller.RuntimeLifecycle {
	lifecycle := controller.RuntimeLifecycle{
		Manager: s.manager,
		Values:  dashboardruntime.Values(values),
		GOOS:    runtimeGOOS(),
		WaitReady: func(ctx context.Context, values dashboardruntime.Values) error {
			return s.runnerReady(ctx, values)
		},
		SetPublicURL: func(publicURL string) {
			s.mu.Lock()
			s.publicURL = strings.TrimSpace(publicURL)
			s.mu.Unlock()
		},
	}
	if s.manager != nil {
		if resolver, ok := s.manager.(interface {
			QuickTunnelURL(context.Context) (string, error)
		}); ok {
			lifecycle.QuickTunnelURL = resolver.QuickTunnelURL
		}
		if verifier, ok := s.manager.(interface {
			VerifyPublicURL(context.Context, string) error
		}); ok {
			lifecycle.VerifyPublicURL = verifier.VerifyPublicURL
		}
	} else if s.launcherSocket != "" {
		lifecycle.QuickTunnelURL = func(ctx context.Context) (string, error) {
			// The outer launcher survives runner-container replacement and
			// publishes the current URL after it has observed cloudflared. Do
			// not make registration depend on a socket request racing that
			// replacement.
			return launcher.ReadQuickTunnelURL(filepath.Dir(s.cfg.Path()))
		}
	}
	return lifecycle
}

func (s *Server) runtimeStop(w http.ResponseWriter, r *http.Request) {
	s.queueDashboardRuntimeAction(w, "stop")
}

func (s *Server) runtimeRestart(w http.ResponseWriter, r *http.Request) {
	s.queueDashboardRuntimeAction(w, "restart")
}

func (s *Server) maintenanceUpgrade(w http.ResponseWriter, r *http.Request) {
	done := make(chan struct{})
	ready := make(chan struct{})
	values := s.cfg.Snapshot()

	if s.operations == nil {
		s.operations = controller.NewCoordinator(s.ctx)
	}
	snapshot, submitErr := s.operations.Submit(controller.OperationRuntimeRestart, func(ctx context.Context, _ func(controller.Progress)) error {
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}

		s.mu.RLock()
		available := s.maintenance
		s.mu.RUnlock()
		var err error
		delegated := false
		if available.Image.UpdateAvailable {
			if upgrader, ok := s.manager.(managerImageUpgrader); ok {
				err = upgrader.UpgradeRunnerImage(ctx, s.appendStartupLog)
			} else if s.launcherSocket != "" {
				err = launcher.RequestUpgrade(ctx, s.launcherSocket)
				delegated = true
			} else if s.manager != nil {
				err = s.manager.UpdateImage(ctx)
			}
		}
		if delegated && err == nil {
			s.setStartupState(StartupReady, "Runner image upgrade accepted by the outer launcher.")
			return nil
		}
		if err == nil && dashboardruntime.RunnerReadinessRequiredBeforeRegistration(dashboardruntime.Values(values), runtimeGOOS()) {
			s.setStartupState(StartupWaitingRunner, "Runner image updated. Waiting for runner readiness.")
			err = s.runnerReady(ctx, values)
		}
		if err == nil {
			s.setStartupState(StartupRegistering, "Runner restarted. Discovering its public URL and updating Credimi registration.")
			err = s.registerCurrent(ctx, values)
		}
		stagedBinary := ""
		if err == nil && available.Runner.UpdateAvailable {
			stagedBinary = s.binaryPath + ".upgrade"
			s.appendStartupLog("Downloading the latest Credimi Runner binary.")
			err = s.downloadBinary(ctx, http.DefaultClient, stagedBinary, s.appendStartupLog)
		}
		if err != nil {
			s.setStartupState(StartupNeedsAttention, "Runner upgrade failed: "+err.Error())
			return err
		} else {
			s.setStartupState(StartupReady, "Runner upgrade complete.")
			if stagedBinary != "" {
				s.appendStartupLog("Restarting the Dashboard with the new Runner binary.")
				if restartErr := s.restartDashboard(stagedBinary); restartErr != nil {
					s.setStartupState(StartupNeedsAttention, "Dashboard restart failed: "+restartErr.Error())
					return restartErr
				}
			}
		}
		return nil
	})
	if submitErr != nil {
		s.renderRuntimeActionError(w, "overview", submitErr)
		return
	}
	s.mu.Lock()
	s.startup = startupState{
		Phase:     StartupUpgrading,
		Message:   "Upgrading runner Docker image.",
		LogBase:   1,
		LogNextID: 1,
		running:   true,
		done:      done,
	}
	s.appendStartupLogLocked("Starting runner image upgrade.")
	s.mu.Unlock()
	close(ready)
	go func() {
		defer close(done)
		_, _ = s.operations.Wait(context.Background(), snapshot.ID)
		s.mu.Lock()
		if s.startup.done == done {
			s.startup.running = false
		}
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"started":true}`))
}

func (s *Server) runtimeRegister(w http.ResponseWriter, r *http.Request) {
	s.queueRuntimeAction(w, "overview", controller.OperationRegistration, func(ctx context.Context) error {
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

func (s *Server) queueDashboardRuntimeAction(w http.ResponseWriter, action string) {
	snapshot, err := s.submitRuntimeAction(action)
	if err != nil {
		s.renderRuntimeActionError(w, "overview", err)
		return
	}
	s.writeQueuedRuntimeAction(w, snapshot, runtimeActionSuccessMessage(action), "/")
}

func (s *Server) queueRuntimeAction(w http.ResponseWriter, page string, kind controller.OperationKind, action func(context.Context) error, success string) {
	if s.operations == nil {
		s.operations = controller.NewCoordinator(s.ctx)
	}
	snapshot, err := s.operations.Submit(kind, func(ctx context.Context, _ func(controller.Progress)) error {
		return action(ctx)
	})
	if err != nil {
		s.renderRuntimeActionError(w, page, err)
		return
	}
	s.writeQueuedRuntimeAction(w, snapshot, success, dashboardRefreshPath(page))
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

func (s *Server) writeQueuedRuntimeAction(w http.ResponseWriter, snapshot controller.Snapshot, success, refresh string) {
	trigger, _ := json.Marshal(map[string]any{"runtimeOperation": map[string]string{
		"id":      snapshot.ID,
		"success": success,
		"refresh": refresh,
	}})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("HX-Reswap", "none")
	w.Header().Set("HX-Trigger", string(trigger))
	w.WriteHeader(http.StatusAccepted)
}

func dashboardRefreshPath(page string) string {
	switch page {
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
	if err := s.runtimeLifecycle(values).Register(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastRegistrationStatus = "Credimi runner registration updated."
	s.mu.Unlock()
	return nil
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
