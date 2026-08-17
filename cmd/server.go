package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/observability"
	"github.com/forkbombeu/credimi-runner/pkg/server"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	cluelog "goa.design/clue/log"
)

var (
	host                  string
	port                  int
	debug                 bool
	serverSignalReadyHook = func() {}
)

var serverCmd = &cobra.Command{
	Use:    "serve",
	Short:  "Start HTTP server to control credimi mobile runner",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := setRunnerBootID(); err != nil {
			return err
		}

		format := cluelog.FormatJSON
		if cluelog.IsTerminal() {
			format = cluelog.FormatTerminal
		}
		ctx := cluelog.Context(context.Background(), cluelog.WithFormat(format))
		if debug {
			ctx = cluelog.Context(ctx, cluelog.WithDebug())
			cluelog.Debugf(ctx, "debug logs enabled")
		}

		otelCfg := observability.ConfigFromEnv()
		otelShutdown, err := observability.Setup(ctx, otelCfg)
		if err != nil {
			return fmt.Errorf("failed to setup opentelemetry: %w", err)
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := otelShutdown(shutdownCtx); err != nil {
				cluelog.Printf(ctx, "failed to shutdown opentelemetry: %v", err)
			}
		}()

		serveCtx, serveSpan := observability.Tracer("credimi-runner.lifecycle").Start(ctx, "credimi_runner.serve",
			trace.WithAttributes(
				attribute.String("listen.host", host),
				attribute.Int("listen.port", port),
				attribute.String("service.name", otelCfg.ServiceName),
				attribute.String("service.instance.id", otelCfg.InstanceID),
			),
		)
		defer serveSpan.End()

		observability.RecordRunnerStart(serveCtx,
			attribute.String("service.name", otelCfg.ServiceName),
			attribute.String("service.instance.id", otelCfg.InstanceID),
			attribute.String("runner_id", otelCfg.RunnerID),
		)
		observability.Info(serveCtx, "credimi-runner.lifecycle", "credimi-runner process started",
			observability.String("listen.host", host),
			observability.Int64("listen.port", int64(port)),
			observability.String("service.name", otelCfg.ServiceName),
			observability.String("service.instance.id", otelCfg.InstanceID),
			observability.String("runner_id", otelCfg.RunnerID),
			observability.String("deployment.environment", otelCfg.Environment),
		)

		http.DefaultClient = observability.NewHTTPClient(http.DefaultClient)

		store := server.NewProcessStore()
		configDir := os.Getenv("CREDIMI_RUNNER_CONFIG_DIR")
		if configDir != "" {
			if err := hydrateTypedRuntimeEnvironment(configDir); err != nil {
				return err
			}
		}
		instance := utils.LoadInstance()
		srv := server.NewRunnerService(store, instance)
		lifecycleCfg := server.LoadRunnerLifecycleConfig(instance)
		lifecycleClient := server.NewRunnerLifecycleClient(lifecycleCfg, http.DefaultClient, store)
		heartbeatCtx, stopHeartbeat := context.WithCancel(serveCtx)
		defer stopHeartbeat()
		var heartbeatOnce sync.Once
		startHeartbeatLoop := func() {
			heartbeatOnce.Do(func() {
				_ = lifecycleClient.StartHeartbeatLoop(heartbeatCtx)
			})
		}
		stopRuntimeControls := startRuntimeControlLoop(serveCtx, configDir, srv, lifecycleClient, store, startHeartbeatLoop)
		defer stopRuntimeControls()

		// Bind before performing remote startup work. Worker recovery and the
		// lifecycle resume request depend on external services and must not
		// prevent the local runner API from becoming reachable.
		handler := observability.WrapHandler(server.NewHTTPHandler(serveCtx, srv, debug), "credimi-runner.http")
		addr := fmt.Sprintf("%s:%d", host, port)
		httpSrv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 60 * time.Second,
		}
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			serveSpan.RecordError(err)
			serveSpan.SetStatus(codes.Error, "http server failed")
			return err
		}
		errc := make(chan error, 1)
		go func() {
			cluelog.Printf(serveCtx, "HTTP server listening on %q", addr)
			observability.Info(serveCtx, "credimi-runner.lifecycle", "http server listening",
				observability.String("address", addr),
			)
			err := httpSrv.Serve(listener)
			if err != nil && err != http.ErrServerClosed {
				errc <- err
			}
		}()
		serverSignalReadyHook()

		// The Dashboard is the sole registration owner. It verifies the runner,
		// registers the runner and devices, then writes a runtime-control command
		// to start execution. Starting workers or sending lifecycle calls here
		// would race that registration and can advertise a runner before Credimi
		// knows about it.
		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(sigc)

		select {
		case sig := <-sigc:
			cluelog.Printf(serveCtx, "exiting (signal: %v)", sig)
			observability.Info(serveCtx, "credimi-runner.lifecycle", "runner received shutdown signal",
				observability.String("signal", sig.String()),
			)
		case err := <-errc:
			// If server failed immediately
			serveSpan.RecordError(err)
			serveSpan.SetStatus(codes.Error, "http server failed")
			observability.Error(serveCtx, "credimi-runner.lifecycle", "http server failed", err,
				observability.String("address", addr),
			)
			return err
		}

		stopHeartbeat()

		pauseCtx, pauseCancel := context.WithTimeout(context.Background(), lifecycleCfg.RequestTimeout)
		defer pauseCancel()
		if err := lifecycleClient.Pause(pauseCtx, "runner_shutdown"); err != nil {
			cluelog.Printf(serveCtx, "Warning: failed to send runner lifecycle pause: %v", err)
			observability.Error(serveCtx, "credimi-runner.lifecycle", "failed to send runner lifecycle pause", err)
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cluelog.Printf(serveCtx, "shutting down HTTP server at %q", addr)
		observability.Info(serveCtx, "credimi-runner.lifecycle", "http server shutting down",
			observability.String("address", addr),
		)
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			serveSpan.RecordError(err)
			serveSpan.SetStatus(codes.Error, "http shutdown failed")
			cluelog.Printf(serveCtx, "failed to shutdown: %v", err)
			observability.Error(serveCtx, "credimi-runner.lifecycle", "http server shutdown failed", err,
				observability.String("address", addr),
			)
			return err
		}

		cluelog.Printf(serveCtx, "exited")
		observability.Info(serveCtx, "credimi-runner.lifecycle", "credimi-runner process exited",
			observability.String("address", addr),
		)
		return nil
	},
}

