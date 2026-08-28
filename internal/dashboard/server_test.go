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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/controller"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/launcher"
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
	quickTunnelURL   string
	status           dashboardruntime.RuntimeStatus
	startErr         error
	stopErr          error
	restartErr       error
	updateImageErr   error
	logTail          int
	startBlock       chan struct{}
	startStarted     chan struct{}
	upgradeBlock     chan struct{}
}

func (f *fakeManager) Start(ctx context.Context) error {
	f.mu.Lock()
	f.startCalls++
	err := f.startErr
	block := f.startBlock
	started := f.startStarted
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
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
func (f *fakeManager) QuickTunnelURL(context.Context) (string, error) {
	if f.quickTunnelURL == "" {
		return "", errors.New("quick tunnel URL is not configured")
	}
	return f.quickTunnelURL, nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	if os.Getenv("ANDROID_SDK_ROOT") == "" {
		originalCandidateProvisioner := ensureCandidateEmulatorReady
		ensureCandidateEmulatorReady = func(context.Context, runnerconfig.Config, string, androidtools.EmulatorProgress) error { return nil }
		t.Cleanup(func() { ensureCandidateEmulatorReady = originalCandidateProvisioner })
	}
	cfg := &Config{path: filepath.Join(t.TempDir(), "config.toml"), values: map[string]string{}}
	for k, v := range Defaults {
		cfg.values[k] = v
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
	hub := NewHub(cfg, t.TempDir(), render, func() dashboardruntime.RuntimeStatus { return dashboardruntime.RuntimeStatus{} })
	hub.snap = Snapshot{
		Services: []Service{{ID: "runner", Name: "runner", Status: Online}},
		Devices:  []Device{{Serial: "device-1", Name: "Pixel 8", Type: "android_phone", Mode: "usb", Status: Online}},
	}
	hub.workers = []Worker{{ID: "runner-mr", Env: "runner", Status: Online}}
	return &Server{
		cfg:                cfg,
		hub:                hub,
		render:             render,
		composeDir:         t.TempDir(),
		ctx:                context.Background(),
		manager:            &fakeManager{quickTunnelURL: "https://runner.example.trycloudflare.com"},
		runnerReady:        func(context.Context, map[string]string) error { return nil },
		lookupPath:         func(string) (string, error) { return "/tmp/fake-bin", nil },
		statPath:           func(string) (os.FileInfo, error) { return fakeFileInfo("ok"), nil },
		maintenanceChecked: true,
		maintenanceChecker: func(context.Context, string, time.Time, string) maintenance.Status { return maintenance.Status{} },
		downloadBinary:     func(context.Context, *http.Client, string, func(string)) error { return nil },
		restartDashboard:   func(string) error { return nil },
	}
}

func writeDashboardTestConfig(t *testing.T, dir string, dashboardToken string) {
	t.Helper()
	cfg := runnerconfig.Config{
		SchemaVersion: runnerconfig.SchemaVersion,
		Runner:        runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"},
		Credimi:       runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "test"},
		Temporal:      runnerconfig.TemporalConfig{Address: "temporal.example:7233"},
		Server:        runnerconfig.ServerConfig{DashboardToken: dashboardToken},
		Exposure:      runnerconfig.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example"},
		Devices:       []runnerconfig.DeviceConfig{{ID: "acme/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "usb-1"}}},
	}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
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

func TestNewHandlerBootstrapWrappersAndRefreshTargets(t *testing.T) {
	for _, progress := range []bool{false, true} {
		t.Run(fmt.Sprintf("progress-%t", progress), func(t *testing.T) {
			var handler http.Handler
			var cancel context.CancelFunc
			var err error
			if progress {
				handler, cancel, err = NewHandlerWithManagerContextAndIdentityAndCoordinatorAndBootstrapProgress(context.Background(), t.TempDir(), &fakeManager{}, "controller", "token", "fingerprint", nil, func(string) {})
			} else {
				handler, cancel, err = NewHandlerWithManagerContextAndIdentityAndCoordinatorAndBootstrap(context.Background(), t.TempDir(), &fakeManager{}, "controller", "token", "fingerprint", nil)
			}
			if err != nil || handler == nil {
				t.Fatalf("bootstrap handler = %v", err)
			}
			cancel()
		})
	}
	for page, want := range map[string]string{"devices": "/devices", "config": "/config", "overview": "/"} {
		if got := dashboardRefreshPath(page); got != want {
			t.Fatalf("refresh path %q = %q, want %q", page, got, want)
		}
	}
}

func TestRuntimeOwnedHandlerDoesNotCreateHostLifecycleManager(t *testing.T) {
	handler, cancel, err := NewRuntimeOwnedHandler(context.Background(), t.TempDir(), "controller", "token", "fingerprint", controller.NewCoordinator(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK && response.Code != http.StatusFound {
		t.Fatalf("runtime-owned dashboard status = %d", response.Code)
	}
}

func TestRuntimeOwnedRegistrationUsesCredimiWithoutLifecycleManager(t *testing.T) {
	var paths []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/mobile-runner" || r.URL.Path == "/api/mobile-device" || r.URL.Path == "/api/mobile-device/reconcile" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()

	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	s.cfg.values["CREDIMI_URL"] = api.URL
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	s.cfg.values["CREDIMI_DEVICE_COUNT"] = "1"
	s.cfg.values["CREDIMI_DEVICE_1_ID"] = "acme/runner/phone"
	s.cfg.values["CREDIMI_DEVICE_1_NAME"] = "Phone"
	s.cfg.values["CREDIMI_DEVICE_1_TYPE"] = "android_phone"
	s.cfg.values["CREDIMI_DEVICE_1_MODE"] = "usb"
	s.cfg.values["CREDIMI_DEVICE_1_SERIAL"] = "usb-1"
	socket := filepath.Join(filepath.Dir(s.cfg.Path()), "control.sock")
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileSetup: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	s.launcherSocket = socket

	s.startRuntimeOwnedRegistration(s.cfg.Snapshot())
	operation := s.operations.Current()
	if operation.ID == "" {
		t.Fatal("runtime-owned registration did not create an operation")
	}
	completed, err := s.operations.Wait(context.Background(), operation.ID)
	if err != nil || completed.Phase != controller.PhaseSucceeded {
		t.Fatalf("runtime-owned registration = %#v err=%v", completed, err)
	}
	if err := s.applyRuntimeOwnedRegistration(s.cfg.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(paths, "/api/mobile-runner") || !slices.Contains(paths, "/api/mobile-device/reconcile") {
		t.Fatalf("Credimi registration paths = %v", paths)
	}
}

func TestRuntimeOwnedConfigDelegatesTopologyChangesToLauncher(t *testing.T) {
	s := newTestServer(t)
	s.runtimeOwned = true
	socket := filepath.Join(t.TempDir(), "control.sock")
	reconciled := make(chan struct{}, 1)
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileConfig: func(context.Context) error {
			reconciled <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	s.launcherSocket = socket
	if err := s.applyRuntimeOwnedConfig(dashboardruntime.ConfigDiff{
		ChangedKeys: []string{"RUNNER_PORT"},
		Classes:     []dashboardruntime.ApplyClass{dashboardruntime.ApplyComposeRecreate},
	}, s.cfg.Snapshot()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("launcher did not receive reconcile-config")
	}
}

func TestRuntimeOwnedConfigOperationWaitsForLauncherAndClearsHandoff(t *testing.T) {
	s := newTestServer(t)
	s.runtimeOwned = true
	configDir := filepath.Dir(s.cfg.Path())
	if err := os.WriteFile(filepath.Join(configDir, "runtime-state"), []byte("stopped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(configDir, "control.sock")
	reconciled := make(chan struct{}, 1)
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileConfig: func(context.Context) error {
			reconciled <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	s.launcherSocket = socket

	err = s.applyRuntimeOwnedConfigInOperation(context.Background(), dashboardruntime.ConfigDiff{
		ChangedKeys: []string{"RUNNER_PORT"},
		Classes:     []dashboardruntime.ApplyClass{dashboardruntime.ApplyComposeRecreate},
	}, s.cfg.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("launcher did not receive reconcile-config")
	}
	if fileExists(configOperationPath(configDir)) {
		t.Fatal("completed config handoff was not cleared")
	}
	if got := s.startupSnapshot().Phase; got != StartupReady {
		t.Fatalf("startup state = %q, want %q", got, StartupReady)
	}
}

func TestRuntimeOwnedSetupRecoversLauncherOperationAfterReplacement(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()

	submitter := newTestServer(t)
	submitter.cfg.values["CREDIMI_URL"] = api.URL
	submitter.cfg.values["CREDIMI_DEVICE_COUNT"] = "1"
	submitter.cfg.values["CREDIMI_DEVICE_1_ID"] = "acme/runner/phone"
	submitter.cfg.values["CREDIMI_DEVICE_1_NAME"] = "Phone"
	submitter.cfg.values["CREDIMI_DEVICE_1_TYPE"] = "android_phone"
	submitter.cfg.values["CREDIMI_DEVICE_1_MODE"] = "usb"
	submitter.cfg.values["CREDIMI_DEVICE_1_SERIAL"] = "usb-1"
	submitter.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	submitter.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	configDir := filepath.Dir(submitter.cfg.Path())
	if err := os.WriteFile(filepath.Join(configDir, "setup-pending"), []byte("pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	socket := filepath.Join(configDir, "control.sock")
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileSetup: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	_, err = launcher.RequestSetupReconcileAsync(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if !fileExists(setupOperationPath(configDir)) {
		t.Fatal("launcher did not persist setup operation handoff")
	}

	replacement := newTestServer(t)
	replacement.cfg.path = submitter.cfg.Path()
	replacement.cfg.values = cloneStringMap(submitter.cfg.Snapshot())
	replacement.runtimeOwned = true
	replacement.launcherSocket = socket
	replacement.startExistingRuntimeJob(replacement.cfg.Snapshot())
	waitForCondition(t, func() bool {
		snapshot := replacement.startupSnapshot()
		return snapshot.Phase == StartupWaitingRunner && fileExists(setupOperationPath(configDir))
	})
	close(release)
	waitForCondition(t, func() bool {
		snapshot := replacement.startupSnapshot()
		return snapshot.Phase == StartupReady && !snapshot.running
	})
	if fileExists(setupOperationPath(configDir)) || fileExists(filepath.Join(configDir, "setup-pending")) {
		t.Fatal("terminal setup operation state was not cleared")
	}
}

func TestRuntimeOwnedSetupSubmitterCancellationPreservesHandoff(t *testing.T) {
	s := newTestServer(t)
	configDir := filepath.Dir(s.cfg.Path())
	socket := filepath.Join(configDir, "control.sock")
	started := make(chan struct{})
	release := make(chan struct{})
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileSetup: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	s.launcherSocket = socket
	handle, err := launcher.RequestSetupReconcileAsync(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.finishSetupRegistration(ctx, s.cfg.Snapshot(), handle.ID, false); err != nil {
		t.Fatalf("cancelled setup submitter = %v", err)
	}
	if got, err := os.ReadFile(setupOperationPath(configDir)); err != nil || strings.TrimSpace(string(got)) != handle.ID {
		t.Fatalf("setup operation handoff after submitter cancellation = %q, %v", got, err)
	}
	close(release)
}

func TestRuntimeOwnedSetupSurfacesLauncherFailureAfterReplacement(t *testing.T) {
	submitter := newTestServer(t)
	configDir := filepath.Dir(submitter.cfg.Path())
	if err := os.WriteFile(filepath.Join(configDir, "setup-pending"), []byte("pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(configDir, "control.sock")
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileSetup: func(context.Context) error {
			return errors.New("docker compose failed: network-scoped aliases are only supported for user-defined networks")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	_, err = launcher.RequestSetupReconcileAsync(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	replacement := newTestServer(t)
	replacement.cfg.path = submitter.cfg.Path()
	replacement.cfg.values = cloneStringMap(submitter.cfg.Snapshot())
	replacement.runtimeOwned = true
	replacement.launcherSocket = socket
	replacement.startExistingRuntimeJob(replacement.cfg.Snapshot())
	waitForCondition(t, func() bool {
		snapshot := replacement.startupSnapshot()
		return snapshot.Phase == StartupNeedsAttention && !snapshot.running
	})
	snapshot := replacement.startupSnapshot()
	if !strings.Contains(snapshot.Message, "network-scoped aliases") {
		t.Fatalf("launcher failure was not surfaced: %q", snapshot.Message)
	}
	if fileExists(setupOperationPath(configDir)) {
		t.Fatal("failed setup operation state was not cleared")
	}
}

func TestRuntimeOwnedSetupMissingOperationDoesNotRemainRunning(t *testing.T) {
	submitter := newTestServer(t)
	configDir := filepath.Dir(submitter.cfg.Path())
	if err := os.WriteFile(filepath.Join(configDir, "setup-pending"), []byte("pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := newTestServer(t)
	replacement.cfg.path = submitter.cfg.Path()
	replacement.cfg.values = cloneStringMap(submitter.cfg.Snapshot())
	replacement.runtimeOwned = true
	replacement.launcherSocket = filepath.Join(t.TempDir(), "missing.sock")
	replacement.startExistingRuntimeJob(replacement.cfg.Snapshot())
	waitForCondition(t, func() bool {
		snapshot := replacement.startupSnapshot()
		return snapshot.Phase == StartupNeedsAttention && !snapshot.running
	})
	if !strings.Contains(replacement.startupSnapshot().Message, "operation is unavailable") {
		t.Fatalf("missing operation was not reported: %q", replacement.startupSnapshot().Message)
	}
}

func TestRuntimeOwnedSetupStaleOperationIsTerminallyReported(t *testing.T) {
	s := newTestServer(t)
	configDir := filepath.Dir(s.cfg.Path())
	if err := os.WriteFile(filepath.Join(configDir, "setup-pending"), []byte("pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(configDir, "control.sock")
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileSetup: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := os.WriteFile(setupOperationPath(configDir), []byte("reconcile-config-stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.runtimeOwned = true
	s.launcherSocket = socket
	s.startExistingRuntimeJob(s.cfg.Snapshot())
	waitForCondition(t, func() bool {
		snapshot := s.startupSnapshot()
		return snapshot.Phase == StartupNeedsAttention && !snapshot.running
	})
	if !strings.Contains(s.startupSnapshot().Message, "operation not found") {
		t.Fatalf("stale operation was not reported: %q", s.startupSnapshot().Message)
	}
	if fileExists(setupOperationPath(configDir)) {
		t.Fatal("stale setup operation was not cleared")
	}
}

func TestRuntimeOwnedConfigRecoveryCompletesWithoutStartingStoppedRuntime(t *testing.T) {
	s := newTestServer(t)
	s.runtimeOwned = true
	configDir := filepath.Dir(s.cfg.Path())
	if err := os.WriteFile(filepath.Join(configDir, "runtime-state"), []byte("stopped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(configDir, "control.sock")
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileConfig: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	s.launcherSocket = socket
	if err := markReconcilePending(configDir); err != nil {
		t.Fatal(err)
	}
	handle, err := launcher.RequestReconcileAsync(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.finishConfigReconcileRecovery(context.Background(), s.cfg.Snapshot(), handle.ID, true); err != nil {
		t.Fatal(err)
	}
	if fileExists(configOperationPath(configDir)) {
		t.Fatal("config operation handoff was not cleared")
	}
	if !fileExists(reconcilePendingPath(configDir)) {
		t.Fatal("stopped recovery cleared pending reconciliation")
	}
	if got := readExecutionState(configDir); got != executionStateStopped {
		t.Fatalf("runtime state changed during stopped recovery: %q", got)
	}
	if s.startupSnapshot().Phase != StartupReady {
		t.Fatalf("startup state = %#v", s.startupSnapshot())
	}
}

func TestRuntimeOwnedConfigRecoveryRegistersRunningRuntime(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	s := newTestServer(t)
	s.runtimeOwned = true
	s.cfg.values["CREDIMI_URL"] = api.URL
	s.cfg.values["CREDIMI_DEVICE_COUNT"] = ""
	configDir := filepath.Dir(s.cfg.Path())
	if err := os.WriteFile(filepath.Join(configDir, "runtime-state"), []byte("running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(configDir, "control.sock")
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileConfig: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	s.launcherSocket = socket
	handle, err := launcher.RequestReconcileAsync(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.finishConfigReconcileRecovery(context.Background(), s.cfg.Snapshot(), handle.ID, true); err != nil {
		t.Fatal(err)
	}
	if fileExists(configOperationPath(configDir)) || s.startupSnapshot().Phase != StartupReady {
		t.Fatalf("running config recovery state = %#v", s.startupSnapshot())
	}
}

func TestRuntimeOwnedConfigRecoverySurfacesAndConsumesLauncherFailure(t *testing.T) {
	s := newTestServer(t)
	s.runtimeOwned = true
	configDir := filepath.Dir(s.cfg.Path())
	if err := os.WriteFile(filepath.Join(configDir, "runtime-state"), []byte("stopped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(configDir, "control.sock")
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileConfig: func(context.Context) error { return errors.New("docker compose failed") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	s.launcherSocket = socket
	handle, err := launcher.RequestReconcileAsync(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	err = s.finishConfigReconcileRecovery(context.Background(), s.cfg.Snapshot(), handle.ID, true)
	if err == nil || !strings.Contains(err.Error(), "docker compose failed") {
		t.Fatalf("config recovery failure = %v", err)
	}
	if fileExists(configOperationPath(configDir)) || s.startupSnapshot().Phase != StartupNeedsAttention {
		t.Fatalf("failed config recovery state = %#v", s.startupSnapshot())
	}
}

func TestConfigOperationStateRejectsMissingAndEmptyReferences(t *testing.T) {
	dir := t.TempDir()
	if _, err := readConfigOperation(dir); err == nil {
		t.Fatal("missing config operation was accepted")
	}
	path := configOperationPath(dir)
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfigOperation(dir); err == nil {
		t.Fatal("empty config operation was accepted")
	}
	if err := clearConfigOperation(dir); err != nil {
		t.Fatal(err)
	}
	if err := clearConfigOperation(dir); err != nil {
		t.Fatal(err)
	}
}

func TestSetupRuntimeHandoffWaitsForConfirmedRuntimeState(t *testing.T) {
	s := newTestServer(t)
	s.runtimeControlFile = filepath.Join(t.TempDir(), "runtime-control")
	started := make(chan struct{})
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			raw, err := os.ReadFile(s.runtimeControlFile)
			if err == nil && string(raw) == "setup-ready\n" {
				close(started)
				_ = os.WriteFile(filepath.Join(filepath.Dir(s.cfg.Path()), "runtime-state"), []byte("running\n"), 0o600)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if err := s.startSetupRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("setup runtime handoff was not observed")
	}
}

func TestSetupRuntimeHandoffSurfacesRuntimeFailure(t *testing.T) {
	s := newTestServer(t)
	s.runtimeControlFile = filepath.Join(t.TempDir(), "runtime-control")
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(s.runtimeControlFile); err == nil {
				_ = os.WriteFile(filepath.Join(filepath.Dir(s.cfg.Path()), "runtime-state"), []byte("failed: workers failed\n"), 0o600)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if err := s.startSetupRuntime(context.Background()); err == nil || !strings.Contains(err.Error(), "workers failed") {
		t.Fatalf("runtime failure = %v", err)
	}
}

func TestWriteSetupRuntimeControlIsPrivateAndActionable(t *testing.T) {
	dir := t.TempDir()
	if err := writeSetupRuntimeControl(""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runtime-control")
	if err := writeSetupRuntimeControl(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("setup runtime control mode = %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "setup-ready\n" {
		t.Fatalf("setup runtime control content = %q, err = %v", raw, err)
	}
	if err := writeSetupRuntimeControl(filepath.Join(dir, "missing", "runtime-control")); err == nil || !strings.Contains(err.Error(), "write setup runtime control") {
		t.Fatalf("setup runtime control write error = %v", err)
	}
}

func TestWriteRuntimeReadyControlSupportsNormalRegistration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-control")
	if err := writeRuntimeReadyControl(path, "registration-ready"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "registration-ready\n" {
		t.Fatalf("registration runtime control = %q, err = %v", raw, err)
	}
	if err := writeRuntimeReadyControl(path, "start"); err == nil || !strings.Contains(err.Error(), "unsupported runtime ready action") {
		t.Fatalf("invalid runtime ready action = %v", err)
	}
}

func TestProvisionRuntimeCapabilitiesWithoutAndroidTargets(t *testing.T) {
	s := newTestServer(t)
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal.example:7233"
	cfg.Storage.StateDir = t.TempDir()
	if err := runnerconfig.WriteFile(s.cfg.Path(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.provisionRuntimeCapabilities(context.Background()); err != nil {
		t.Fatalf("iOS-only runtime capability provisioning failed: %v", err)
	}
}

func TestRuntimeOperationalUsesSingleStateMarker(t *testing.T) {
	dir := t.TempDir()
	if !runtimeOperational(dir) {
		t.Fatal("missing runtime state should be operational for first start")
	}
	for _, state := range []string{"running", "starting"} {
		if err := os.WriteFile(filepath.Join(dir, "runtime-state"), []byte(state+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !runtimeOperational(dir) {
			t.Fatalf("state %q reported non-operational", state)
		}
	}
	for _, state := range []string{"stopped", "paused", "failed: device missing"} {
		if err := os.WriteFile(filepath.Join(dir, "runtime-state"), []byte(state+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if runtimeOperational(dir) {
			t.Fatalf("state %q reported operational", state)
		}
	}
}

func TestSetupRuntimeHandoffReportsControlWriteFailure(t *testing.T) {
	s := newTestServer(t)
	s.runtimeControlFile = filepath.Join(t.TempDir(), "missing", "runtime-control")
	if err := s.startSetupRuntime(context.Background()); err == nil || !strings.Contains(err.Error(), "write setup runtime control") {
		t.Fatalf("setup runtime control failure = %v", err)
	}
}

func TestRuntimeOwnedSetupReportsLauncherRequestFailure(t *testing.T) {
	s := newTestServer(t)
	s.runtimeOwned = true
	s.launcherSocket = filepath.Join(t.TempDir(), "missing.sock")
	s.startRuntimeOwnedRegistration(s.cfg.Snapshot())
	waitForCondition(t, func() bool {
		snapshot := s.startupSnapshot()
		return snapshot.Phase == StartupNeedsAttention && !snapshot.running
	})
	if !strings.Contains(s.startupSnapshot().Message, "connect to runner launcher") {
		t.Fatalf("launcher request failure was not surfaced: %q", s.startupSnapshot().Message)
	}
}

func TestWaitForLauncherOperationHonorsCancellation(t *testing.T) {
	s := newTestServer(t)
	socket := filepath.Join(filepath.Dir(s.cfg.Path()), "control.sock")
	started := make(chan struct{})
	finished := make(chan struct{})
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileConfig: func(context.Context) error {
			close(started)
			<-finished
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	s.launcherSocket = socket
	handle, err := launcher.RequestReconcileAsync(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.waitForLauncherOperation(ctx, handle.ID) }()
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("canceled launcher wait = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled launcher wait did not finish")
	}
	close(finished)
}

func TestRuntimeOwnedSetupClearsOperationAfterRegistrationFailure(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.Error(w, "registration failed", http.StatusBadGateway)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	s := newTestServer(t)
	s.cfg.values["CREDIMI_URL"] = api.URL
	s.cfg.values["CREDIMI_DEVICE_COUNT"] = "1"
	s.cfg.values["CREDIMI_DEVICE_1_ID"] = "acme/runner/phone"
	s.cfg.values["CREDIMI_DEVICE_1_NAME"] = "Phone"
	s.cfg.values["CREDIMI_DEVICE_1_TYPE"] = "android_phone"
	s.cfg.values["CREDIMI_DEVICE_1_MODE"] = "usb"
	s.cfg.values["CREDIMI_DEVICE_1_SERIAL"] = "usb-1"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	s.cfg.values["RUNNER_PUBLIC_URL"] = "https://runner.example"
	socket := filepath.Join(filepath.Dir(s.cfg.Path()), "control.sock")
	control, err := launcher.ServeWithOperations(socket, func(context.Context) error { return nil }, nil, launcher.Operations{
		ReconcileSetup: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	s.launcherSocket = socket
	handle, err := launcher.RequestSetupReconcileAsync(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	err = s.finishSetupRegistration(context.Background(), s.cfg.Snapshot(), handle.ID, true)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("registration failure = %v", err)
	}
	if fileExists(setupOperationPath(filepath.Dir(s.cfg.Path()))) {
		t.Fatal("registration failure left setup operation state behind")
	}
}

func TestSetupOperationStateIsMinimalAndTerminallyCleared(t *testing.T) {
	dir := t.TempDir()
	if _, err := readSetupOperation(dir); err == nil {
		t.Fatal("missing setup operation state was accepted")
	}
	path := setupOperationPath(dir)
	if err := os.WriteFile(path, []byte("reconcile-config-7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	operationID, err := readSetupOperation(dir)
	if err != nil || operationID != "reconcile-config-7" {
		t.Fatalf("setup operation ID=%q err=%v", operationID, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("setup operation mode=%o", info.Mode().Perm())
	}
	if err := clearSetupOperation(dir); err != nil || fileExists(path) {
		t.Fatalf("clear setup operation err=%v exists=%t", err, fileExists(path))
	}
	if err := clearSetupOperation(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSetupOperation(dir); err == nil {
		t.Fatal("empty setup operation state was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearSetupOperation(dir); err == nil {
		t.Fatal("directory setup operation state was silently cleared")
	}
	if err := os.Remove(filepath.Join(path, "marker")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "setup-pending"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "setup-pending", "marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearSetupPending(dir); err == nil {
		t.Fatal("directory setup pending state was silently cleared")
	}
	if err := os.Remove(filepath.Join(dir, "setup-pending", "marker")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "setup-pending")); err != nil {
		t.Fatal(err)
	}
	if err := clearSetupPending(dir); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeOwnedLifecycleReadsLauncherQuickTunnelState(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	s.launcherSocket = filepath.Join(t.TempDir(), "control.sock")
	configDir := filepath.Dir(s.cfg.Path())
	if err := launcher.WriteQuickTunnelURL(configDir, "https://current.trycloudflare.com"); err != nil {
		t.Fatal(err)
	}
	url, err := s.runtimeLifecycle(s.cfg.Snapshot()).QuickTunnelURL(context.Background())
	if err != nil || url != "https://current.trycloudflare.com" {
		t.Fatalf("launcher quick tunnel state = %q, %v", url, err)
	}
}

type nativeRuntimeControlFake struct {
	url        string
	prepared   int
	stopped    int
	execution  int
	reconciled int
	err        error
	prepareErr error
}

func (f *nativeRuntimeControlFake) Prepare(context.Context) error { f.prepared++; return f.prepareErr }
func (f *nativeRuntimeControlFake) Reconcile(context.Context, bool) error {
	f.reconciled++
	return f.err
}
func (f *nativeRuntimeControlFake) StartExecution(context.Context) error { f.execution++; return nil }
func (f *nativeRuntimeControlFake) Stop(context.Context) error           { f.stopped++; return nil }
func (f *nativeRuntimeControlFake) CurrentPublicURL(context.Context) (string, error) {
	return f.url, nil
}
func (f *nativeRuntimeControlFake) VerifyPublicURL(context.Context, string) error { return nil }
func (f *nativeRuntimeControlFake) Status(context.Context) dashboardruntime.RuntimeStatus {
	return dashboardruntime.RuntimeStatus{Configured: true, RunnerRunning: true, PublicURL: f.url}
}

func TestRuntimeOwnedLifecycleUsesInjectedNativeRuntime(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	s.launcherSocket = ""
	s.nativeRuntime = &nativeRuntimeControlFake{url: "https://native.trycloudflare.com"}
	lifecycle := s.runtimeLifecycle(s.cfg.Snapshot())
	url, err := lifecycle.QuickTunnelURL(context.Background())
	if err != nil || url != "https://native.trycloudflare.com" {
		t.Fatalf("native quick tunnel URL = %q, %v", url, err)
	}
	if lifecycle.VerifyPublicURL == nil {
		t.Fatal("native runtime did not provide endpoint verification")
	}
}

func TestNativeRuntimeOwnedHandlerUsesSupervisorStatusAndStop(t *testing.T) {
	dir := t.TempDir()
	writeDashboardTestConfig(t, dir, "token")
	native := &nativeRuntimeControlFake{url: "https://native.trycloudflare.com"}
	handler, cancel, err := NewRuntimeOwnedHandlerWithNativeRuntime(context.Background(), dir, "controller", "identity", "fingerprint", controller.NewCoordinator(context.Background()), native)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/controller/status?token=token", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "native.trycloudflare.com") {
		t.Fatalf("native controller status = %d %s", recorder.Code, recorder.Body.String())
	}
	server := newTestServer(t)
	server.manager = nil
	server.runtimeOwned = true
	server.nativeRuntime = native
	snapshot, err := server.submitRuntimeAction("stop")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := server.operations.Wait(context.Background(), snapshot.ID)
	if err != nil || completed.Phase != controller.PhaseSucceeded || native.stopped != 1 {
		t.Fatalf("native stop = %#v stopped=%d err=%v", completed, native.stopped, err)
	}
}

func TestNativeStoppedConfigReconcileBuildsCurrentGenerationWithoutExecution(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	native := &nativeRuntimeControlFake{}
	s.nativeRuntime = native
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.cfg.Path()), "runtime-state"), []byte("stopped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff := dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired}}
	if err := s.applyRuntimeOwnedConfigInOperation(context.Background(), diff, s.cfg.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if native.reconciled != 1 || native.execution != 0 {
		t.Fatalf("stopped native reconcile = reconciled %d execution %d", native.reconciled, native.execution)
	}
	if got := s.runtimeOwnedPublicURL(); got != "" {
		t.Fatalf("manual native public URL = %q, want empty", got)
	}
	if err := s.applyRuntimeOwnedConfig(diff, s.cfg.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if native.reconciled != 2 || native.execution != 0 {
		t.Fatalf("asynchronous stopped native reconcile = reconciled %d execution %d", native.reconciled, native.execution)
	}
}

func TestRuntimeOwnedNativeAutoPublicURLUsesCurrentGeneration(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	s.cfg.mu.Lock()
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "auto"
	s.cfg.mu.Unlock()
	s.nativeRuntime = &nativeRuntimeControlFake{url: "https://current.trycloudflare.com"}
	if got := s.runtimeOwnedPublicURL(); got != "https://current.trycloudflare.com" {
		t.Fatalf("native current public URL = %q", got)
	}
	data := s.pageData("overview", nil)
	if !data.Data.(map[string]any)["NativeRuntimeControlAvailable"].(bool) || data.RuntimeStatus().PublicURL != "https://current.trycloudflare.com" {
		t.Fatal("page data did not expose the current native runtime generation")
	}
}

func TestNativeRuntimeActionReportsSupervisorReconcileFailure(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	s.nativeRuntime = &nativeRuntimeControlFake{err: errors.New("native generation unavailable")}
	snapshot, err := s.submitRuntimeAction("restart")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := s.operations.Wait(context.Background(), snapshot.ID)
	if err != nil || completed.Phase != controller.PhaseFailed || !strings.Contains(completed.Error, "native generation unavailable") {
		t.Fatalf("native restart failure = %#v err=%v", completed, err)
	}
}

func TestNativeRuntimeStartStopsBeforeRegistrationWhenPreparationFails(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	native := &nativeRuntimeControlFake{prepareErr: errors.New("listener bind failed")}
	s.nativeRuntime = native
	snapshot, err := s.submitRuntimeAction("start")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := s.operations.Wait(context.Background(), snapshot.ID)
	if err != nil || completed.Phase != controller.PhaseFailed || !strings.Contains(completed.Error, "listener bind failed") {
		t.Fatalf("native start failure = %#v err=%v", completed, err)
	}
	if native.prepared != 1 || native.execution != 0 {
		t.Fatalf("failed native preparation execution=%d prepared=%d", native.execution, native.prepared)
	}
}

func TestNativeRuntimeStartPreparesButNeverExecutesWhenRegistrationFails(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	native := &nativeRuntimeControlFake{}
	s.nativeRuntime = native
	s.cfg.mu.Lock()
	s.cfg.values["CREDIMI_USER_API_KEY"] = ""
	s.cfg.values["CREDIMI_INTERNAL_ADMIN_KEY"] = ""
	s.cfg.mu.Unlock()
	snapshot, err := s.submitRuntimeAction("start")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := s.operations.Wait(context.Background(), snapshot.ID)
	if err != nil || completed.Phase != controller.PhaseFailed || !strings.Contains(completed.Error, "missing Credimi API key") {
		t.Fatalf("native start registration failure = %#v err=%v", completed, err)
	}
	if native.prepared != 1 || native.execution != 0 {
		t.Fatalf("registration failure execution=%d prepared=%d", native.execution, native.prepared)
	}
}

func TestNativeRuntimeStartRegistersBeforeStartingExecution(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	native := &nativeRuntimeControlFake{}
	s.nativeRuntime = native
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = originalClient })
	snapshot, err := s.submitRuntimeAction("start")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := s.operations.Wait(context.Background(), snapshot.ID)
	if err != nil || completed.Phase != controller.PhaseSucceeded {
		t.Fatalf("native start = %#v err=%v", completed, err)
	}
	if native.prepared != 1 || native.execution != 1 {
		t.Fatalf("native start ordering prepared=%d execution=%d", native.prepared, native.execution)
	}
}

func TestNativeRuntimeRestartReplacesThenRegistersBeforeExecution(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	native := &nativeRuntimeControlFake{}
	s.nativeRuntime = native
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = originalClient })
	snapshot, err := s.submitRuntimeAction("restart")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := s.operations.Wait(context.Background(), snapshot.ID)
	if err != nil || completed.Phase != controller.PhaseSucceeded {
		t.Fatalf("native restart = %#v err=%v", completed, err)
	}
	if native.stopped != 1 || native.reconciled != 1 || native.execution != 1 {
		t.Fatalf("native restart stopped=%d reconciled=%d execution=%d", native.stopped, native.reconciled, native.execution)
	}
}

func TestExistingNativeRuntimePreparesBeforeRegistrationAndExecution(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	native := &nativeRuntimeControlFake{}
	s.nativeRuntime = native
	configDir := filepath.Dir(s.cfg.Path())
	if err := os.WriteFile(filepath.Join(configDir, "runtime-state"), []byte("running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = originalClient })
	s.startExistingRuntimeJob(s.cfg.Snapshot())
	s.mu.RLock()
	done := s.startup.done
	s.mu.RUnlock()
	if done == nil {
		t.Fatal("existing runtime registration was not started")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("existing native runtime registration did not complete")
	}
	if native.prepared != 1 || native.execution != 1 {
		t.Fatalf("existing native startup prepared=%d execution=%d", native.prepared, native.execution)
	}
}

func TestRunningNativeConfigReconcileDoesNotStartWorkersBeforeRegistration(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	native := &nativeRuntimeControlFake{}
	s.nativeRuntime = native
	s.cfg.mu.Lock()
	s.cfg.values["CREDIMI_USER_API_KEY"] = ""
	s.cfg.values["CREDIMI_INTERNAL_ADMIN_KEY"] = ""
	s.cfg.mu.Unlock()
	diff := dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired}}
	err := s.applyRuntimeOwnedConfigInOperation(context.Background(), diff, s.cfg.Snapshot())
	if err == nil || !strings.Contains(err.Error(), "missing Credimi API key") {
		t.Fatalf("running native reconcile error = %v", err)
	}
	if native.reconciled != 1 || native.execution != 0 {
		t.Fatalf("registration failure started execution: reconciled=%d execution=%d", native.reconciled, native.execution)
	}
}

func TestRunningNativeConfigReconcileRegistersBeforeExecution(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	native := &nativeRuntimeControlFake{}
	s.nativeRuntime = native
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.cfg.Path()), "runtime-state"), []byte("running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = originalClient })
	diff := dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired}}
	if err := s.applyRuntimeOwnedConfigInOperation(context.Background(), diff, s.cfg.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if native.reconciled != 1 || native.execution != 1 {
		t.Fatalf("running native reconcile = reconciled %d execution %d", native.reconciled, native.execution)
	}
}

func TestRunningNativeBackgroundConfigReconcileAlsoWaitsForRegistration(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	native := &nativeRuntimeControlFake{}
	s.nativeRuntime = native
	s.cfg.mu.Lock()
	s.cfg.values["CREDIMI_USER_API_KEY"] = ""
	s.cfg.values["CREDIMI_INTERNAL_ADMIN_KEY"] = ""
	s.cfg.mu.Unlock()
	diff := dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired}}
	err := s.applyRuntimeOwnedConfig(diff, s.cfg.Snapshot())
	if err == nil || !strings.Contains(err.Error(), "missing Credimi API key") {
		t.Fatalf("background native reconcile error = %v", err)
	}
	if native.reconciled != 1 || native.execution != 0 {
		t.Fatalf("background registration failure started execution: reconciled=%d execution=%d", native.reconciled, native.execution)
	}
}

func TestRunningNativeBackgroundConfigReconcileStartsExecutionAfterRegistration(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	native := &nativeRuntimeControlFake{}
	s.nativeRuntime = native
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.cfg.Path()), "runtime-state"), []byte("running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = originalClient })
	diff := dashboardruntime.ConfigDiff{Classes: []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired}}
	if err := s.applyRuntimeOwnedConfig(diff, s.cfg.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if native.reconciled != 1 || native.execution != 1 {
		t.Fatalf("background native reconcile = reconciled %d execution %d", native.reconciled, native.execution)
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
	writeDashboardTestConfig(t, dir, "")
	manager := &fakeManager{startBlock: make(chan struct{}), startStarted: make(chan struct{})}
	defer close(manager.startBlock)
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
	select {
	case <-manager.startStarted:
	case <-time.After(time.Second):
		t.Fatal("runtime start operation did not begin")
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
	writeDashboardTestConfig(t, dir, "secret-token")
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
	requestContext, cancelRequest := context.WithCancel(req.Context())
	req = req.WithContext(requestContext)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveDevicesConfig(rec, req)
	cancelRequest()
	waitForQueuedOperation(t, s, rec)
	updatedStore, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		t.Fatal(err)
	}
	config, err := updatedStore.RuntimeConfig()
	if err != nil || len(config.Devices) != 1 || config.Devices[0].ID != "acme/runner/pixel" || config.Devices[0].Serial != "usb-1" {
		t.Fatalf("saved config = %#v, %v", config, err)
	}
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
	s.cfg.values["ANDROID_RUNNER_IMAGE"] = "runner:local"

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
	if !strings.Contains(rec.Body.String(), `"confirm_required":false`) {
		t.Fatalf("obsolete runner type field should be ignored: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "runner record in Credimi") {
		t.Fatalf("obsolete runner type field should not require Credimi update: %s", rec.Body.String())
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

func TestServerConfigDiffRejectsDirectRunnerIDChange(t *testing.T) {
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
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("configDiff direct ID change = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CREDIMI_RUNNER_ID cannot be changed after runner setup") {
		t.Fatalf("direct runner ID edit should be rejected clearly: %s", rec.Body.String())
	}
}

func TestServerConfigDiffRejectsNameChange(t *testing.T) {
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
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("configDiff name change = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CREDIMI_RUNNER_NAME cannot be changed after runner setup") {
		t.Fatalf("name change should be rejected clearly: %s", rec.Body.String())
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
	if !strings.Contains(rec.Body.String(), "CREDIMI_RUNNER_ORGANIZATION cannot be changed after runner setup") {
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

func TestSetupConfigMutationSurfacesWriteFailure(t *testing.T) {
	s := newTestServer(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/setup", nil)
	s.queueConfigMutation(recorder, request, "setup", func(context.Context, *http.Request, func(string)) error {
		return errors.New("write config failed")
	})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("setup mutation response = %d", recorder.Code)
	}
	operation, err := s.operations.Wait(context.Background(), s.operations.Current().ID)
	if err != nil || operation.Phase != controller.PhaseFailed {
		t.Fatalf("setup mutation operation = %#v, %v", operation, err)
	}
	startup := s.startupSnapshot()
	if startup.Phase != StartupNeedsAttention || !strings.Contains(startup.Message, "write config failed") {
		t.Fatalf("startup failure = %#v", startup)
	}
}

func TestReconcilePendingMarkerSurvivesUntilCleared(t *testing.T) {
	dir := t.TempDir()
	if err := markReconcilePending(dir); err != nil {
		t.Fatal(err)
	}
	if !fileExists(reconcilePendingPath(dir)) {
		t.Fatal("reconciliation pending marker was not persisted")
	}
	if err := clearReconcilePending(dir); err != nil {
		t.Fatal(err)
	}
	if fileExists(reconcilePendingPath(dir)) {
		t.Fatal("reconciliation pending marker was not cleared")
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
	s.manager.(*fakeManager).status.RunnerRunning = true
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
	waitForQueuedOperation(t, s, rec)
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
	manager.quickTunnelURL = "https://fresh.example.trycloudflare.com"
	s.cfg.values["CREDIMI_URL"] = api.URL
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
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
	s.cfg.values["ANDROID_RUNNER_IMAGE"] = "local:latest"
	s.cfg.values["ANDROID_PULL_POLICY"] = "never"
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

func TestRuntimeOwnedDashboardDelegatesImageUpgradeToLauncher(t *testing.T) {
	started := make(chan struct{})
	socket := filepath.Join(t.TempDir(), "control.sock")
	control, err := launcher.Serve(socket, func(context.Context) error {
		close(started)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	s := newTestServer(t)
	s.manager = nil
	s.launcherSocket = socket
	recorder := httptest.NewRecorder()
	s.controllerUpgradeImage(recorder, httptest.NewRequest(http.MethodPost, "/api/controller/maintenance/upgrade-image", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("delegated image upgrade = %d %s", recorder.Code, recorder.Body.String())
	}
	operation := s.operations.Current()
	if completed, err := s.operations.Wait(context.Background(), operation.ID); err != nil || completed.Phase != controller.PhaseSucceeded {
		t.Fatalf("delegated upgrade operation = %#v err=%v", completed, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("outer launcher did not receive delegated upgrade")
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
		"CREDIMI_SERVICE_MODE": "manual",
		"CREDIMI_RUNNER_TYPE":  "ios_simulator",
		"RUNNER_HOST":          host,
		"RUNNER_PORT":          port,
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
		quickTunnelURL: "https://new.example.trycloudflare.com",
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

func TestApplySavedConfigDefersWhenRuntimeStopped(t *testing.T) {
	s := newTestServer(t)
	s.manager = &fakeManager{status: dashboardruntime.RuntimeStatus{RunnerRunning: false, ComposeRunning: false}}
	outcome, err := s.applySavedConfig(context.Background(), dashboardruntime.ConfigDiff{
		ChangedKeys: []string{"RUNNER_PORT"},
		Classes:     []dashboardruntime.ApplyClass{dashboardruntime.ApplyRestartRequired},
	}, map[string]string{"CREDIMI_RUNNER_ID": "acme/runner"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Applied || !outcome.Deferred {
		t.Fatalf("stopped runtime outcome = %#v", outcome)
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
	waitForQueuedOperation(t, s, rec)
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
	waitForQueuedOperation(t, s, rec)
	if fm.stopCalls != 1 || fm.startCalls != 1 {
		t.Fatalf("saveConfig lifecycle calls stop=%d start=%d", fm.stopCalls, fm.startCalls)
	}
}

func TestServerSaveConfigRejectsEstablishedIdentityChange(t *testing.T) {
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
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("saveConfig identity change = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CREDIMI_RUNNER_ID cannot be changed after runner setup") {
		t.Fatalf("saveConfig identity change should be rejected clearly: %s", rec.Body.String())
	}
	if s.cfg.Get("CREDIMI_RUNNER_ID") != "acme/runner" || s.cfg.Get("CREDIMI_RUNNER_NAME") != "runner" {
		t.Fatal("rejected identity change modified the stored configuration")
	}
	if fm.stopCalls != 0 || fm.startCalls != 0 {
		t.Fatalf("rejected identity change triggered lifecycle calls stop=%d start=%d", fm.stopCalls, fm.startCalls)
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

func candidateProvisionValues() dashboardruntime.Values {
	return dashboardruntime.Values{
		"CREDIMI_URL":                 "https://credimi.example",
		"CREDIMI_USER_API_KEY":        "user-key",
		"CREDIMI_RUNNER_ID":           "acme/runner",
		"CREDIMI_RUNNER_NAME":         "runner",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
		"TEMPORAL_ADDRESS":            "temporal.example:7233",
		"CREDIMI_DEVICE_COUNT":        "1",
		"CREDIMI_DEVICE_1_ID":         "acme/runner/device",
		"CREDIMI_DEVICE_1_NAME":       "Device",
		"CREDIMI_DEVICE_1_TYPE":       "android_phone",
		"CREDIMI_DEVICE_1_MODE":       "no_device",
		"CREDIMI_DEVICE_1_ENABLED":    "true",
	}
}

func TestConfigApplyOperationOwnsProvisioningContext(t *testing.T) {
	original := ensureCandidateEmulatorReady
	defer func() { ensureCandidateEmulatorReady = original }()
	s := newTestServer(t)
	started := make(chan struct{})
	release := make(chan struct{})
	ensureCandidateEmulatorReady = func(ctx context.Context, _ runnerconfig.Config, _ string, _ androidtools.EmulatorProgress) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader("x=y"))
	req = req.WithContext(requestContext)
	response := httptest.NewRecorder()
	s.queueConfigMutation(response, req, "config", func(ctx context.Context, _ *http.Request, progress func(string)) error {
		return provisionCandidateCapabilities(ctx, candidateProvisionValues(), progress)
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("config operation response = %d", response.Code)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		failed, _ := s.operations.Wait(context.Background(), s.operations.Current().ID)
		t.Fatalf("candidate provisioning did not start: %#v", failed)
	}
	cancelRequest()
	close(release)
	completed := waitForQueuedOperation(t, s, response)
	if completed.Phase != controller.PhaseSucceeded {
		t.Fatalf("request cancellation cancelled accepted operation: %#v", completed)
	}
}

func TestConfigApplyOperationCancelsWithCoordinator(t *testing.T) {
	original := ensureCandidateEmulatorReady
	defer func() { ensureCandidateEmulatorReady = original }()
	s := newTestServer(t)
	started := make(chan struct{})
	ensureCandidateEmulatorReady = func(ctx context.Context, _ runnerconfig.Config, _ string, _ androidtools.EmulatorProgress) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	parent, cancel := context.WithCancel(context.Background())
	s.ctx = parent
	s.operations = controller.NewCoordinator(parent)
	response := httptest.NewRecorder()
	s.queueConfigMutation(response, httptest.NewRequest(http.MethodPost, "/config", nil), "config", func(ctx context.Context, _ *http.Request, progress func(string)) error {
		return provisionCandidateCapabilities(ctx, candidateProvisionValues(), progress)
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("config operation response = %d", response.Code)
	}
	<-started
	cancel()
	completed, err := s.operations.Wait(context.Background(), s.operations.Current().ID)
	if err != nil || completed.Phase != controller.PhaseCancelled {
		t.Fatalf("coordinator cancellation = %#v err=%v", completed, err)
	}
}

func TestConfigApplyOperationRejectsStaleCandidate(t *testing.T) {
	original := beforeCandidateCommit
	defer func() { beforeCandidateCommit = original }()
	s := newTestServer(t)
	started := make(chan struct{})
	release := make(chan struct{})
	beforeCandidateCommit = func() {
		close(started)
		<-release
	}
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "key"
	s.cfg.values["CREDIMI_DEVICE_COUNT"] = "1"
	s.cfg.values["CREDIMI_DEVICE_1_ID"] = "acme/runner/device"
	s.cfg.values["CREDIMI_DEVICE_1_NAME"] = "Device"
	s.cfg.values["CREDIMI_DEVICE_1_TYPE"] = "android_phone"
	s.cfg.values["CREDIMI_DEVICE_1_MODE"] = "no_device"
	s.cfg.values["CREDIMI_DEVICE_1_ENABLED"] = "true"
	typed, err := dashboardruntime.TypedConfigFromValues(dashboardruntime.Values(s.cfg.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	if err := runnerconfig.WriteFile(s.cfg.Path(), typed); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader("CREDIMI_RUNNER_DESCRIPTION=candidate"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveConfig(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("config operation response = %d", response.Code)
	}
	<-started
	persisted, err := runnerconfig.LoadFile(s.cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	persisted.Runner.Description = "newer"
	if err := runnerconfig.WriteFile(s.cfg.Path(), persisted); err != nil {
		t.Fatal(err)
	}
	close(release)
	completed, err := s.operations.Wait(context.Background(), s.operations.Current().ID)
	if err != nil || completed.Phase != controller.PhaseFailed || !strings.Contains(completed.Error, "configuration changed") {
		t.Fatalf("stale candidate result = %#v err=%v", completed, err)
	}
}

func TestProvisionCandidateCapabilitiesUsesDeviceDelta(t *testing.T) {
	original := ensureCandidateEmulatorReady
	defer func() { ensureCandidateEmulatorReady = original }()
	calls := 0
	ensureCandidateEmulatorReady = func(context.Context, runnerconfig.Config, string, androidtools.EmulatorProgress) error {
		calls++
		return nil
	}
	old := candidateProvisionValues()
	old["CREDIMI_DEVICE_1_TYPE"] = "android_emulator"
	old["CREDIMI_DEVICE_1_MODE"] = "emulator"
	old["CREDIMI_DEVICE_1_BASE_NAME"] = "credimi"
	old["CREDIMI_DEVICE_1_GOLDEN_PATH"] = "/avd-golden/credimi-golden"
	unchanged := dashboardruntime.Values(old)
	unchanged["CREDIMI_RUNNER_DESCRIPTION"] = "updated"
	if err := provisionCandidateCapabilitiesForChange(context.Background(), old, unchanged, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("unchanged emulator was provisioned %d times", calls)
	}
	added := dashboardruntime.Values(candidateProvisionValues())
	added["CREDIMI_DEVICE_1_TYPE"] = "android_emulator"
	added["CREDIMI_DEVICE_1_MODE"] = "emulator"
	added["CREDIMI_DEVICE_1_BASE_NAME"] = "credimi"
	added["CREDIMI_DEVICE_1_GOLDEN_PATH"] = "/avd-golden/credimi-golden"
	if err := provisionCandidateCapabilitiesForChange(context.Background(), candidateProvisionValues(), added, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("added emulator provision calls = %d, want 1", calls)
	}
}

func TestConfigApplyOperationConflictsWithoutBlockingStatusReads(t *testing.T) {
	original := ensureCandidateEmulatorReady
	defer func() { ensureCandidateEmulatorReady = original }()
	s := newTestServer(t)
	started := make(chan struct{})
	release := make(chan struct{})
	ensureCandidateEmulatorReady = func(_ context.Context, _ runnerconfig.Config, _ string, _ androidtools.EmulatorProgress) error {
		close(started)
		<-release
		return nil
	}
	action := func(ctx context.Context, _ *http.Request, progress func(string)) error {
		return provisionCandidateCapabilities(ctx, candidateProvisionValues(), progress)
	}
	first := httptest.NewRecorder()
	s.queueConfigMutation(first, httptest.NewRequest(http.MethodPost, "/config", nil), "config", action)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first config operation response = %d", first.Code)
	}
	<-started
	second := httptest.NewRecorder()
	s.queueConfigMutation(second, httptest.NewRequest(http.MethodPost, "/config", nil), "config", action)
	if second.Code != http.StatusConflict {
		t.Fatalf("second config operation response = %d", second.Code)
	}
	status := httptest.NewRecorder()
	s.systemMetrics(status, httptest.NewRequest(http.MethodGet, "/api/system-metrics", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status read during provisioning = %d", status.Code)
	}
	close(release)
	waitForQueuedOperation(t, s, first)
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

func TestServerSaveDevicesConfigUpdatesRunningRunnerWithoutRestart(t *testing.T) {
	transport := http.DefaultTransport
	var registrations []string
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		registrations = append(registrations, req.URL.Path)
		switch req.URL.Path {
		case "/api/mobile-device/preview-id":
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_id":"acme/runner/second"}`))}, nil
		case "/api/mobile-runner", "/api/mobile-device", "/api/mobile-device/reconcile":
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		default:
			return nil, errors.New("unexpected Credimi path: " + req.URL.Path)
		}
	})
	t.Cleanup(func() { http.DefaultTransport = transport })

	s := newTestServer(t)
	fm := &fakeManager{status: dashboardruntime.RuntimeStatus{RunnerRunning: true}}
	s.manager = fm
	store, err := dashboardruntime.LoadStore(filepath.Dir(s.cfg.Path()))
	if err != nil {
		t.Fatal(err)
	}
	host := dashboardruntime.Values{
		"CREDIMI_URL":                 "https://credimi.example",
		"CREDIMI_USER_API_KEY":        "user-key",
		"CREDIMI_RUNNER_ID":           "acme/runner",
		"CREDIMI_RUNNER_NAME":         "runner",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
		"CREDIMI_SERVICE_MODE":        "manual",
		"RUNNER_PUBLIC_URL":           "https://runner.example",
		"TEMPORAL_ADDRESS":            "temporal.example:7233",
		"RUNNER_HOST":                 "127.0.0.1",
		"RUNNER_PORT":                 "8050",
		"DASHBOARD_HOST":              "127.0.0.1",
		"DASHBOARD_PORT":              "8051",
		"ANDROID_RUNNER_IMAGE":        "credimi-runner:local",
		"ANDROID_PULL_POLICY":         "never",
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{Host: host, Devices: []dashboardruntime.DeviceRuntimeConfig{{
		ID: "acme/runner/first", Name: "First", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "usb-1", Values: dashboardruntime.Values{"SERIAL": "usb-1"},
	}}}); err != nil {
		t.Fatal(err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)

	form := url.Values{"name": {"Second"}, "type": {"android_phone"}, "mode": {"usb"}, "serial": {"usb-2"}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.saveDevicesConfig(recorder, request)
	waitForQueuedOperation(t, s, recorder)
	if fm.stopCalls != 0 || fm.startCalls != 0 {
		t.Fatalf("device add unexpectedly restarted runner stop=%d start=%d", fm.stopCalls, fm.startCalls)
	}
	if got := strings.Join(registrations, ","); strings.Count(got, "/api/mobile-device") < 3 || !strings.Contains(got, "/api/mobile-runner") || !strings.Contains(got, "/api/mobile-device/reconcile") {
		t.Fatalf("Credimi registrations = %s", got)
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

func TestServerSystemMetricsReturnsEmptySnapshotWithoutMonitor(t *testing.T) {
	s := newTestServer(t)
	s.systemMonitor = nil
	recorder := httptest.NewRecorder()
	s.systemMetrics(recorder, httptest.NewRequest(http.MethodGet, "/api/system-metrics", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"samples":[]`) || !strings.Contains(recorder.Body.String(), `"interval_ms":2000`) {
		t.Fatalf("empty system metrics = %d %s", recorder.Code, recorder.Body.String())
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
	s.manager.(*fakeManager).quickTunnelURL = "https://new-url.trycloudflare.com"

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

	s.manager.(*fakeManager).quickTunnelURL = "https://replacement-url.trycloudflare.com"
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

func TestRuntimeOwnedStartRegistersCurrentQuickTunnelURL(t *testing.T) {
	s := newTestServer(t)
	configDir := filepath.Dir(s.cfg.Path())
	s.runtimeOwned = true
	s.manager = nil
	s.runnerReady = func(context.Context, map[string]string) error { return nil }
	s.cfg.values["CREDIMI_RUNNER_ID"] = "acme/runner"
	s.cfg.values["CREDIMI_RUNNER_NAME"] = "runner"
	s.cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	s.cfg.values["CREDIMI_USER_API_KEY"] = "user-key"
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "auto"
	if err := launcher.WriteQuickTunnelURL(configDir, "https://restarted.trycloudflare.com"); err != nil {
		t.Fatal(err)
	}
	control, err := launcher.ServeWithOperations(filepath.Join(configDir, "control.sock"), func(context.Context) error { return nil }, nil, launcher.Operations{RuntimeStart: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	s.launcherSocket = filepath.Join(configDir, "control.sock")

	var registration dashboardruntime.RegisterRunnerRequest
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mobile-runner" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	s.cfg.values["CREDIMI_URL"] = api.URL

	snapshot, err := s.submitRuntimeAction("start")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.operations.Wait(context.Background(), snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if registration.IP != "https://restarted.trycloudflare.com" {
		t.Fatalf("restarted runner registration URL = %q", registration.IP)
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
	s.runtimeOwned = true
	s.launcherSocket = filepath.Join(t.TempDir(), "control.sock")
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

func TestRuntimeOwnedDashboardReloadsLauncherQuickTunnelURL(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	s.launcherSocket = filepath.Join(t.TempDir(), "control.sock")
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "auto"
	configDir := filepath.Dir(s.cfg.Path())
	if err := launcher.WriteQuickTunnelURL(configDir, "https://current.trycloudflare.com"); err != nil {
		t.Fatal(err)
	}

	data := s.pageData("overview", nil)
	runtimeStatus, ok := data.Data.(map[string]any)["RuntimeStatus"].(dashboardruntime.RuntimeStatus)
	if !ok {
		t.Fatalf("runtime status = %#v", data.Data)
	}
	if got, want := runtimeStatus.PublicURL, "https://current.trycloudflare.com"; got != want {
		t.Fatalf("public URL = %q, want %q", got, want)
	}
	if got := s.publicURL; got != "https://current.trycloudflare.com" {
		t.Fatalf("cached public URL = %q", got)
	}
}

func TestRuntimeOwnedDashboardRefreshesQuickTunnelURLAfterLauncherRestart(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	s.runtimeOwned = true
	s.launcherSocket = filepath.Join(t.TempDir(), "control.sock")
	s.cfg.values["CREDIMI_SERVICE_MODE"] = "auto"
	configDir := filepath.Dir(s.cfg.Path())
	s.publicURL = "https://old.trycloudflare.com"
	if err := launcher.WriteQuickTunnelURL(configDir, "https://new.trycloudflare.com"); err != nil {
		t.Fatal(err)
	}

	if got, want := s.runtimeOwnedPublicURL(), "https://new.trycloudflare.com"; got != want {
		t.Fatalf("public URL = %q, want %q", got, want)
	}
	if got, want := s.publicURL, "https://new.trycloudflare.com"; got != want {
		t.Fatalf("cached public URL = %q, want %q", got, want)
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
