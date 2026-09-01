package runtimesupervisor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/edge"
	"github.com/forkbombeu/credimi-runner/pkg/server"
)

type testAPI struct {
	mu                 sync.Mutex
	started, closed    bool
	startErr, closeErr error
	startHook          func()
	failures           chan error
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
func (a *testAPI) Listening() bool        { a.mu.Lock(); defer a.mu.Unlock(); return a.started && !a.closed }
func (a *testAPI) Failures() <-chan error { return a.failures }

type testEdge struct {
	mu                          sync.Mutex
	starts, stops               int
	startErr, stopErr, closeErr error
	running                     bool
	startHook                   func()
	failures                    chan error
	origins                     []string
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
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{NewAPI: func(config.Config, context.Context) (API, error) { return api, nil }, NewEdge: func(config.Config) (edge.Edge, error) { return edgeImpl, nil }, NewWorkers: func(config.Config) WorkerSet { return workers }, NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life }})
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
	if !api.Listening() || !s.ExecutionRunning() {
		t.Fatal("not running")
	}
	if err := s.Stop(context.Background()); err == nil {
		t.Fatal("expected pause error")
	}
	if api.Listening() || e.Running() || s.ExecutionRunning() {
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
		NewAPI:             func(config.Config, context.Context) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config) WorkerSet { return workers },
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
		NewAPI:             func(config.Config, context.Context) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config) WorkerSet { return workers },
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
		NewAPI:  func(config.Config, context.Context) (API, error) { return api, nil },
		NewEdge: func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
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
		NewAPI: func(config.Config, context.Context) (API, error) {
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
		NewAPI: func(config.Config, context.Context) (API, error) {
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
	s, err := NewSupervisor(dir, func() (config.Config, error) { return cfg, nil }, Dependencies{})
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
	if s.Status().Actual != ActualRunning || !s.ExecutionRunning() {
		t.Fatalf("restart state=%+v", s.Status())
	}
}

func TestSupervisorLocalCleanupErrorsRetainGeneration(t *testing.T) {
	life := &testLife{}
	edgeImpl := &testEdge{closeErr: errors.New("edge close")}
	api := &testAPI{closeErr: errors.New("api close")}
	workers := &testWorkers{}
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{
		NewAPI:             func(config.Config, context.Context) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config) WorkerSet { return workers },
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
	if !s.ExecutionRunning() || s.Status().Actual != ActualRunning {
		t.Fatalf("restart state=%+v", s.Status())
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.ExecutionRunning() || s.Status().Actual != ActualStopped {
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
	if s.ExecutionRunning() || s.Status().Actual != ActualStopped {
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
		NewAPI: func(config.Config, context.Context) (API, error) { return &testAPI{}, nil },
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
		NewAPI: func(config.Config, context.Context) (API, error) { return &testAPI{}, nil }, NewEdge: func(config.Config) (edge.Edge, error) { return e, nil }, NewWorkers: func(config.Config) WorkerSet { return w }, NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life },
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
		NewAPI:     func(config.Config, context.Context) (API, error) { return api, nil },
		NewWorkers: func(config.Config) WorkerSet { return workers },
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
		NewAPI:             func(config.Config, context.Context) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config) WorkerSet { return workers },
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
		NewAPI:             func(config.Config, context.Context) (API, error) { return api, nil },
		NewEdge:            func(config.Config) (edge.Edge, error) { return edgeImpl, nil },
		NewWorkers:         func(config.Config) WorkerSet { return workers },
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

func TestSupervisorCloseAndStateFailures(t *testing.T) {
	life := &testLife{}
	e := &testEdge{}
	w := &testWorkers{}
	s, err := New(t.TempDir(), func() (config.Config, error) { return validConfig(), nil }, Dependencies{NewAPI: func(config.Config, context.Context) (API, error) { return &testAPI{}, nil }, NewEdge: func(config.Config) (edge.Edge, error) { return e, nil }, NewWorkers: func(config.Config) WorkerSet { return w }, NewLifecycleClient: func(config.Config, *server.ProcessStore) LifecycleClient { return life }})
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
	s, err := New(t.TempDir(), func() (config.Config, error) { return base, nil }, Dependencies{NewAPI: func(config.Config, context.Context) (API, error) { return nil, apiErr }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, apiErr) {
		t.Fatalf("%v", err)
	}
	verifyErr := errors.New("verify")
	e := &testEdge{}
	w := &testWorkers{}
	s, err = New(t.TempDir(), func() (config.Config, error) { return base, nil }, Dependencies{NewAPI: func(config.Config, context.Context) (API, error) { return &testAPI{}, nil }, NewEdge: func(config.Config) (edge.Edge, error) { return e, nil }, NewWorkers: func(config.Config) WorkerSet { return w }, VerifyPublicEndpoint: func(context.Context, config.Config, string) error { return verifyErr }})
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
		NewAPI: func(config.Config, context.Context) (API, error) { return nil, apiErr },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, apiErr) {
		t.Fatalf("api error=%v", err)
	}
	otelErr := errors.New("otel construction")
	s, err = New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
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
		NewAPI:  func(config.Config, context.Context) (API, error) { return &testAPI{}, nil },
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
		NewAPI: func(config.Config, context.Context) (API, error) { return &testAPI{}, nil },
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
		NewAPIWithStore:     func(config.Config, context.Context, *server.ProcessStore) (API, error) { return api, nil },
		NewWorkersWithStore: func(config.Config, *server.ProcessStore) WorkerSet { return workers },
		NewLifecycleClient:  func(config.Config, *server.ProcessStore) LifecycleClient { return life },
		NewEdge:             func(config.Config) (edge.Edge, error) { return &testEdge{}, nil },
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

func TestSupervisorStartWorkersCallback(t *testing.T) {
	cfg := validConfig()
	called := 0
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{
		NewAPI:       func(config.Config, context.Context) (API, error) { return &testAPI{}, nil },
		StartWorkers: func(context.Context, config.Config, *server.ProcessStore) error { called++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("workers callback=%d", called)
	}
	if g := s.currentGeneration(); g == nil || !g.workers.Running() {
		t.Fatal("callback workers not marked running")
	}
	_ = s.Stop(context.Background())
}

func TestSupervisorNoopGenerationAndBoundedContext(t *testing.T) {
	cfg := validConfig()
	s, err := New(t.TempDir(), func() (config.Config, error) { return cfg, nil }, Dependencies{NewAPI: func(config.Config, context.Context) (API, error) { return &testAPI{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.Status().APIListening && s.Status().Actual != ActualRunning {
		t.Fatal("unexpected status")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, c := boundedContext(short, time.Hour); c == nil {
		t.Fatal("missing cancel")
	}
}

func TestNoopAPIIsSafe(t *testing.T) {
	a := &noopAPI{}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Listening() {
		t.Fatal("noop API reports listening")
	}
}

type supervisorListener struct{ done chan struct{} }

func (l *supervisorListener) Accept() (net.Conn, error) { <-l.done; return nil, net.ErrClosed }
func (l *supervisorListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}
func (l *supervisorListener) Addr() net.Addr { return supervisorAddr("127.0.0.1:8050") }

type supervisorAddr string

func (a supervisorAddr) Network() string { return "tcp" }
func (a supervisorAddr) String() string  { return string(a) }

func TestHTTPAPIStartShutdownAndListenFailure(t *testing.T) {
	old := listenTCP
	t.Cleanup(func() { listenTCP = old })
	listener := &supervisorListener{done: make(chan struct{})}
	listenTCP = func(string, string) (net.Listener, error) { return listener, nil }
	a, err := NewHTTPAPI(validConfig(), http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if !a.Listening() {
		t.Fatal("API not listening")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Listening() {
		t.Fatal("API still listening")
	}
	if err := a.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	listenTCP = func(string, string) (net.Listener, error) { return nil, errors.New("bind") }
	if _, err := NewHTTPAPI(validConfig(), http.NewServeMux()); err == nil {
		t.Fatal("expected bind failure")
	}
}

func TestHTTPAPIRealShutdownUsesConfiguredTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.Server.APIListen = "127.0.0.1:0"
	cfg.Server.ReadHeaderTimeout = config.Duration(17 * time.Second)
	cfg.Server.ShutdownTimeout = config.Duration(23 * time.Second)
	a, err := NewHTTPAPI(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	if err != nil {
		t.Fatal(err)
	}
	if a.Server.ReadHeaderTimeout != 17*time.Second || a.shutdownTimeout != 23*time.Second {
		t.Fatalf("timeouts=%s/%s", a.Server.ReadHeaderTimeout, a.shutdownTimeout)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get("http://" + a.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Listening() {
		t.Fatal("API still listening after shutdown")
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-a.Failures():
		t.Fatalf("normal shutdown reported failure: %v", failure)
	default:
	}
}

type failingListener struct{}

func (failingListener) Accept() (net.Conn, error) { return nil, errors.New("accept failed") }
func (failingListener) Close() error              { return nil }
func (failingListener) Addr() net.Addr            { return supervisorAddr("127.0.0.1:8050") }

func TestHTTPAPIUnexpectedServeFailureReportsFailure(t *testing.T) {
	old := listenTCP
	t.Cleanup(func() { listenTCP = old })
	listenTCP = func(string, string) (net.Listener, error) { return failingListener{}, nil }
	a, err := NewHTTPAPI(validConfig(), http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-a.Failures():
		if failure == nil || !strings.Contains(failure.Error(), "accept failed") {
			t.Fatalf("failure=%v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("serve failure was not reported")
	}
	if a.Listening() {
		t.Fatal("failed API still reports listening")
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPAPIShutdownBeforeStartAndClosedStart(t *testing.T) {
	old := listenTCP
	t.Cleanup(func() { listenTCP = old })
	listener := &supervisorListener{done: make(chan struct{})}
	listenTCP = func(string, string) (net.Listener, error) { return listener, nil }
	a, err := NewHTTPAPI(validConfig(), http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Listening() {
		t.Fatal("never-started API reports listening")
	}
	if err := a.Start(); err == nil {
		t.Fatal("expected closed API start error")
	}
}

func TestHTTPAPINilShutdownIsSafe(t *testing.T) {
	var api *HTTPAPI
	if err := api.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.Listening() {
		t.Fatal("nil API reports listening")
	}
}