func startRuntimeControlLoop(ctx context.Context, configDir string, srv interface {
	StartExistingWorkers(context.Context) error
}, lifecycle interface {
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	Heartbeat(context.Context) error
}, store interface{ StopAll() }, onRuntimeReady func()) func() {
	controlCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-controlCtx.Done():
				return
			case <-ticker.C:
				raw, err := os.ReadFile(filepath.Join(configDir, "runtime-control"))
				if err != nil {
					continue
				}
				_ = os.Remove(filepath.Join(configDir, "runtime-control"))
				switch strings.TrimSpace(string(raw)) {
				case "setup-ready", "registration-ready":
					if err := srv.StartExistingWorkers(controlCtx); err != nil {
						_ = writeRuntimeState(configDir, "failed: "+err.Error())
						continue
					}
					if err := lifecycle.Resume(controlCtx, "setup"); err != nil {
						cluelog.Printf(controlCtx, "Warning: failed to resume runner lifecycle during startup: %v", err)
					}
					if err := lifecycle.Heartbeat(controlCtx); err != nil {
						cluelog.Printf(controlCtx, "Warning: failed to send runner heartbeat during startup: %v", err)
					}
					_ = writeRuntimeState(configDir, "running")
					if onRuntimeReady != nil {
						onRuntimeReady()
					}
				case "stop":
					store.StopAll()
					_ = lifecycle.Pause(controlCtx, "dashboard_stop")
					_ = writeRuntimeState(configDir, "stopped")
				case "start":
					if err := srv.StartExistingWorkers(controlCtx); err == nil {
						if err := lifecycle.Resume(controlCtx, "dashboard_start"); err != nil {
							cluelog.Printf(controlCtx, "Warning: failed to resume runner lifecycle during start: %v", err)
						}
						if err := lifecycle.Heartbeat(controlCtx); err != nil {
							cluelog.Printf(controlCtx, "Warning: failed to send runner heartbeat during start: %v", err)
						}
						_ = writeRuntimeState(configDir, "running")
						if onRuntimeReady != nil {
							onRuntimeReady()
						}
					} else {
						_ = writeRuntimeState(configDir, "failed: "+err.Error())
					}
				case "restart":
					_ = writeRuntimeState(configDir, "restarting")
					store.StopAll()
					_ = lifecycle.Pause(controlCtx, "dashboard_restart")
					if err := srv.StartExistingWorkers(controlCtx); err == nil {
						if err := lifecycle.Resume(controlCtx, "dashboard_restart"); err != nil {
							cluelog.Printf(controlCtx, "Warning: failed to resume runner lifecycle during restart: %v", err)
						}
						if err := lifecycle.Heartbeat(controlCtx); err != nil {
							cluelog.Printf(controlCtx, "Warning: failed to send runner heartbeat during restart: %v", err)
						}
						_ = writeRuntimeState(configDir, "running")
						if onRuntimeReady != nil {
							onRuntimeReady()
						}
					} else {
						_ = writeRuntimeState(configDir, "failed: "+err.Error())
					}
				}
			}
		}
	}()
	return cancel
}

func writeRuntimeState(configDir, state string) error {
	if strings.TrimSpace(configDir) == "" {
		return nil
	}
	sequence := int64(0)
	if raw, err := os.ReadFile(filepath.Join(configDir, "runtime-state-sequence")); err == nil {
		sequence, _ = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	}
	sequence++
	if err := os.WriteFile(filepath.Join(configDir, "runtime-state-sequence"), []byte(strconv.FormatInt(sequence, 10)+"\n"), 0o600); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(configDir, ".runtime-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(strings.TrimSpace(state) + "\n"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(configDir, "runtime-state"))
}

func setRunnerBootID() error {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Errorf("generate runner boot ID: %w", err)
	}
	return os.Setenv("CREDIMI_RUNNER_BOOT_ID", hex.EncodeToString(bytes))
}

func init() {
	serverCmd.Flags().StringVar(&host, "host", "127.0.0.1", "Listen host")
	serverCmd.Flags().IntVar(&port, "port", 8050, "Listen port")
	serverCmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging and /debug endpoints")
	rootCmd.AddCommand(serverCmd)
}
