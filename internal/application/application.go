package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/edge"
	"github.com/forkbombeu/credimi-runner/internal/runtimesupervisor"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
	"github.com/forkbombeu/credimi-runner/pkg/server"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	cluelog "goa.design/clue/log"
)

type Application struct {
	configDir                   string
	supervisor                  *runtimesupervisor.Supervisor
	dashboardListener           net.Listener
	dashboardServer             *http.Server
	dashboardShutdownTimeout    time.Duration
	closeSupervisor             func(context.Context) error
	shutdownDashboard           func(context.Context) error
	newDashboardShutdownContext func(time.Duration) (context.Context, context.CancelFunc)
	operations                  *controller.Coordinator
	lease                       *controller.Lease
	metadata                    controller.Metadata
	closeOnce                   sync.Once
	listen                      func(string, string) (net.Listener, error)
}

func (a *Application) Supervisor() *runtimesupervisor.Supervisor { return a.supervisor }

func New(configDir string, supervisors ...*runtimesupervisor.Supervisor) (*Application, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, errors.New("application config directory is empty")
	}
	var supervisor *runtimesupervisor.Supervisor
	if len(supervisors) > 0 {
		supervisor = supervisors[0]
	}
	if supervisor == nil {
		var err error
		deps := runtimeDependencies(configDir)
		supervisor, err = runtimesupervisor.New(configDir, nil, deps)
		if err != nil {
			return nil, err
		}
	}
	return &Application{configDir: configDir, supervisor: supervisor, listen: net.Listen}, nil
}

func runtimeDependencies(configDir string) runtimesupervisor.Dependencies {
	return runtimesupervisor.Dependencies{
		NewEdge: func(cfg runnerconfig.Config) (edge.Edge, error) {
			mode := cfg.Exposure.Mode
			if mode == "manual" {
				return edge.NewManual(cfg.Exposure.PublicURL), nil
			}
			exe, err := os.Executable()
			if err != nil {
				return nil, err
			}
			binary := filepath.Join(filepath.Dir(exe), "credimi-cloudflared")
			if runtime.GOOS != "darwin" {
				binary = "/usr/local/bin/cloudflared"
			}
			return edge.NewCloudflared(binary, mode, cfg.Exposure.CloudflareToken, cfg.Exposure.Domain), nil
		},
		NewAPI: func(cfg runnerconfig.Config, ctx context.Context, store *server.ProcessStore) (runtimesupervisor.API, error) {
			applyEnvironment(cfg)
			apiCfg := cfg
			listen, err := executionAPIBindAddress(cfg.Server.APIListen, os.Getenv(servicemanager.ServiceNetworkModeEnv))
			if err != nil {
				return nil, err
			}
			apiCfg.Server.APIListen = listen
			loader := runtimeConfigLoader(cfg)
			svc := server.NewRunnerServiceWithDeps(store, utils.LoadInstance(), server.Deps{RuntimeConfigLoader: loader})
			readiness := server.NewReadinessService()
			readiness.RuntimeConfig = loader
			return runtimesupervisor.NewHTTPAPI(apiCfg, server.NewHTTPHandlerWithReadiness(ctx, svc, false, readiness))
		},
		NewWorkers: func(cfg runnerconfig.Config, store *server.ProcessStore) runtimesupervisor.WorkerSet {
			applyEnvironment(cfg)
			return &workerSet{service: server.NewRunnerServiceWithDeps(store, utils.LoadInstance(), server.Deps{RuntimeConfigLoader: runtimeConfigLoader(cfg)}), store: store}
		},
		NewLifecycleClient: func(cfg runnerconfig.Config, store *server.ProcessStore) runtimesupervisor.LifecycleClient {
			applyEnvironment(cfg)
			loader := runtimeConfigLoader(cfg)
			return server.NewRunnerLifecycleClient(server.LoadRunnerLifecycleConfig(utils.LoadInstance(), loader), http.DefaultClient, store, loader)
		},
		ValidateRuntimeCapabilities: func(ctx context.Context, cfg runnerconfig.Config) error {
			return androidtools.EnsureEmulatorReady(ctx, cfg, runtime.GOOS, nil)
		},
		Register:             runtimesupervisor.Register,
		VerifyPublicEndpoint: runtimesupervisor.VerifyPublicEndpoint,
	}
}

func runtimeConfigLoader(cfg runnerconfig.Config) server.RuntimeConfigLoader {
	values := dashboardruntime.ValuesFromTypedConfig(cfg)
	return func() (dashboardruntime.RunnerRuntimeConfig, error) {
		snapshot := make(dashboardruntime.Values, len(values))
		for key, value := range values {
			snapshot[key] = value
		}
		return dashboardruntime.ParseRuntimeConfig(snapshot)
	}
}

func applyEnvironment(cfg runnerconfig.Config) {
	for key, value := range dashboardruntime.ValuesFromTypedConfig(cfg) {
		if value == "" {
			_ = os.Unsetenv(key)
		} else {
			_ = os.Setenv(key, value)
		}
	}
}

type workerSet struct {
	service interface{ StartExistingWorkers(context.Context) error }
	store   *server.ProcessStore
}

func (w *workerSet) Start(ctx context.Context) error   { return w.service.StartExistingWorkers(ctx) }
func (w *workerSet) StopAll()                          { w.store.StopAll() }
func (w *workerSet) WaitAll(ctx context.Context) error { return w.store.WaitAll(ctx) }
func (w *workerSet) Running() bool {
	for _, p := range w.store.List() {
		if p.IsRunning() {
			return true
		}
	}
	return false
}

