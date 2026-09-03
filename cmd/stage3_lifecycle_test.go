package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/servicecoordination"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
)

type stage3Manager struct {
	mu           sync.Mutex
	status       servicemanager.Status
	restarts     int
	restart      func()
	restartErr   error
	logs         int
	logsExitOnce bool
	logStarted   chan struct{}
	logOnce      sync.Once
}

func (m *stage3Manager) Start(context.Context) error   { return nil }
func (m *stage3Manager) Stop(context.Context) error    { return nil }
func (m *stage3Manager) Enable(context.Context) error  { return nil }
func (m *stage3Manager) Disable(context.Context) error { return nil }
func (m *stage3Manager) Logs(ctx context.Context, _ servicemanager.LogOptions) error {
	m.mu.Lock()
	m.logs++
	call := m.logs
	m.mu.Unlock()
	m.logOnce.Do(func() {
		if m.logStarted != nil {
			close(m.logStarted)
		}
	})
	if m.logsExitOnce && call == 1 {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}
func (m *stage3Manager) Status(context.Context) (servicemanager.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status, nil
}
func (m *stage3Manager) Restart(context.Context) error {
	m.mu.Lock()
	m.restarts++
	if m.restartErr != nil {
		err := m.restartErr
		m.mu.Unlock()
		return err
	}
	restart := m.restart
	m.status.Running = true
	m.status.ServiceRestartRequired = false
	m.mu.Unlock()
	if restart != nil {
		restart()
	}
	return nil
}

func stage3Config(t *testing.T, dir string) runnerconfig.Config {
	t.Helper()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner.ID = "org/runner"
	cfg.Runner.Name = "runner"
	cfg.Runner.Organization = "org"
	cfg.Credimi.URL = "https://credimi.example"
	cfg.Credimi.UserAPIKey = "key"
	cfg.Temporal.Address = "temporal.example:7233"
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestApplyServiceRestartRequestWaitsForReplacementAndVerifiesFingerprint(t *testing.T) {
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner.ID = "org/runner"
	cfg.Runner.Name = "runner"
	cfg.Runner.Organization = "org"
	cfg.Credimi.URL = "https://credimi.example"
	cfg.Credimi.UserAPIKey = "key"
	cfg.Temporal.Address = "temporal.example:7233"
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}

	const identity = "replacement-identity"
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Credimi-Controller-Token") != identity {
			http.Error(w, "identity mismatch", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"controller_id":      "replacement",
			"config_fingerprint": "runtime-plan",
		})
	}))
	defer probe.Close()

	manager := &stage3Manager{status: servicemanager.Status{Running: false, ServiceRestartRequired: true}}
	manager.restart = func() {
		metadata := controller.Metadata{
			Schema: 1, ControllerID: "replacement", ConfigDir: dir, ListenPort: 8051,
			ProbeURL: probe.URL, PublicURL: probe.URL, ConfigFingerprint: "runtime-plan",
			IdentityToken: identity,
		}
		raw, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request, err := servicecoordination.NewRestartRequest(servicemanager.ServiceConfigFingerprint(cfg, true), nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}

	if err := applyServiceRestartRequest(context.Background(), manager, dir, request); err != nil {
		t.Fatal(err)
	}
	result, err := servicecoordination.ReadRestartResult(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.AppliedFingerprint != request.RequestedFingerprint {
		t.Fatalf("restart result=%+v", result)
	}
	if manager.restarts != 1 {
		t.Fatalf("restarts=%d, want 1", manager.restarts)
	}
}

func TestApplyServiceRestartRequestRejectsSupersededConfig(t *testing.T) {
	dir := t.TempDir()
	stage3Config(t, dir)
	request, err := servicecoordination.NewRestartRequest("obsolete-fingerprint", nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	manager := &stage3Manager{}
	if err := applyServiceRestartRequest(context.Background(), manager, dir, request); err != nil {
		t.Fatal(err)
	}
	result, err := servicecoordination.ReadRestartResult(dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || manager.restarts != 0 || result.RequestID != request.RequestID {
		t.Fatalf("result=%+v restarts=%d", result, manager.restarts)
	}
}

func TestApplyServiceRestartRequestRecordsRestartFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := stage3Config(t, dir)
	request, err := servicecoordination.NewRestartRequest(servicemanager.ServiceConfigFingerprint(cfg, true), nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	manager := &stage3Manager{restartErr: errors.New("restart unavailable")}
	if err := applyServiceRestartRequest(context.Background(), manager, dir, request); err == nil {
		t.Fatal("restart failure was not returned")
	}
	result, err := servicecoordination.ReadRestartResult(dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "restart unavailable") {
		t.Fatalf("restart result=%+v", result)
	}
}

func TestApplyServiceRestartRequestDoesNotRestartAlreadyAppliedService(t *testing.T) {
	dir := t.TempDir()
	cfg := stage3Config(t, dir)
	fingerprint := servicemanager.ServiceConfigFingerprint(cfg, true)
	request, err := servicecoordination.NewRestartRequest(fingerprint, nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	manager := &stage3Manager{status: servicemanager.Status{Running: true}}
	if err := applyServiceRestartRequest(context.Background(), manager, dir, request); err != nil {
		t.Fatal(err)
	}
	result, err := servicecoordination.ReadRestartResult(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.AppliedFingerprint != fingerprint || manager.restarts != 0 {
		t.Fatalf("result=%+v restarts=%d", result, manager.restarts)
	}
}

func TestApplyServiceRestartRequestRecordsConfigurationLoadFailure(t *testing.T) {
	dir := t.TempDir()
	request, err := servicecoordination.NewRestartRequest("fingerprint", nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	if err := applyServiceRestartRequest(context.Background(), &stage3Manager{}, dir, request); err == nil {
		t.Fatal("missing configuration was not returned")
	}
	result, err := servicecoordination.ReadRestartResult(dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "config.toml") {
		t.Fatalf("configuration failure result=%+v", result)
	}
}

func TestApplyServiceRestartRequestRecordsReplacementReadinessFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := stage3Config(t, dir)
	request, err := servicecoordination.NewRestartRequest(servicemanager.ServiceConfigFingerprint(cfg, true), nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := applyServiceRestartRequest(ctx, &stage3Manager{}, dir, request); err == nil {
		t.Fatal("canceled replacement was not returned")
	}
	result, err := servicecoordination.ReadRestartResult(dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "replacement service did not become ready") {
		t.Fatalf("readiness failure result=%+v", result)
	}
}

func TestAttachedHostHandlesRestartRequestOnce(t *testing.T) {
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner.ID = "org/runner"
	cfg.Runner.Name = "runner"
	cfg.Runner.Organization = "org"
	cfg.Credimi.URL = "https://credimi.example"
	cfg.Credimi.UserAPIKey = "key"
	cfg.Temporal.Address = "temporal.example:7233"
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"controller_id": "replacement", "config_fingerprint": "runtime-plan"})
	}))
	defer probe.Close()
	manager := &stage3Manager{
		status:     servicemanager.Status{ServiceRestartRequired: true},
		logStarted: make(chan struct{}),
	}
	manager.restart = func() {
		metadata := controller.Metadata{
			Schema: 1, ControllerID: "replacement", ConfigDir: dir, ListenPort: 8051,
			ProbeURL: probe.URL, PublicURL: probe.URL, ConfigFingerprint: "runtime-plan",
			IdentityToken: "replacement-identity",
		}
		raw, _ := json.Marshal(metadata)
		if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request, err := servicecoordination.NewRestartRequest(servicemanager.ServiceConfigFingerprint(cfg, true), nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if err := servicecoordination.WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- followAttachedService(ctx, manager, dir) }()
	select {
	case <-manager.logStarted:
	case <-time.After(time.Second):
		t.Fatal("attached log follower did not start")
	}
	deadline := time.After(3 * time.Second)
	for {
		manager.mu.Lock()
		restarts := manager.restarts
		manager.mu.Unlock()
		if restarts == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("attached host did not handle restart request")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	result, err := servicecoordination.ReadRestartResult(dir)
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAttachedHostResumesAfterLogStreamEnds(t *testing.T) {
	manager := &stage3Manager{logsExitOnce: true, logStarted: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- followAttachedService(ctx, manager, t.TempDir()) }()
	select {
	case <-manager.logStarted:
	case <-time.After(time.Second):
		t.Fatal("attached log follower did not start")
	}
	deadline := time.After(time.Second)
	for {
		manager.mu.Lock()
		logs := manager.logs
		manager.mu.Unlock()
		if logs >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("attached host did not resume log following")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func nowForTest() time.Time {
	return time.Now().UTC()
}
