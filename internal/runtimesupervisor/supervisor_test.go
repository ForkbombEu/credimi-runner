package runtimesupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/edge"
	"github.com/forkbombeu/credimi-runner/pkg/server"
)

type testAPI struct {
	mu                 sync.Mutex
	started, closed    bool
	startErr, closeErr error
	startHook          func()
	failures           chan error
	origin             string
}

func (a *testAPI) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.startErr != nil {
		return a.startErr
	}
	a.started = true
	if a.startHook != nil {
		a.startHook()
	}
	return nil
}
func (a *testAPI) Shutdown(context.Context) error {
	a.mu.Lock()
	a.closed = true
	err := a.closeErr
	a.mu.Unlock()
	return err
}
func (a *testAPI) Listening() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.started && !a.closed }
func (a *testAPI) LocalOrigin() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.origin == "" {
		return "http://127.0.0.1:8050", nil
	}
	return a.origin, nil
}
func (a *testAPI) Failures() <-chan error { return a.failures }

type testEdge struct {
	mu                          sync.Mutex
	starts, stops               int
	startErr, stopErr, closeErr error
	running                     bool
	startHook                   func()
	failures                    chan error
	origins                     []string
	startURLs                   []string
}

func (e *testEdge) Start(_ context.Context, origin string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts++
	e.origins = append(e.origins, origin)
	if e.startErr != nil {
		return "", e.startErr
	}
	e.running = true
	if e.startHook != nil {
		e.startHook()
	}
	if len(e.startURLs) > 0 {
		url := e.startURLs[0]
		e.startURLs = e.startURLs[1:]
		return url, nil
	}
	return "https://runner.example", nil
}
func (e *testEdge) Stop(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stops++
	if e.stopErr != nil {
		return e.stopErr
	}
	e.running = false
	return nil
}
func (e *testEdge) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closeErr
}
func (e *testEdge) Running() bool          { e.mu.Lock(); defer e.mu.Unlock(); return e.running }
func (e *testEdge) Failures() <-chan error { return e.failures }

type testWorkers struct {
	mu                   sync.Mutex
	starts, stops, waits int
	startErr, errorWait  error
	running              bool
	block                chan struct{}
	startHook            func()
}

func (w *testWorkers) Start(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.starts++
	if w.startErr != nil {
		return w.startErr
	}
	w.running = true
	hook := w.startHook
	if hook != nil {
		hook()
	}
	return nil
}
func (w *testWorkers) StopAll() { w.mu.Lock(); w.stops++; w.mu.Unlock() }
func (w *testWorkers) WaitAll(ctx context.Context) error {
	w.mu.Lock()
	w.waits++
	b := w.block
	err := w.errorWait
	w.mu.Unlock()
	if b != nil {
		select {
		case <-b:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	w.mu.Lock()
	w.running = false
	w.mu.Unlock()
	return err
}
func (w *testWorkers) Running() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.running }

type testLife struct {
	mu                               sync.Mutex
	pauses, resumes, beats, loops    int
	pauseErr, errorResume, errorBeat error
	resumeHook, heartbeatHook        func()
	loopHook                         func()
}

func (l *testLife) Pause(context.Context, string) error {
	l.mu.Lock()
	l.pauses++
	e := l.pauseErr
	l.mu.Unlock()
	return e
}
func (l *testLife) Resume(context.Context, string) error {
	l.mu.Lock()
	l.resumes++
	e := l.errorResume
	if l.resumeHook != nil {
		l.resumeHook()
	}
	l.mu.Unlock()
	return e
}
func (l *testLife) Heartbeat(context.Context) error {
	l.mu.Lock()
	l.beats++
	e := l.errorBeat
	if l.heartbeatHook != nil {
		l.heartbeatHook()
	}
	l.mu.Unlock()
	return e
}
func (l *testLife) StartHeartbeatLoop(context.Context) func() {
	l.mu.Lock()
	l.loops++
	if l.loopHook != nil {
		l.loopHook()
	}
	l.mu.Unlock()
	return func() {}
}
func validConfig() config.Config {
	c := config.Bootstrap()
	c.SchemaVersion = config.SchemaVersion
	c.Runner = config.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
	c.Credimi = config.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	c.Temporal.Address = "temporal:7233"
	c.Server.APIListen = "127.0.0.1:0"
	c.Server.DashboardListen = "127.0.0.1:8051"
	c.Server.ReadHeaderTimeout = config.Duration(time.Minute)
	c.Server.ShutdownTimeout = config.Duration(time.Minute)
	c.Storage.StateDir = "/tmp/runner"
	c.Storage.ArtifactRetention = config.Duration(time.Hour)
	c.Exposure = config.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example"}
	c.Android = config.AndroidConfig{RunnerImage: "runner", PullPolicy: "never", Network: "runner", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"}
	c.Storage.TempDir = "/tmp/runner"
	return c
}

func TestNewNormalizesPersistedActualWithoutGeneration(t *testing.T) {
	dir := t.TempDir()
	cfg := validConfig()
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "runtime-state.json")
	for _, actual := range []ActualState{ActualRunning, ActualStarting, ActualStopping} {
		t.Run(string(actual), func(t *testing.T) {
			if err := (StateStore{Path: statePath}).Save(PersistentState{Desired: DesiredRunning, Actual: actual}); err != nil {
				t.Fatal(err)
			}
			s, err := New(dir, func() (config.Config, error) { return cfg, nil }, Dependencies{})
			if err != nil {
				t.Fatal(err)
			}
			status := s.Status()
			if status.Desired != DesiredRunning || status.Actual != ActualStopped {
				t.Fatalf("status = %+v, want desired running and actual stopped", status)
			}
			if s.currentGeneration() != nil {
				t.Fatal("new Supervisor unexpectedly owns a generation")
			}
		})
	}
}
func newTestSupervisor(t *testing.T, life *testLife, edgeImpl *testEdge, workers *testWorkers) (*Supervisor, *testAPI) {
	t.Helper()
	api := &testAPI{}
	cfg := validConfig()
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil }, NewEdge: func(config.Config) (edge.Edge, error) { return edgeImpl, nil }, NewWorkers: func(config.Config, *server.ProcessStore) WorkerSet { return workers }, NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life }})
	if err != nil {
		t.Fatal(err)
	}
	return s, api
}