func (a *Application) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	format := cluelog.FormatJSON
	if cluelog.IsTerminal() {
		format = cluelog.FormatTerminal
	}
	ctx = cluelog.Context(ctx, cluelog.WithFormat(format))
	cfg, err := dashboard.LoadConfig(a.configDir)
	if err != nil {
		return err
	}
	values, err := dashboardruntime.NormalizeValues(cfg.Snapshot(), runtime.GOOS)
	if err != nil {
		return err
	}
	typedCfg, err := dashboardruntime.TypedConfigFromValues(values)
	if err != nil {
		return err
	}
	if err := runnerconfig.ApplyDefaults(&typedCfg); err != nil {
		return err
	}
	a.dashboardShutdownTimeout = typedCfg.Server.ShutdownTimeout.Duration()
	host, port := dashboardHostPort(values)
	serviceNetworkMode := os.Getenv(servicemanager.ServiceNetworkModeEnv)
	bindHost := host
	if serviceNetworkMode == "bridge" {
		bindHost = "0.0.0.0"
	}
	listen := a.listen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp", net.JoinHostPort(bindHost, port))
	if err != nil {
		return err
	}
	a.dashboardListener = listener
	lease, err := controller.Acquire(a.configDir)
	if err != nil {
		_ = listener.Close()
		return err
	}
	a.lease = lease
	defer func() { _ = lease.Close() }()
	a.operations = controller.NewCoordinator(ctx)
	identity, err := controller.NewIdentityToken()
	if err != nil {
		_ = lease.Close()
		_ = listener.Close()
		return err
	}
	plan := dashboardruntime.BuildRuntimePlan(a.configDir, values)
	handler, cancelHandler, err := dashboard.NewHandler(ctx, a.configDir, fmt.Sprintf("application-%d", os.Getpid()), identity, plan.ConfigFingerprint, a.supervisor, a.operations)
	if err != nil {
		listener.Close()
		return err
	}
	controllerHost := dashboardControllerHost(host, bindHost, serviceNetworkMode)
	controllerURL := localHTTPURL(controllerHost, port)
	if err := lease.Publish(controller.Metadata{
		ControllerID: fmt.Sprintf("application-%d", os.Getpid()), PID: os.Getpid(), ConfigDir: a.configDir,
		ListenHost: bindHost, ListenPort: atoiPort(port), ProbeURL: controllerURL + "/internal/controller/identity",
		PublicURL: controllerURL, ConfigFingerprint: plan.ConfigFingerprint, IdentityToken: identity,
	}); err != nil {
		cancelHandler()
		_ = listener.Close()
		return err
	}
	a.dashboardServer = &http.Server{Handler: handler, ReadHeaderTimeout: typedCfg.Server.ReadHeaderTimeout.Duration()}
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.dashboardServer.Serve(listener) }()
	serviceRestartRequired := dashboardruntime.ServiceRestartRequired(values, cfg.Exists())
	if cfg.Exists() && a.supervisor.Status().Desired == runtimesupervisor.DesiredRunning && !serviceRestartRequired {
		if _, err := a.operations.Submit(controller.OperationRuntimeStart, func(opCtx context.Context, _ func(controller.Progress)) error {
			return a.supervisor.Start(opCtx)
		}); err != nil {
			fmt.Printf("submit runtime auto-start: %v\n", err)
		}
	}
	select {
	case <-ctx.Done():
		cancelHandler()
		return a.shutdown()
	case err := <-serveErr:
		cancelHandler()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return a.shutdown()
	}
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func dashboardControllerHost(desiredHost, bindHost, serviceNetworkMode string) string {
	if serviceNetworkMode == "bridge" {
		return "127.0.0.1"
	}
	return bindHost
}

func localHTTPURL(host, port string) string {
	return "http://" + net.JoinHostPort(host, port)
}

func executionAPIBindAddress(desiredListen, serviceNetworkMode string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(desiredListen))
	if err != nil {
		return "", fmt.Errorf("parse execution API listen address %q: %w", desiredListen, err)
	}
	if serviceNetworkMode == "bridge" || serviceNetworkMode == "host" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, port), nil
}

func atoiPort(port string) int {
	var n int
	_, _ = fmt.Sscanf(port, "%d", &n)
	return n
}

func (a *Application) shutdown() error {
	var result error
	a.closeOnce.Do(func() {
		if a.closeSupervisor != nil {
			result = errors.Join(result, a.closeSupervisor(context.Background()))
		} else if a.supervisor != nil {
			result = errors.Join(result, a.supervisor.Close(context.Background()))
		}
		if a.dashboardServer != nil {
			newContext := a.newDashboardShutdownContext
			if newContext == nil {
				newContext = func(timeout time.Duration) (context.Context, context.CancelFunc) {
					return context.WithTimeout(context.Background(), timeout)
				}
			}
			ctx, cancel := newContext(a.dashboardShutdownTimeout)
			shutdown := a.shutdownDashboard
			if shutdown == nil {
				shutdown = a.dashboardServer.Shutdown
			}
			result = errors.Join(result, shutdown(ctx))
			cancel()
		} else if a.dashboardListener != nil {
			result = errors.Join(result, a.dashboardListener.Close())
		}
	})
	return result
}
func dashboardHostPort(values map[string]string) (string, string) {
	host := strings.TrimSpace(values["DASHBOARD_HOST"])
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := values["DASHBOARD_PORT"]
	if port == "" {
		port = "8051"
	}
	return host, port
}
