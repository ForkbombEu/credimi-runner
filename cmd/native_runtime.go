//go:build darwin

package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/pkg/observability"
	"github.com/forkbombeu/credimi-runner/pkg/server"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/spf13/cobra"
	cluelog "goa.design/clue/log"
)

// NativeRuntimeSupervisor owns the only macOS execution generation. The
// Dashboard outlives generations and asks this owner to replace them.
type NativeRuntimeSupervisor struct {
	configDir string
	root      context.Context

	transitionMu sync.Mutex
	mu           sync.RWMutex
	generation   *nativeRuntimeGeneration
	executing    bool
}

type nativeRuntimeGeneration struct {
	values       dashboardruntime.Values
	ctx          context.Context
	cancel       context.CancelFunc
	listener     net.Listener
	http         *http.Server
	store        *server.ProcessStore
	service      interface{ StartExistingWorkers(context.Context) error }
	lifecycle    *server.RunnerLifecycleClient
	edge         *dashboardruntime.LifecycleManager
	stopBeat     func()
	shutdownOTEL func(context.Context) error
	stopOnce     sync.Once
}

func NewNativeRuntimeSupervisor(configDir string) *NativeRuntimeSupervisor {
	return NewNativeRuntimeSupervisorWithContext(context.Background(), configDir)
}

// runNativeApplicationRuntime keeps Dashboard alive while the supervisor
// replaces only the configuration-dependent execution generation.
func runNativeApplicationRuntime(cmd *cobra.Command, args []string) error {
	configDir := effectiveConfigDir()
	if err := os.Setenv("CREDIMI_RUNNER_CONFIG_DIR", configDir); err != nil {
		return err
	}
	if err := setRunnerBootID(); err != nil {
		return err
	}
	supervisor := NewNativeRuntimeSupervisorWithContext(cmd.Context(), configDir)
	defer supervisor.Close(context.Background())
	if _, err := os.Stat(filepath.Join(configDir, "config.toml")); err == nil {
		if err := supervisor.Reconcile(cmd.Context(), nativeRuntimeShouldRun(configDir)); err != nil {
			return err
		}
	}
	errCh := make(chan error, 1)
	go func() { errCh <- runDashboardOwnedWithNativeRuntime(cmd, args, supervisor) }()
	select {
	case err := <-errCh:
		return err
	case <-cmd.Context().Done():
		return cmd.Context().Err()
	}
}

func nativeRuntimeShouldRun(configDir string) bool {
	state, err := os.ReadFile(filepath.Join(configDir, "runtime-state"))
	return err != nil || strings.TrimSpace(string(state)) != "stopped"
}

func NewNativeRuntimeSupervisorWithContext(root context.Context, configDir string) *NativeRuntimeSupervisor {
	if root == nil {
		root = context.Background()
	}
	return &NativeRuntimeSupervisor{configDir: configDir, root: root}
}

func (s *NativeRuntimeSupervisor) Prepare(ctx context.Context) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	values, err := nativeRuntimeValues(s.configDir)
	if err != nil {
		return err
	}
	s.mu.RLock()
	current := s.generation
	matched := current != nil && reflect.DeepEqual(current.values, values)
	s.mu.RUnlock()
	if !matched {
		return s.reconcileLocked(ctx, values, true)
	}
	return current.startEdge(ctx)
}

// Reconcile replaces the generation from the current typed TOML. It never
// starts workers; Dashboard registration must succeed first.
func (s *NativeRuntimeSupervisor) Reconcile(ctx context.Context, running bool) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	values, err := nativeRuntimeValues(s.configDir)
	if err != nil {
		return err
	}
	return s.reconcileLocked(ctx, values, running)
}

func (s *NativeRuntimeSupervisor) reconcileLocked(ctx context.Context, values dashboardruntime.Values, running bool) error {
	s.mu.Lock()
	previous := s.generation
	wasExecuting := s.executing
	s.generation = nil
	s.executing = false
	s.mu.Unlock()
	if previous != nil {
		if err := previous.close(ctx, wasExecuting); err != nil {
			return fmt.Errorf("close previous native runtime generation: %w", err)
		}
	}
	next, err := newNativeRuntimeGeneration(s.root, s.configDir, values)
	if err != nil {
		_ = writeRuntimeState(s.configDir, "failed: "+err.Error())
		return err
	}
	if running {
		if err := next.startEdge(ctx); err != nil {
			_ = next.close(ctx, false)
			_ = writeRuntimeState(s.configDir, "failed: "+err.Error())
			return err
		}
	}
	s.mu.Lock()
	s.generation = next
	s.mu.Unlock()
	if running {
		return writeRuntimeState(s.configDir, "starting")
	}
	return writeRuntimeState(s.configDir, "stopped")
}

