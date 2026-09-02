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
	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/runtimesupervisor"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
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

func TestApplicationPublishesReachableControllerURLs(t *testing.T) {
	for _, tc := range []struct {
		name, listen, mode, wantListen, wantURL string
	}{
		{"native ipv4", "127.0.0.1:9051", "", "127.0.0.1:9051", "http://127.0.0.1:9051"},
		{"native alternate loopback", "127.0.0.2:9051", "", "127.0.0.2:9051", "http://127.0.0.2:9051"},
		{"native ipv6", "[::1]:9051", "", "[::1]:9051", "http://[::1]:9051"},
		{"bridge", "127.0.0.1:9051", "bridge", "0.0.0.0:9051", "http://127.0.0.1:9051"},
		{"host network", "127.0.0.1:9051", "host", "127.0.0.1:9051", "http://127.0.0.1:9051"},
		{"explicit address", "192.0.2.10:9051", "", "192.0.2.10:9051", "http://192.0.2.10:9051"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := runnerconfig.Bootstrap()
			cfg.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
			cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
			cfg.Temporal.Address = "temporal:7233"
			cfg.Server.DashboardListen = tc.listen
			cfg.Server.APIListen = "127.0.0.1:8050"
			cfg.Server.ReadHeaderTimeout = runnerconfig.Duration(time.Second)
			cfg.Server.ShutdownTimeout = runnerconfig.Duration(time.Second)
			cfg.Exposure.Mode = "manual"
			cfg.Exposure.PublicURL = "https://runner.example"
			cfg.Devices = []runnerconfig.DeviceConfig{{
				ID: "org/runner/device", Name: "Device", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
				AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "no_device"},
			}}
			cfg.Storage.StateDir = filepath.Join(dir, "state")
			cfg.Storage.ArtifactRetention = runnerconfig.Duration(time.Hour)
			if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
				t.Fatal(err)
			}
			t.Setenv(servicemanager.ServiceNetworkModeEnv, tc.mode)
			var gotAddress string
			s, err := runtimesupervisor.New(dir, func() (runnerconfig.Config, error) { return cfg, nil }, runtimesupervisor.Dependencies{})
			if err != nil {
				t.Fatal(err)
			}
			a := &Application{configDir: dir, supervisor: s, listen: func(network, address string) (net.Listener, error) {
				gotAddress = address
				return newTestListener(), nil
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			var metadata controller.Metadata
			metadataReady := time.NewTicker(5 * time.Millisecond)
			defer metadataReady.Stop()
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			for {
				select {
				case runErr := <-done:
					t.Fatalf("application exited before metadata: %v", runErr)
				default:
				}
				metadata, err = controller.ReadMetadata(dir)
				if err == nil {
					break
				}
				select {
				case <-metadataReady.C:
				case <-deadline.C:
					t.Fatalf("metadata not published: %v", err)
				}
			}
			if gotAddress != tc.wantListen {
				t.Fatalf("listen address=%q want %q", gotAddress, tc.wantListen)
			}
			if net.JoinHostPort(metadata.ListenHost, fmt.Sprint(metadata.ListenPort)) != tc.wantListen || metadata.PublicURL != tc.wantURL || metadata.ProbeURL != tc.wantURL+"/internal/controller/identity" {
				t.Fatalf("metadata=%+v want listen=%q URL=%q", metadata, tc.wantListen, tc.wantURL)
			}
			cancel()
			select {
			case runErr := <-done:
				if runErr != nil {
					t.Fatal(runErr)
				}
			case <-time.After(time.Second):
				t.Fatal("application did not stop")
			}
		})
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

func TestRuntimeConfigLoaderKeepsGenerationSnapshot(t *testing.T) {
	dir := t.TempDir()
	cfgA := runnerconfig.Bootstrap()
	cfgA.SchemaVersion = runnerconfig.SchemaVersion
	cfgA.Runner.ID = "org/runner"
	cfgA.Runner.Name = "runner"
	cfgA.Runner.Organization = "org"
	cfgA.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfgA.Temporal.Address = "temporal:7233"
	cfgA.Devices = []runnerconfig.DeviceConfig{{
		ID: "org/runner/device-a", Name: "A", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "A"},
	}}
	loaderA := runtimeConfigLoader(cfgA)
	first, err := loaderA()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Devices) != 1 || first.Devices[0].ID != "org/runner/device-a" {
		t.Fatalf("generation A inventory = %+v", first.Devices)
	}

	cfgB := cfgA
	cfgB.Devices = []runnerconfig.DeviceConfig{{
		ID: "org/runner/device-b", Name: "B", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.0.2.10", WiFiPort: "5555"},
	}}
	configPath := filepath.Join(dir, "config.toml")
	if err := runnerconfig.WriteFile(configPath, cfgB); err != nil {
		t.Fatal(err)
	}
	second, err := loaderA()
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Devices) != 1 || second.Devices[0].ID != "org/runner/device-a" {
		t.Fatalf("generation A changed after desired config write: %+v", second.Devices)
	}

	loaderB := runtimeConfigLoader(cfgB)
	generationB, err := loaderB()
	if err != nil {
		t.Fatal(err)
	}
	if len(generationB.Devices) != 1 || generationB.Devices[0].ID != "org/runner/device-b" {
		t.Fatalf("generation B inventory = %+v", generationB.Devices)
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

type countingApplicationAPI struct {
	starts  int
	started chan struct{}
}

func (a *countingApplicationAPI) Start() error {
	a.starts++
	if a.started != nil {
		close(a.started)
	}
	return nil
}
func (*countingApplicationAPI) Shutdown(context.Context) error { return nil }
func (*countingApplicationAPI) Listening() bool                { return false }

func TestApplicationDoesNotAutoStartStaleServiceConfiguration(t *testing.T) {
	dir := t.TempDir()
	cfgA := runnerconfig.Bootstrap()
	cfgA.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
	cfgA.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfgA.Temporal.Address = "temporal:7233"
	cfgA.Devices = []runnerconfig.DeviceConfig{{
		ID: "org/runner/device", Name: "Device", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "no_device"},
	}}
	cfgA.Android.RunnerImage = "runner:a"
	cfgB := cfgA
	cfgB.Android.RunnerImage = "runner:b"
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfgB); err != nil {
		t.Fatal(err)
	}
	if err := (runtimesupervisor.StateStore{Path: filepath.Join(dir, "runtime-state.json")}).Save(runtimesupervisor.PersistentState{Desired: runtimesupervisor.DesiredRunning, Actual: runtimesupervisor.ActualRunning}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(cfgA, true))
	api := &countingApplicationAPI{}
	s, err := runtimesupervisor.New(dir, nil, runtimesupervisor.Dependencies{
		NewAPI: func(runnerconfig.Config, context.Context) (runtimesupervisor.API, error) { return api, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	listenerReady := make(chan struct{})
	a := &Application{configDir: dir, supervisor: s, listen: func(string, string) (net.Listener, error) {
		close(listenerReady)
		return newTestListener(), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-listenerReady:
	case <-time.After(time.Second):
		select {
		case err := <-done:
			t.Fatalf("application exited before dashboard listen: %v", err)
		default:
			t.Fatal("dashboard did not start listening")
		}
	}
	if api.starts != 0 {
		t.Fatalf("stale service auto-started runtime %d time(s)", api.starts)
	}
	if got := s.Status(); got.Desired != runtimesupervisor.DesiredRunning || got.Actual != runtimesupervisor.ActualStopped {
		t.Fatalf("stale service changed runtime state: %+v", got)
	}
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

func TestApplicationAutoStartsMatchingServiceAfterProcessRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	cfg.Temporal.Address = "temporal:7233"
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "org/runner/device", Name: "Device", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "no_device"},
	}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := (runtimesupervisor.StateStore{Path: filepath.Join(dir, "runtime-state.json")}).Save(runtimesupervisor.PersistentState{Desired: runtimesupervisor.DesiredRunning, Actual: runtimesupervisor.ActualRunning}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(cfg, true))
	api := &countingApplicationAPI{started: make(chan struct{})}
	s, err := runtimesupervisor.New(dir, nil, runtimesupervisor.Dependencies{
		NewAPI: func(runnerconfig.Config, context.Context) (runtimesupervisor.API, error) { return api, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &Application{configDir: dir, supervisor: s, listen: func(string, string) (net.Listener, error) { return newTestListener(), nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-api.started:
	case <-time.After(time.Second):
		t.Fatal("matching service did not auto-start the runtime")
	}
	if api.starts != 1 {
		t.Fatalf("auto-start calls=%d", api.starts)
	}
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
