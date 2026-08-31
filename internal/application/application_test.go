package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/runtimesupervisor"
	"github.com/forkbombeu/credimi-runner/pkg/server"
)

func TestDashboardHostPortDefaults(t *testing.T) {
	host, port := dashboardHostPort(map[string]string{"DASHBOARD_HOST": "0.0.0.0"})
	if host != "127.0.0.1" || port != "8051" {
		t.Fatalf("got %s:%s", host, port)
	}
	host, port = dashboardHostPort(map[string]string{"DASHBOARD_HOST": "127.0.0.2", "DASHBOARD_PORT": "9000"})
	if host != "127.0.0.2" || port != "9000" {
		t.Fatalf("got %s:%s", host, port)
	}
}
func TestApplicationRequiresDirectory(t *testing.T) {
	if _, err := New(" "); err == nil {
		t.Fatal("expected config directory error")
	}
}

func TestApplicationUsesProvidedSupervisor(t *testing.T) {
	dir := t.TempDir()
	s, err := runtimesupervisor.New(dir, nil, runtimesupervisor.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if a.Supervisor() != s {
		t.Fatal("application did not retain provided supervisor")
	}
}
func TestRegistrationManagerImplementsRuntimeManager(t *testing.T) {
	var _ runtime.Manager = (*registrationManager)(nil)
	m := &registrationManager{url: "https://runner"}
	if m.Status(context.Background()).PublicURL != "https://runner" {
		t.Fatal("url missing")
	}
	if _, err := m.Logs(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []func(context.Context) error{m.Start, m.Stop, m.Restart, m.UpdateImage} {
		if err := fn(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	m.Configure(runtime.Values{})
	m.SetPublicURL("https://other")
}
func TestRuntimeDependenciesCreateManualEdge(t *testing.T) {
	deps := runtimeDependencies(t.TempDir())
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal:7233"
	cfg.Exposure.Mode = "manual"
	cfg.Exposure.PublicURL = "https://runner"
	e, err := deps.NewEdge(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if e == nil {
		t.Fatal("edge missing")
	}
}

func TestRuntimeDependenciesCreateCloudflaredEdge(t *testing.T) {
	deps := runtimeDependencies(t.TempDir())
	cfg := runnerconfig.Bootstrap()
	cfg.Exposure.Mode = "named_tunnel"
	cfg.Exposure.Domain = "runner.example"
	cfg.Exposure.CloudflareToken = "token"
	e, err := deps.NewEdge(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if e == nil || !strings.Contains(fmt.Sprintf("%T", e), "Cloudflared") {
		t.Fatalf("edge=%T", e)
	}
}

type testWorkerService struct {
	err   error
	calls int
}

func (s *testWorkerService) StartExistingWorkers(context.Context) error { s.calls++; return s.err }
func TestWorkerSetDelegatesAndStops(t *testing.T) {
	service := &testWorkerService{}
	store := server.NewProcessStore()
	w := &workerSet{service: service, store: store}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.StopAll()
	if err := w.WaitAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.calls != 1 {
		t.Fatalf("calls=%d", service.calls)
	}
	if w.Running() {
		t.Fatal("empty worker set should report stopped")
	}
}
func TestWorkerSetPropagatesStartupError(t *testing.T) {
	want := errors.New("startup")
	w := &workerSet{service: &testWorkerService{err: want}, store: server.NewProcessStore()}
	if !errors.Is(w.Start(context.Background()), want) {
		t.Fatal("startup error not propagated")
	}
}
func TestControllerAdapterDelegatesState(t *testing.T) {
	s, err := runtimesupervisor.New(t.TempDir(), func() (runnerconfig.Config, error) { return runnerconfig.Config{}, nil }, runtimesupervisor.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	a := &controllerAdapter{supervisor: s}
	if a.ExecutionRunning() {
		t.Fatal("unexpected running intent")
	}
	if _, err := a.CurrentPublicURL(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.VerifyPublicURL(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := a.Status(context.Background()); got.Configured != true {
		t.Fatal("status not configured")
	}
	_ = a.Prepare(context.Background())
	_ = a.StartExecution(context.Background())
	_ = a.Stop(context.Background())
}

func TestControllerAdapterReconcileLoadsTypedConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal:7233"
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := (runtimesupervisor.StateStore{Path: filepath.Join(dir, "runtime-state.json")}).Save(runtimesupervisor.PersistentState{Desired: runtimesupervisor.DesiredStopped, Actual: runtimesupervisor.ActualStopped}); err != nil {
		t.Fatal(err)
	}
	s, err := runtimesupervisor.New(dir, nil, runtimesupervisor.Dependencies{
		NewAPI: func(runnerconfig.Config, context.Context) (runtimesupervisor.API, error) { return &appTestAPI{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&controllerAdapter{supervisor: s}).Reconcile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

type appTestAPI struct{}

func (*appTestAPI) Start() error                   { return nil }
func (*appTestAPI) Shutdown(context.Context) error { return nil }
func (*appTestAPI) Listening() bool                { return false }

func TestApplicationShutdownIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := runtimesupervisor.New(dir, func() (runnerconfig.Config, error) { return runnerconfig.Config{}, nil }, runtimesupervisor.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	a := &Application{configDir: dir, supervisor: s}
	if err := a.shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := a.shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationShutdownClosesListenerWithoutServer(t *testing.T) {
	l := newTestListener()
	a := &Application{dashboardListener: l}
	if err := a.shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.closed:
	default:
		t.Fatal("listener remained open")
	}
}

func TestAtoiPort(t *testing.T) {
	if got := atoiPort("8123"); got != 8123 {
		t.Fatalf("port=%d", got)
	}
	if got := atoiPort("bad"); got != 0 {
		t.Fatalf("bad port=%d", got)
	}
}

type testListener struct {
	closed chan struct{}
}

func newTestListener() *testListener              { return &testListener{closed: make(chan struct{})} }
func (l *testListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *testListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}
func (l *testListener) Addr() net.Addr { return testAddr("127.0.0.1:8051") }

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

func TestApplicationRunStartsDashboardAndShutsDown(t *testing.T) {
	dir := t.TempDir()
	s, err := runtimesupervisor.New(dir, func() (runnerconfig.Config, error) { return runnerconfig.Config{}, nil }, runtimesupervisor.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	a := &Application{configDir: dir, supervisor: s, listen: func(string, string) (net.Listener, error) { return newTestListener(), nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("application did not shut down")
	}
}

func TestRuntimeDependencyFactories(t *testing.T) {
	deps := runtimeDependencies(t.TempDir())
	cfg := runnerconfig.Bootstrap()
	cfg.Exposure.Mode = "manual"
	cfg.Exposure.PublicURL = "https://runner"
	w := deps.NewWorkersWithStore(cfg, server.NewProcessStore())
	if w == nil || w.Running() {
		t.Fatal("worker set unavailable")
	}
	w.StopAll()
	if err := w.WaitAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if deps.NewLifecycleClient(cfg, server.NewProcessStore()) == nil {
		t.Fatal("lifecycle client missing")
	}
	if err := deps.Register(context.Background(), cfg, "https://runner"); err == nil {
		t.Fatal("expected remote registration failure")
	}
}