func (s *NativeRuntimeSupervisor) StartExecution(ctx context.Context) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.mu.RLock()
	generation := s.generation
	alreadyRunning := s.executing
	s.mu.RUnlock()
	if generation == nil {
		return fmt.Errorf("native runtime generation is unavailable")
	}
	if alreadyRunning {
		return nil
	}
	if err := generation.startEdge(ctx); err != nil {
		return fmt.Errorf("start native edge: %w", err)
	}
	if err := generation.service.StartExistingWorkers(generation.ctx); err != nil {
		return fmt.Errorf("start native workers: %w", err)
	}
	if err := generation.lifecycle.Resume(ctx, "dashboard_start"); err != nil {
		generation.store.StopAll()
		return fmt.Errorf("resume runner lifecycle: %w", err)
	}
	if err := generation.lifecycle.Heartbeat(ctx); err != nil {
		generation.store.StopAll()
		_ = generation.lifecycle.Pause(ctx, "startup_failed")
		return fmt.Errorf("send runner heartbeat: %w", err)
	}
	generation.stopBeat = generation.lifecycle.StartHeartbeatLoop(generation.ctx)
	s.mu.Lock()
	s.executing = true
	s.mu.Unlock()
	return writeRuntimeState(s.configDir, "running")
}

func (s *NativeRuntimeSupervisor) Stop(ctx context.Context) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.mu.RLock()
	generation := s.generation
	s.mu.RUnlock()
	if generation == nil {
		return writeRuntimeState(s.configDir, "stopped")
	}
	generation.store.StopAll()
	if generation.stopBeat != nil {
		generation.stopBeat()
		generation.stopBeat = nil
	}
	if err := generation.lifecycle.Pause(ctx, "dashboard_stop"); err != nil {
		return fmt.Errorf("pause runner lifecycle: %w", err)
	}
	if err := generation.stopEdge(ctx); err != nil {
		return fmt.Errorf("stop native edge: %w", err)
	}
	s.mu.Lock()
	s.executing = false
	s.mu.Unlock()
	return writeRuntimeState(s.configDir, "stopped")
}

func (s *NativeRuntimeSupervisor) CurrentPublicURL(ctx context.Context) (string, error) {
	s.mu.RLock()
	generation := s.generation
	s.mu.RUnlock()
	if generation == nil {
		return "", fmt.Errorf("native runtime generation is unavailable")
	}
	return generation.edge.QuickTunnelURL(ctx)
}

func (s *NativeRuntimeSupervisor) VerifyPublicURL(ctx context.Context, publicURL string) error {
	s.mu.RLock()
	generation := s.generation
	s.mu.RUnlock()
	if generation == nil {
		return fmt.Errorf("native runtime generation is unavailable")
	}
	return generation.edge.VerifyPublicURL(ctx, publicURL)
}

func (s *NativeRuntimeSupervisor) Status(ctx context.Context) dashboardruntime.RuntimeStatus {
	s.mu.RLock()
	generation := s.generation
	executing := s.executing
	s.mu.RUnlock()
	if generation == nil {
		return dashboardruntime.RuntimeStatus{Configured: false}
	}
	status := generation.edge.Status(ctx)
	status.Configured = true
	status.RunnerRunning = executing
	if !executing {
		status.PublicURL = ""
	}
	return status
}

func (s *NativeRuntimeSupervisor) Close(ctx context.Context) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.mu.Lock()
	generation := s.generation
	wasExecuting := s.executing
	s.generation = nil
	s.executing = false
	s.mu.Unlock()
	if generation == nil {
		return nil
	}
	return generation.close(ctx, wasExecuting)
}