func TestSupervisorLifecycleAndPauseFailure(t *testing.T) {
	life := &testLife{pauseErr: errors.New("pause failed")}
	e := &testEdge{}
	w := &testWorkers{}
	s, api := newTestSupervisor(t, life, e, w)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !api.Listening() || s.Status().Desired != DesiredRunning {
		t.Fatal("not running")
	}
	if err := s.Stop(context.Background()); err == nil {
		t.Fatal("expected pause error")
	}
	if api.Listening() || e.Running() || s.Status().Desired == DesiredRunning {
		t.Fatal("resources still active")
	}
	if s.Status().Actual != ActualStopped {
		t.Fatalf("state=%+v", s.Status())
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorHeartbeatStatusTracksLoopLifetime(t *testing.T) {
	life := &testLife{}
	s, _ := newTestSupervisor(t, life, &testEdge{}, &testWorkers{})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.Status().HeartbeatRunning {
		t.Fatal("heartbeat loop not reported as running")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Status().HeartbeatRunning {
		t.Fatal("heartbeat loop still reported as running")
	}
}

func TestSupervisorAPIFailureTearsDownGeneration(t *testing.T) {
	api := &testAPI{failures: make(chan error, 1)}
	edgeImpl := &testEdge{failures: make(chan error, 1)}
	workers := &testWorkers{}
	life := &testLife{}
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := make(chan struct{})
	s.saveState = func(state PersistentState) error {
		if state.Actual == ActualFailed {
			select {
			case <-failed:
			default:
				close(failed)
			}
		}
		return nil
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.failures <- errors.New("execution API died")
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("API failure was not handled")
	}
	status := s.Status()
	if status.Desired != DesiredRunning || status.Actual != ActualFailed || s.currentGeneration() != nil {
		t.Fatalf("status=%+v generation=%v", status, s.currentGeneration())
	}
	if !strings.Contains(status.LastError, "execution API died") || workers.stops == 0 || workers.waits == 0 || edgeImpl.stops == 0 {
		t.Fatalf("failure cleanup status=%+v workers=%+v edge=%+v", status, workers, edgeImpl)
	}
}

func TestSupervisorEdgeFailureTearsDownGeneration(t *testing.T) {
	api := &testAPI{}
	edgeImpl := &testEdge{failures: make(chan error, 1)}
	workers := &testWorkers{}
	life := &testLife{}
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := make(chan struct{})
	s.saveState = func(state PersistentState) error {
		if state.Actual == ActualFailed {
			select {
			case <-failed:
			default:
				close(failed)
			}
		}
		return nil
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	edgeImpl.failures <- errors.New("cloudflared died")
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("edge failure was not handled")
	}
	status := s.Status()
	if status.Desired != DesiredRunning || status.Actual != ActualFailed || s.currentGeneration() != nil {
		t.Fatalf("status=%+v generation=%v", status, s.currentGeneration())
	}
	if !strings.Contains(status.LastError, "cloudflared died") {
		t.Fatalf("status=%+v", status)
	}
}

func TestSupervisorExpectedStopDoesNotBecomeFatal(t *testing.T) {
	api := &testAPI{failures: make(chan error, 1)}
	edgeImpl := &testEdge{failures: make(chan error, 1)}
	s, _ := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI:     func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewWorkers: func(config.Config, *server.ProcessStore) WorkerSet { return &testWorkers{} },
		NewEdge:    func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.failures <- errors.New("late API failure")
	edgeImpl.failures <- errors.New("late edge failure")
	if status := s.Status(); status.Desired != DesiredStopped || status.Actual != ActualStopped {
		t.Fatalf("status=%+v", status)
	}
}

func TestSupervisorIgnoresFailureFromReplacedGeneration(t *testing.T) {
	var apis []*testAPI
	var edges []*testEdge
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) {
			api := &testAPI{failures: make(chan error, 1)}
			apis = append(apis, api)
			return api, nil
		},
		NewEdge: func(config.Config) (edge.Edge, error) {
			edgeImpl := &testEdge{}
			edges = append(edges, edgeImpl)
			return edgeImpl, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := s.currentGeneration()
	if err := s.Reconcile(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}
	current := s.currentGeneration()
	if current == nil || current == old || len(apis) != 2 || len(edges) != 2 {
		t.Fatal("replacement generation was not installed")
	}
	old.fatal <- errors.New("stale generation failure")
	if status := s.Status(); status.Actual != ActualRunning || s.currentGeneration() != current {
		t.Fatalf("stale failure changed current runtime: status=%+v", status)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorStopClosesListenerAndStartReopensIt(t *testing.T) {
	var apis []*testAPI
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) {
			api := &testAPI{}
			apis = append(apis, api)
			return api, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(apis) != 1 || !apis[0].Listening() {
		t.Fatalf("initial listeners = %d, listening=%t", len(apis), len(apis) == 1 && apis[0].Listening())
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if apis[0].Listening() {
		t.Fatal("runner API listener remained open after stop")
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(apis) != 2 || !apis[1].Listening() {
		t.Fatalf("replacement listeners = %d, listening=%t", len(apis), len(apis) == 2 && apis[1].Listening())
	}
}

func TestNewSupervisorAliasLoadsState(t *testing.T) {
	dir := t.TempDir()
	cfg := validConfig()
	s, err := New(dir, func() (config.Config, error) { return cfg, nil }, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if s.ConfigDir() != dir {
		t.Fatalf("config dir=%q", s.ConfigDir())
	}
}

func TestSupervisorFailedEdgeShutdownRetainsGeneration(t *testing.T) {
	life := &testLife{}
	e := &testEdge{stopErr: errors.New("edge stop")}
	w := &testWorkers{}
	s, _ := newTestSupervisor(t, life, e, w)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err == nil {
		t.Fatal("expected edge error")
	}
	if s.currentGeneration() == nil || s.Status().Actual != ActualFailed {
		t.Fatal("generation/state lost")
	}
	e.mu.Lock()
	e.stopErr = nil
	e.mu.Unlock()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.currentGeneration() != nil {
		t.Fatal("generation retained after successful cleanup")
	}
}

func TestSupervisorReconcilePauseFailureDoesNotInstallReplacement(t *testing.T) {
	life := &testLife{}
	s, _ := newTestSupervisor(t, life, &testEdge{}, &testWorkers{})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	life.pauseErr = errors.New("pause unavailable")
	if err := s.Reconcile(context.Background(), validConfig()); err == nil || !strings.Contains(err.Error(), "pause unavailable") {
		t.Fatalf("reconcile error=%v", err)
	}
	if s.currentGeneration() != nil || s.Status().Actual != ActualFailed || s.Status().WorkersRunning {
		t.Fatalf("unexpected state=%+v", s.Status())
	}
}

func TestSupervisorRestartContinuesAfterRemotePauseFailure(t *testing.T) {
	life := &testLife{pauseErr: errors.New("remote pause")}
	s, _ := newTestSupervisor(t, life, &testEdge{}, &testWorkers{})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Status().Actual != ActualRunning || s.Status().Desired != DesiredRunning {
		t.Fatalf("restart state=%+v", s.Status())
	}
}

func TestSupervisorLocalCleanupErrorsRetainGeneration(t *testing.T) {
	life := &testLife{}
	edgeImpl := &testEdge{closeErr: errors.New("edge close")}
	api := &testAPI{closeErr: errors.New("api close")}
	workers := &testWorkers{}
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "edge close") || !strings.Contains(err.Error(), "api close") {
		t.Fatalf("cleanup error=%v", err)
	}
	if s.currentGeneration() == nil || s.Status().Actual != ActualFailed {
		t.Fatalf("generation was released after cleanup failure: %+v", s.Status())
	}
	api.closeErr = nil
	edgeImpl.closeErr = nil
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.currentGeneration() != nil || s.Status().Actual != ActualStopped {
		t.Fatalf("state after retry=%+v", s.Status())
	}
}

func TestSupervisorStartConfigurationFailures(t *testing.T) {
	loadErr := errors.New("config unavailable")
	s, err := New(t.TempDir(), func() (config.Config, error) { return config.Config{}, loadErr }, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, loadErr) {
		t.Fatalf("start error=%v", err)
	}
	if s.Status().Desired != DesiredRunning || s.Status().Actual != ActualFailed {
		t.Fatalf("failure state=%+v", s.Status())
	}
	bad := validConfig()
	bad.Runner.ID = "invalid"
	s, err = New(t.TempDir(), func() (config.Config, error) { return bad, nil }, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestSupervisorWorkerFailureRollsBack(t *testing.T) {
	life := &testLife{}
	e := &testEdge{}
	w := &testWorkers{startErr: errors.New("worker startup")}
	s, api := newTestSupervisor(t, life, e, w)
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("expected worker failure")
	}
	if api.Listening() || e.Running() {
		t.Fatal("activation resources not rolled back")
	}
	if life.resumes != 0 || life.beats != 0 || s.Status().Actual != ActualFailed {
		t.Fatal("activation advanced after failure")
	}
}

func TestSupervisorReconcileWaitsForWorkers(t *testing.T) {
	life := &testLife{}
	e := &testEdge{}
	w := &testWorkers{block: make(chan struct{})}
	s, _ := newTestSupervisor(t, life, e, w)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Reconcile(context.Background(), validConfig()) }()
	select {
	case err := <-done:
		t.Fatalf("reconcile completed early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if s.currentGeneration() == nil {
		t.Fatal("generation lost while worker blocked")
	}
	close(w.block)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconcile did not complete")
	}
}

func TestSupervisorRestartAndClosePreserveDesired(t *testing.T) {
	life := &testLife{}
	e := &testEdge{}
	w := &testWorkers{}
	s, _ := newTestSupervisor(t, life, e, w)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Status().Desired != DesiredRunning || s.Status().Actual != ActualRunning {
		t.Fatalf("restart state=%+v", s.Status())
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Status().Desired != DesiredRunning || s.Status().Actual != ActualStopped {
		t.Fatalf("close state=%+v", s.Status())
	}
}

func TestSupervisorStoppedReconcileDoesNotBuildGeneration(t *testing.T) {
	life := &testLife{}
	e := &testEdge{}
	w := &testWorkers{}
	s, _ := newTestSupervisor(t, life, e, w)
	if err := s.Reconcile(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}
	if s.Status().Desired == DesiredRunning || s.Status().Actual != ActualStopped {
		t.Fatalf("state=%+v", s.Status())
	}
	if s.currentGeneration() != nil || e.Running() || w.Running() {
		t.Fatal("stopped reconcile constructed resources")
	}
}

func TestSupervisorStoppedReconcileTearsDownExistingGenerationWithoutReplacement(t *testing.T) {
	life := &testLife{}
	e := &testEdge{}
	w := &testWorkers{}
	s, _ := newTestSupervisor(t, life, e, w)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := s.currentGeneration()
	if old == nil {
		t.Fatal("missing running generation")
	}
	if err := s.updateState(func(st *PersistentState) { st.Desired = DesiredStopped }); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}
	if s.currentGeneration() != nil || s.Status().Actual != ActualStopped {
		t.Fatalf("stopped reconcile state=%+v", s.Status())
	}
	if e.Running() || w.Running() || !old.localResourcesClosed() {
		t.Fatal("old generation was not fully torn down")
	}
}

func TestSupervisorActivationFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testLife, *testEdge, *testWorkers)
		want   string
	}{
		{"edge", func(_ *testLife, e *testEdge, _ *testWorkers) { e.startErr = errors.New("edge") }, "start edge"},
		{"resume", func(l *testLife, _ *testEdge, _ *testWorkers) { l.errorResume = errors.New("resume") }, "resume lifecycle"},
		{"heartbeat", func(l *testLife, _ *testEdge, _ *testWorkers) { l.errorBeat = errors.New("beat") }, "initial heartbeat"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := &testLife{}
			e := &testEdge{}
			w := &testWorkers{}
			tc.mutate(l, e, w)
			s, _ := newTestSupervisor(t, l, e, w)
			if err := s.Start(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
			if s.Status().Actual != ActualFailed {
				t.Fatalf("state=%+v", s.Status())
			}
		})
	}
}

func TestSupervisorStartStatePersistenceFailureAllocatesNoResources(t *testing.T) {
	life := &testLife{}
	edgeImpl := &testEdge{}
	workers := &testWorkers{}
	s, api := newTestSupervisor(t, life, edgeImpl, workers)
	persistErr := errors.New("state write failed")
	s.saveState = func(PersistentState) error { return persistErr }
	if err := s.Start(context.Background()); !errors.Is(err, persistErr) {
		t.Fatalf("error=%v", err)
	}
	if api.started || edgeImpl.starts != 0 || workers.starts != 0 || s.currentGeneration() != nil {
		t.Fatalf("resources allocated: api=%v edge=%d workers=%d generation=%v", api.started, edgeImpl.starts, workers.starts, s.currentGeneration())
	}
}

func TestSupervisorRestartStatePersistenceFailureAfterTeardownIsFailed(t *testing.T) {
	life := &testLife{}
	edgeImpl := &testEdge{}
	workers := &testWorkers{}
	s, api := newTestSupervisor(t, life, edgeImpl, workers)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("restart state write failed")
	s.saveState = func(state PersistentState) error {
		if state.Actual == ActualStarting {
			return persistErr
		}
		return nil
	}
	if err := s.Restart(context.Background()); !errors.Is(err, persistErr) {
		t.Fatalf("error=%v", err)
	}
	status := s.Status()
	if api.Listening() || edgeImpl.Running() || workers.Running() || s.currentGeneration() != nil {
		t.Fatal("old generation resources remain active")
	}
	if status.Desired != DesiredRunning || status.Actual != ActualFailed {
		t.Fatalf("status=%+v", status)
	}
	if status.Actual == ActualStopping || status.Actual == ActualStarting || !strings.Contains(status.LastError, persistErr.Error()) {
		t.Fatalf("untruthful restart failure status=%+v", status)
	}
}

func TestSupervisorStopStatePersistenceFailureStillCleansUp(t *testing.T) {
	life := &testLife{}
	edgeImpl := &testEdge{}
	workers := &testWorkers{}
	s, api := newTestSupervisor(t, life, edgeImpl, workers)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("stop state write failed")
	s.saveState = func(PersistentState) error { return persistErr }
	if err := s.Stop(context.Background()); !errors.Is(err, persistErr) {
		t.Fatalf("error=%v", err)
	}
	if api.Listening() || edgeImpl.Running() || workers.Running() || s.currentGeneration() != nil {
		t.Fatal("stop left local resources active")
	}
	if status := s.Status(); status.Desired != DesiredStopped || status.Actual != ActualStopped {
		t.Fatalf("in-memory status=%+v", status)
	}
}

func TestSupervisorRollbackJoinsStatePersistenceFailure(t *testing.T) {
	verifyErr := errors.New("endpoint verification failed")
	persistErr := errors.New("failed state write")
	s, _ := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) { return &testAPI{}, nil },
		VerifyPublicEndpoint: func(context.Context, config.Config, string) error {
			return verifyErr
		},
	})
	s.saveState = func(state PersistentState) error {
		if state.Actual == ActualFailed {
			return persistErr
		}
		return nil
	}
	err := s.Start(context.Background())
	if !errors.Is(err, verifyErr) || !errors.Is(err, persistErr) {
		t.Fatalf("error=%v", err)
	}
	if s.currentGeneration() != nil || s.Status().Actual != ActualFailed {
		t.Fatalf("rollback state=%+v generation=%v", s.Status(), s.currentGeneration())
	}
}

func TestSupervisorCloseStatePersistenceFailureCleansUp(t *testing.T) {
	life := &testLife{}
	edgeImpl := &testEdge{}
	workers := &testWorkers{}
	s, api := newTestSupervisor(t, life, edgeImpl, workers)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("close state write failed")
	s.saveState = func(PersistentState) error { return persistErr }
	if err := s.Close(context.Background()); !errors.Is(err, persistErr) {
		t.Fatalf("error=%v", err)
	}
	if api.Listening() || edgeImpl.Running() || workers.Running() || s.currentGeneration() != nil {
		t.Fatal("close left local resources active")
	}
	if status := s.Status(); status.Actual != ActualStopped || status.Desired != DesiredRunning {
		t.Fatalf("in-memory status=%+v", status)
	}
}

func TestSupervisorObservabilityAndRegistrationHooks(t *testing.T) {
	life := &testLife{}
	e := &testEdge{}
	w := &testWorkers{}
	var setup, shutdown, verify, register int
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		InitObservability: func(context.Context, config.Config) (func(context.Context) error, error) {
			setup++
			return func(context.Context) error { shutdown++; return nil }, nil
		},
		NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) { return &testAPI{}, nil }, NewEdge: func(config.Config) (edge.Edge, error) { return e, nil }, NewWorkers: func(config.Config, *server.ProcessStore) WorkerSet { return w }, NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
		VerifyPublicEndpoint: func(context.Context, config.Config, string) error { verify++; return nil }, Register: func(context.Context, config.Config, string) error { register++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if setup != 1 || verify != 1 || register != 1 {
		t.Fatalf("hooks=%d/%d/%d", setup, verify, register)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if shutdown != 1 {
		t.Fatalf("shutdown=%d", shutdown)
	}
}

func TestSupervisorRegistersBeforeWorkers(t *testing.T) {
	var events []string
	workers := &testWorkers{startHook: func() { events = append(events, "workers") }}
	api := &testAPI{}
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI:     func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewWorkers: func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		Register:   func(context.Context, config.Config, string) error { events = append(events, "register"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(events, ","), "register,workers"; got != want {
		t.Fatalf("activation order = %q, want %q", got, want)
	}
	_ = s.Stop(context.Background())
}

func TestSupervisorValidatesCapabilitiesBeforeActivationCompletes(t *testing.T) {
	var events []string
	api := &testAPI{startHook: func() { events = append(events, "api-start") }}
	edgeImpl := &testEdge{startHook: func() { events = append(events, "edge-start") }}
	workers := &testWorkers{startHook: func() { events = append(events, "workers-start") }}
	life := &testLife{
		resumeHook:    func() { events = append(events, "resume") },
		heartbeatHook: func() { events = append(events, "heartbeat") },
		loopHook:      func() { events = append(events, "heartbeat-loop") },
	}
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
		VerifyPublicEndpoint: func(context.Context, config.Config, string) error {
			events = append(events, "verify")
			return nil
		},
		ValidateRuntimeCapabilities: func(context.Context, config.Config) error {
			events = append(events, "runtime-capabilities")
			return nil
		},
		Register: func(context.Context, config.Config, string) error {
			events = append(events, "register")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "api-start,edge-start,verify,runtime-capabilities,register,workers-start,resume,heartbeat,heartbeat-loop"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("activation order = %q, want %q", got, want)
	}
	_ = s.Stop(context.Background())
}

func TestSupervisorCapabilityFailureRollsBackBeforeWorkers(t *testing.T) {
	api := &testAPI{}
	edgeImpl := &testEdge{}
	workers := &testWorkers{}
	life := &testLife{}
	capabilityErr := errors.New("/dev/kvm is required")
	validations := 0
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
		ValidateRuntimeCapabilities: func(context.Context, config.Config) error {
			validations++
			return capabilityErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Start(context.Background())
	if !errors.Is(err, capabilityErr) || !strings.Contains(err.Error(), "/dev/kvm") {
		t.Fatalf("start error = %v", err)
	}
	if validations != 1 {
		t.Fatalf("capability validations = %d", validations)
	}
	if workers.starts != 0 || life.resumes != 0 || life.beats != 0 || life.loops != 0 {
		t.Fatalf("activation continued: workers=%d resume=%d heartbeat=%d loop=%d", workers.starts, life.resumes, life.beats, life.loops)
	}
	status := s.Status()
	if status.Desired != DesiredRunning || status.Actual != ActualFailed || s.currentGeneration() != nil {
		t.Fatalf("status=%+v generation=%v", status, s.currentGeneration())
	}
	if !api.closed || edgeImpl.stops == 0 {
		t.Fatalf("rollback incomplete: api closed=%t edge stops=%d", api.closed, edgeImpl.stops)
	}
}

func TestSupervisorApplyInventoryKeepsGenerationRunning(t *testing.T) {
	api := &testAPI{}
	workers := &testWorkers{}
	life := &testLife{}
	registrations := 0
	cfg := validConfig()
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
		Register:           func(context.Context, config.Config, string) error { registrations++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	generation := s.currentGeneration()
	updated := cfg
	updated.Runner.Description = "updated"
	if err := s.ApplyInventory(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if s.currentGeneration() != generation || workers.stops != 0 || life.pauses != 0 || registrations != 2 {
		t.Fatalf("inventory apply disrupted generation: same=%t worker stops=%d pauses=%d registrations=%d", s.currentGeneration() == generation, workers.stops, life.pauses, registrations)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorApplyInventoryAdvancesActiveConfigOnlyAfterRegistration(t *testing.T) {
	oldDelay := registrationRetryDelay
	registrationRetryDelay = time.Millisecond
	t.Cleanup(func() { registrationRetryDelay = oldDelay })
	api := &testAPI{}
	workers := &testWorkers{}
	life := &testLife{}
	registrationErr := errors.New("Credimi unavailable")
	shouldFail := false
	var active *config.ActiveConfig
	cfg := validConfig()
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
		Register: func(_ context.Context, candidate config.Config, _ string) error {
			if active == nil {
				active = config.ActiveConfigOf(candidate)
			}
			if shouldFail {
				return registrationErr
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated := cfg
	updated.Runner.Description = "new desired description"
	shouldFail = true
	if err := s.ApplyInventory(context.Background(), updated); !errors.Is(err, registrationErr) {
		t.Fatalf("failed inventory apply error = %v", err)
	}
	if got := active.Load().Runner.Description; got != cfg.Runner.Description {
		t.Fatalf("failed apply advanced active config to %q", got)
	}
	shouldFail = false
	if err := s.ApplyInventory(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if got := active.Load().Runner.Description; got != updated.Runner.Description {
		t.Fatalf("successful apply active description = %q", got)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorReconcileRegistersTheNewEdgeURL(t *testing.T) {
	api := &testAPI{}
	edgeImpl := &testEdge{startURLs: []string{"https://tunnel-a.trycloudflare.com", "https://tunnel-b.trycloudflare.com"}}
	workers := &testWorkers{}
	life := &testLife{}
	var registered []string
	cfg := validConfig()
	cfg.Exposure.Mode = "manual"
	cfg.Exposure.PublicURL = "https://manual.example"
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
		Register: func(_ context.Context, cfg config.Config, publicURL string) error {
			endpoint, _, err := registrationEndpoint(cfg, publicURL)
			if err != nil {
				return err
			}
			registered = append(registered, endpoint)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	auto := cfg
	auto.Exposure.Mode = "quick_tunnel"
	auto.Exposure.PublicURL = "https://stale-manual.example"
	if err := s.Reconcile(context.Background(), auto); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 2 || registered[0] != "https://manual.example" || registered[1] != "https://tunnel-b.trycloudflare.com" {
		t.Fatalf("registered endpoints = %#v", registered)
	}
	if s.Status().PublicURL != "https://tunnel-b.trycloudflare.com" {
		t.Fatalf("active public URL = %q", s.Status().PublicURL)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorApplyEndpointVerifiesBeforeActivating(t *testing.T) {
	api := &testAPI{}
	edgeImpl := &testEdge{startURLs: []string{"https://old.example"}}
	workers := &testWorkers{}
	life := &testLife{}
	var events []string
	cfg := validConfig()
	cfg.Exposure.Mode = "manual"
	cfg.Exposure.PublicURL = "https://old.example"
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
		VerifyPublicEndpoint: func(_ context.Context, cfg config.Config, publicURL string) error {
			events = append(events, "verify:"+cfg.Exposure.PublicURL+":"+publicURL)
			return nil
		},
		Register: func(_ context.Context, cfg config.Config, _ string) error {
			events = append(events, "register:"+cfg.Exposure.PublicURL+":"+cfg.Exposure.PublicPort)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	events = nil
	updated := cfg
	updated.Exposure.PublicURL = "https://new.example"
	updated.Exposure.PublicPort = "9443"
	if err := s.ApplyEndpoint(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(events, "|"), "verify:https://new.example:https://new.example|register:https://new.example:9443"; got != want {
		t.Fatalf("apply events = %q, want %q", got, want)
	}
	if s.Status().PublicURL != "https://new.example" {
		t.Fatalf("active public URL = %q", s.Status().PublicURL)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorApplyEndpointFailureKeepsActiveEndpoint(t *testing.T) {
	oldDelay := registrationRetryDelay
	registrationRetryDelay = time.Millisecond
	t.Cleanup(func() { registrationRetryDelay = oldDelay })
	api := &testAPI{}
	edgeImpl := &testEdge{startURLs: []string{"https://old.example"}}
	workers := &testWorkers{}
	life := &testLife{}
	verifyErr := errors.New("new endpoint is not ready")
	registerErr := errors.New("Credimi registration failed")
	verifyFails := true
	cfg := validConfig()
	cfg.Exposure.Mode = "manual"
	cfg.Exposure.PublicURL = "https://old.example"
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
		VerifyPublicEndpoint: func(_ context.Context, cfg config.Config, _ string) error {
			if verifyFails && cfg.Exposure.PublicURL == "https://new.example" {
				return verifyErr
			}
			return nil
		},
		Register: func(_ context.Context, cfg config.Config, _ string) error {
			if cfg.Exposure.PublicURL == "https://new.example" {
				return registerErr
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated := cfg
	updated.Exposure.PublicURL = "https://new.example"
	if err := s.ApplyEndpoint(context.Background(), updated); !errors.Is(err, verifyErr) {
		t.Fatalf("apply error = %v", err)
	}
	if got := s.Status().PublicURL; got != "https://old.example" {
		t.Fatalf("failed apply changed active public URL to %q", got)
	}
	verifyFails = false
	if err := s.ApplyEndpoint(context.Background(), updated); !errors.Is(err, registerErr) {
		t.Fatalf("registration failure = %v", err)
	}
	if got := s.Status().PublicURL; got != "https://old.example" {
		t.Fatalf("registration failure changed active public URL to %q", got)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationAndPublicEndpointVerification(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/readyz" {
			_, _ = w.Write([]byte(`{"runner_id":"org/runner"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	cfg := validConfig()
	cfg.Credimi.URL = server.URL
	cfg.Exposure.PublicURL = server.URL
	if err := Register(context.Background(), cfg, server.URL); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPublicEndpoint(context.Background(), cfg, server.URL); err != nil {
		t.Fatal(err)
	}
	if len(paths) < 2 || paths[0] != "/api/mobile-runner" || paths[len(paths)-1] != "/readyz" {
		t.Fatalf("registration paths = %#v", paths)
	}
	for _, mode := range []string{"manual", "named_tunnel", "quick_tunnel"} {
		candidate := cfg
		candidate.Exposure.Mode = mode
		if mode == "named_tunnel" {
			candidate.Exposure.Domain = "runner.example"
		}
		endpoint, _, err := registrationEndpoint(candidate, server.URL)
		if err != nil || endpoint == "" {
			t.Fatalf("registration endpoint mode %q = %q, %v", mode, endpoint, err)
		}
	}
	if err := Register(context.Background(), config.Config{Credimi: config.CredimiConfig{URL: server.URL}}, server.URL); err == nil {
		t.Fatal("registration without API key succeeded")
	}
	for _, tc := range []struct {
		name string
		cfg  config.Config
		url  string
	}{
		{"manual URL", config.Config{Exposure: config.ExposureConfig{Mode: "manual"}}, ""},
		{"managed domain", config.Config{Exposure: config.ExposureConfig{Mode: "named_tunnel"}}, ""},
		{"quick URL", config.Config{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := registrationEndpoint(tc.cfg, tc.url); err == nil {
				t.Fatal("missing endpoint data was accepted")
			}
		})
	}
	wrong := cfg
	wrong.Runner.ID = "other/runner"
	if err := VerifyPublicEndpoint(context.Background(), wrong, server.URL); err == nil || !strings.Contains(err.Error(), "belongs to runner") {
		t.Fatalf("wrong runner endpoint error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := VerifyPublicEndpoint(canceled, cfg, "http://[::1"); err == nil {
		t.Fatal("invalid endpoint unexpectedly verified")
	}
}

func TestRegistrationUsesCurrentExposureEndpoint(t *testing.T) {
	var requests []dashboardruntime.RegisterRunnerRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/mobile-runner" {
			var request dashboardruntime.RegisterRunnerRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	for _, tc := range []struct {
		name, mode, persistedURL, persistedPort, domain, activeURL, wantIP, wantPort string
	}{
		{"manual to auto", "quick_tunnel", "https://old-manual.example", "9443", "", "https://new.trycloudflare.com", "https://new.trycloudflare.com", ""},
		{"auto to manual", "manual", "https://manual-new.example", "9443", "", "https://old.trycloudflare.com", "https://manual-new.example", "9443"},
		{"auto to named", "named_tunnel", "", "", "runner.example", "https://old.trycloudflare.com", "https://runner.example", ""},
		{"named to auto", "quick_tunnel", "", "", "old.example", "https://new.trycloudflare.com", "https://new.trycloudflare.com", ""},
		{"manual URL change", "manual", "https://manual-b.example", "443", "", "https://ignored.example", "https://manual-b.example", "443"},
		{"manual port change", "manual", "https://manual.example", "9443", "", "https://ignored.example", "https://manual.example", "9443"},
		{"named domain change", "named_tunnel", "", "", "runner-b.example", "https://old.trycloudflare.com", "https://runner-b.example", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Credimi.URL = server.URL
			cfg.Exposure.Mode = tc.mode
			cfg.Exposure.PublicURL = tc.persistedURL
			cfg.Exposure.PublicPort = tc.persistedPort
			cfg.Exposure.Domain = tc.domain
			if err := Register(context.Background(), cfg, tc.activeURL); err != nil {
				t.Fatal(err)
			}
			if len(requests) == 0 {
				t.Fatal("registration request was not sent")
			}
			got := requests[len(requests)-1]
			if got.IP != tc.wantIP || got.Port != tc.wantPort {
				t.Fatalf("registered endpoint = %q:%q, want %q:%q", got.IP, got.Port, tc.wantIP, tc.wantPort)
			}
		})
	}
}

func TestPublicEndpointVerificationURLUsesManualPortAndBasePath(t *testing.T) {
	for _, tc := range []struct {
		name, mode, publicURL, publicPort, want string
	}{
		{"manual IPv4", "manual", "http://192.0.2.10", "8050", "http://192.0.2.10:8050/readyz"},
		{"manual DNS base path", "manual", "https://runner.example/base/", "8050", "https://runner.example:8050/base/readyz"},
		{"manual explicit port wins", "manual", "http://runner.example:9000", "8050", "http://runner.example:9000/readyz"},
		{"manual IPv6", "manual", "http://[2001:db8::10]", "8050", "http://[2001:db8::10]:8050/readyz"},
		{"quick tunnel ignores public port", "quick_tunnel", "https://quick.example/base", "8050", "https://quick.example/base/readyz"},
		{"malformed URL", "manual", "http://[::1", "8050", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Exposure: config.ExposureConfig{Mode: tc.mode, PublicPort: tc.publicPort}}
			if tc.name == "malformed URL" {
				cfg.Exposure.PublicURL = tc.publicURL
				if _, err := publicEndpointVerificationURL(cfg, tc.publicURL); err == nil {
					t.Fatal("malformed public URL unexpectedly succeeded")
				}
				return
			}
			got, err := publicEndpointVerificationURL(cfg, tc.publicURL)
			if err != nil || got != tc.want {
				t.Fatalf("publicEndpointVerificationURL()=%q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestVerifyPublicEndpointUsesManualPublicPort(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"runner_id":"org/runner"}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := validConfig()
	cfg.Exposure.PublicURL = "http://" + parsed.Hostname()
	cfg.Exposure.PublicPort = parsed.Port()
	if err := VerifyPublicEndpoint(context.Background(), cfg, cfg.Exposure.PublicURL); err != nil {
		t.Fatal(err)
	}
	if requestedPath != "/readyz" {
		t.Fatalf("verification path=%q; want /readyz", requestedPath)
	}
}

func TestRegisterWithRetryRetriesTransientFailures(t *testing.T) {
	originalDelay := registrationRetryDelay
	registrationRetryDelay = time.Millisecond
	t.Cleanup(func() { registrationRetryDelay = originalDelay })

	transient := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	attempts := 0
	err := registerWithRetry(context.Background(), func(context.Context, config.Config, string) error {
		attempts++
		if attempts <= 2 {
			return fmt.Errorf("register: %w", transient)
		}
		return nil
	}, validConfig(), "")
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("registration attempts=%d, want 3", attempts)
	}
}

func TestRegisterWithRetryRetriesServerErrorButNotPermanentErrors(t *testing.T) {
	originalDelay := registrationRetryDelay
	registrationRetryDelay = time.Millisecond
	t.Cleanup(func() { registrationRetryDelay = originalDelay })

	t.Run("server error then success", func(t *testing.T) {
		attempts := 0
		err := registerWithRetry(context.Background(), func(context.Context, config.Config, string) error {
			attempts++
			if attempts == 1 {
				return &dashboardruntime.CredimiStatusError{Status: "503 Service Unavailable", StatusCode: http.StatusServiceUnavailable}
			}
			return nil
		}, validConfig(), "")
		if err != nil || attempts != 2 {
			t.Fatalf("err=%v attempts=%d", err, attempts)
		}
	})
	t.Run("unauthorized fails promptly", func(t *testing.T) {
		attempts := 0
		err := registerWithRetry(context.Background(), func(context.Context, config.Config, string) error {
			attempts++
			return &dashboardruntime.CredimiStatusError{Status: "401 Unauthorized", StatusCode: http.StatusUnauthorized}
		}, validConfig(), "")
		if err == nil || attempts != 1 {
			t.Fatalf("err=%v attempts=%d", err, attempts)
		}
	})
	for _, statusCode := range []int{http.StatusTooManyRequests, http.StatusBadRequest, http.StatusForbidden} {
		statusCode := statusCode
		t.Run(fmt.Sprintf("status %d", statusCode), func(t *testing.T) {
			attempts := 0
			err := registerWithRetry(context.Background(), func(context.Context, config.Config, string) error {
				attempts++
				if statusCode == http.StatusTooManyRequests && attempts == 2 {
					return nil
				}
				return &dashboardruntime.CredimiStatusError{StatusCode: statusCode}
			}, validConfig(), "")
			wantAttempts := 1
			if statusCode == http.StatusTooManyRequests {
				wantAttempts = 2
			}
			wantErr := statusCode != http.StatusTooManyRequests
			if (err != nil) != wantErr || attempts != wantAttempts {
				t.Fatalf("err=%v attempts=%d want=%d", err, attempts, wantAttempts)
			}
		})
	}
	t.Run("joined transient and permanent error fails promptly", func(t *testing.T) {
		attempts := 0
		err := registerWithRetry(context.Background(), func(context.Context, config.Config, string) error {
			attempts++
			return errors.Join(
				&dashboardruntime.CredimiStatusError{StatusCode: http.StatusServiceUnavailable},
				&dashboardruntime.CredimiStatusError{StatusCode: http.StatusBadRequest},
			)
		}, validConfig(), "")
		if err == nil || attempts != 1 {
			t.Fatalf("err=%v attempts=%d", err, attempts)
		}
	})
}

func TestRegisterWithRetryHonorsDeadlineAndCancellation(t *testing.T) {
	originalDelay := registrationRetryDelay
	registrationRetryDelay = time.Second
	t.Cleanup(func() { registrationRetryDelay = originalDelay })
	transient := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	attempts := 0
	err := registerWithRetry(ctx, func(context.Context, config.Config, string) error {
		attempts++
		return transient
	}, validConfig(), "")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error=%v", err)
	}
	if attempts != 1 {
		t.Fatalf("deadline attempts=%d, want 1", attempts)
	}

	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	err = registerWithRetry(ctx, func(context.Context, config.Config, string) error {
		t.Fatal("registration called after cancellation")
		return nil
	}, validConfig(), "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestSupervisorRequestStartPersistsDesiredRunning(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) {
			return &testAPI{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.updateState(func(state *PersistentState) {
		state.Desired = DesiredStopped
		state.Actual = ActualStopped
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestStart(); err != nil {
		t.Fatal(err)
	}
	state, err := (StateStore{Path: filepath.Join(dir, "runtime-state.json")}).Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Desired != DesiredRunning {
		t.Fatalf("persisted desired state=%q", state.Desired)
	}
}

func TestSupervisorCloseAndStateFailures(t *testing.T) {
	life := &testLife{}
	e := &testEdge{}
	w := &testWorkers{}
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) { return &testAPI{}, nil }, NewEdge: func(config.Config) (edge.Edge, error) { return e, nil }, NewWorkers: func(config.Config, *server.ProcessStore) WorkerSet { return w }, NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Status().Actual != ActualStopped {
		t.Fatal("nil close not stopped")
	}
	if err := s.updateState(func(st *PersistentState) { st.Desired = DesiredRunning }); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorCreateAndVerifyFailures(t *testing.T) {
	base := validConfig()
	apiErr := errors.New("api bind")
	s, err := New(t.TempDir(), func() (config.Config, error) { return base, nil }, Dependencies{NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) { return nil, apiErr }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, apiErr) {
		t.Fatalf("%v", err)
	}
	verifyErr := errors.New("verify")
	e := &testEdge{}
	w := &testWorkers{}
	s, err = New(t.TempDir(), func() (config.Config, error) { return base, nil }, Dependencies{NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) { return &testAPI{}, nil }, NewEdge: func(config.Config) (edge.Edge, error) { return e, nil }, NewWorkers: func(config.Config, *server.ProcessStore) WorkerSet { return w }, VerifyPublicEndpoint: func(context.Context, config.Config, string) error { return verifyErr }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, verifyErr) {
		t.Fatalf("%v", err)
	}
}

func TestSupervisorGenerationConstructionFailures(t *testing.T) {
	cfg := validConfig()
	apiErr := errors.New("api construction")
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) { return nil, apiErr },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, apiErr) {
		t.Fatalf("api error=%v", err)
	}
	otelErr := errors.New("otel construction")
	s, err = New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI:            func(config.Config, context.Context, *server.ProcessStore) (API, error) { return &testAPI{}, nil },
		InitObservability: func(context.Context, config.Config) (func(context.Context) error, error) { return nil, otelErr },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, otelErr) {
		t.Fatalf("otel error=%v", err)
	}
	edgeErr := errors.New("edge construction")
	s, err = New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI:  func(config.Config, context.Context, *server.ProcessStore) (API, error) { return &testAPI{}, nil },
		NewEdge: func(config.Config) (edge.Edge, error) { return nil, edgeErr },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, edgeErr) {
		t.Fatalf("edge error=%v", err)
	}
}

func TestSupervisorActivationWithoutOptionalDependencies(t *testing.T) {
	cfg := validConfig()
	// An API with no edge, workers, lifecycle, or observability is a valid
	// minimal generation for installation/bootstrap tests.
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) { return &testAPI{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.Actual != ActualRunning || !st.APIListening {
		t.Fatalf("minimal status=%+v", st)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorDependencyVariantsAndHelpers(t *testing.T) {
	cfg := validConfig()
	life, workers := &testLife{}, &testWorkers{}
	api := &testAPI{}
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewWorkers:         func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
		NewEdge:            func(config.Config) (edge.Edge, error) { return &testEdge{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.ConfigDir() == "" {
		t.Fatal("missing config dir")
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorStartsExplicitWorkerSet(t *testing.T) {
	cfg := validConfig()
	workers := &testWorkers{}
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI:     func(config.Config, context.Context, *server.ProcessStore) (API, error) { return &testAPI{}, nil },
		NewWorkers: func(config.Config, *server.ProcessStore) WorkerSet { return workers },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g := s.currentGeneration(); g == nil || !g.workers.Running() {
		t.Fatal("callback workers not marked running")
	}
	_ = s.Stop(context.Background())
}

func TestSupervisorAllowsNoWorkersAndBoundsContext(t *testing.T) {
	cfg := validConfig()
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) { return &testAPI{}, nil }})
	if err != nil || s == nil {
		t.Fatal("construct supervisor")
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, c := boundedContext(short, time.Hour); c == nil {
		t.Fatal("missing cancel")
	}
}
