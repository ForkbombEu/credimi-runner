package runtimesupervisor

import (
	"context"
	"errors"
	"net"
	"net/http"
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
}

func (a *testAPI) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.startErr != nil {
		return a.startErr
	}
	a.started = true
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

type testEdge struct {
	mu                          sync.Mutex
	starts, stops               int
	startErr, stopErr, closeErr error
	running                     bool
}

func (e *testEdge) Start(context.Context, string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts++
	if e.startErr != nil {
		return "", e.startErr
	}
	e.running = true
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
func (e *testEdge) Running() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.running }

type testWorkers struct {
	mu                   sync.Mutex
	starts, stops, waits int
	startErr, errorWait  error
	running              bool
	block                chan struct{}
}

func (w *testWorkers) Start(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.starts++
	if w.startErr != nil {
		return w.startErr
	}
	w.running = true
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
	l.mu.Unlock()
	return e
}
func (l *testLife) Heartbeat(context.Context) error {
	l.mu.Lock()
	l.beats++
	e := l.errorBeat
	l.mu.Unlock()
	return e
}
func (l *testLife) StartHeartbeatLoop(context.Context) func() {
	l.mu.Lock()
	l.loops++
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

func TestSupervisorStoppedReconcileBuildsGeneration(t *testing.T) {
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
	if e.Running() || w.Running() {
		t.Fatal("stopped reconcile activated resources")
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
	_ = s.updateState(func(st *PersistentState) { st.Desired = DesiredRunning })
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