func newNativeRuntimeGeneration(parent context.Context, configDir string, values dashboardruntime.Values) (*nativeRuntimeGeneration, error) {
	parent = cluelog.Context(parent, cluelog.WithFormat(cluelog.FormatJSON))
	if err := hydrateTypedRuntimeEnvironment(configDir); err != nil {
		return nil, fmt.Errorf("hydrate runtime compatibility values: %w", err)
	}
	cfg, err := runnerconfig.LoadFile(configDir + "/config.toml")
	if err != nil {
		return nil, fmt.Errorf("load typed runtime config: %w", err)
	}
	if err := androidtools.ConnectConfiguredWiFiDevices(parent, cfg); err != nil {
		return nil, fmt.Errorf("connect configured Wi-Fi devices: %w", err)
	}
	generationCtx, cancel := context.WithCancel(parent)
	otelShutdown, err := observability.Setup(generationCtx, observability.ConfigFromEnv())
	if err != nil {
		cancel()
		return nil, fmt.Errorf("setup generation observability: %w", err)
	}
	listen := runnerAPIListen(cfg.Server.APIListen)
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		cancel()
		_ = otelShutdown(context.Background())
		return nil, fmt.Errorf("listen on %s: %w", listen, err)
	}
	store := server.NewProcessStore()
	instance := utils.LoadInstance()
	service := server.NewRunnerService(store, instance)
	lifecycle := server.NewRunnerLifecycleClient(server.LoadRunnerLifecycleConfig(instance), http.DefaultClient, store)
	handler := observability.WrapHandler(server.NewHTTPHandler(generationCtx, service, debug), "credimi-runner.http")
	httpServer := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Duration()}
	if httpServer.ReadHeaderTimeout <= 0 {
		httpServer.ReadHeaderTimeout = 60 * time.Second
	}
	generation := &nativeRuntimeGeneration{
		values: values, ctx: generationCtx, cancel: cancel, listener: listener, http: httpServer,
		store: store, service: service, lifecycle: lifecycle,
		edge:     dashboardruntime.NewLifecycleManagerForOS("", configDir, values, nil, "darwin"),
		stopBeat: func() {}, shutdownOTEL: otelShutdown,
	}
	go func() { _ = httpServer.Serve(listener) }()
	return generation, nil
}

func runnerAPIListen(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "0.0.0.0:8050"
	}
	if strings.TrimSpace(host) == "" || host == "127.0.0.1" || host == "::1" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, port)
}

func nativeRuntimeValues(configDir string) (dashboardruntime.Values, error) {
	store, err := dashboardruntime.LoadStore(configDir)
	if err != nil {
		return nil, err
	}
	if !store.Exists() {
		return nil, fmt.Errorf("native runtime configuration is unavailable")
	}
	return dashboardruntime.NormalizeValues(store.Snapshot(), "darwin")
}

func (g *nativeRuntimeGeneration) startEdge(ctx context.Context) error {
	if err := g.edge.Start(ctx); err != nil {
		return err
	}
	return nil
}

func (g *nativeRuntimeGeneration) stopEdge(ctx context.Context) error {
	return g.edge.Stop(ctx)
}

func (g *nativeRuntimeGeneration) close(ctx context.Context, pause bool) error {
	var result error
	g.stopOnce.Do(func() {
		g.store.StopAll()
		g.stopBeat()
		g.cancel()
		if pause {
			if err := g.lifecycle.Pause(ctx, "generation_replaced"); err != nil {
				result = fmt.Errorf("pause generation lifecycle: %w", err)
			}
		}
		if err := g.stopEdge(ctx); err != nil && result == nil {
			result = fmt.Errorf("stop generation edge: %w", err)
		}
		if err := g.edge.Close(); err != nil && result == nil {
			result = fmt.Errorf("close generation edge: %w", err)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := g.http.Shutdown(shutdownCtx); err != nil && result == nil {
			result = fmt.Errorf("shutdown generation HTTP server: %w", err)
		}
		if err := g.listener.Close(); err != nil && !strings.Contains(err.Error(), "use of closed network connection") && result == nil {
			result = fmt.Errorf("close generation listener: %w", err)
		}
		if g.shutdownOTEL != nil {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer shutdownCancel()
			if err := g.shutdownOTEL(shutdownCtx); err != nil && result == nil {
				result = fmt.Errorf("shutdown generation observability: %w", err)
			}
		}
	})
	return result
}
