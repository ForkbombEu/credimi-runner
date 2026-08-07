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
	"syscall"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
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
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
		instance := utils.LoadInstance()
		srv := server.NewRunnerService(store, instance)
		lifecycleCfg := server.LoadRunnerLifecycleConfig(instance)
		lifecycleClient := server.NewRunnerLifecycleClient(lifecycleCfg, http.DefaultClient, store)

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

		if err := srv.StartExistingWorkers(serveCtx); err != nil {
			serveSpan.RecordError(err)
			serveSpan.SetStatus(codes.Error, "start existing workers failed")
			cluelog.Printf(serveCtx, "Warning: failed to start some existing workers: %v", err)
			observability.Error(serveCtx, "credimi-runner.lifecycle", "failed to start existing workers", err)
		}

		if err := lifecycleClient.Resume(serveCtx, "runner_startup"); err != nil {
			cluelog.Printf(serveCtx, "Warning: failed to send runner lifecycle resume: %v", err)
			observability.Error(serveCtx, "credimi-runner.lifecycle", "failed to send runner lifecycle resume", err)
		}
		// `serve` may be started directly by Compose, Coolify, or the CLI rather
		// than through the dashboard lifecycle controller. Ensure every indexed
		// child exists before the first heartbeat reports its state to Credimi.
		if err := registerConfiguredDevices(serveCtx); err != nil {
			cluelog.Printf(serveCtx, "Warning: failed to register configured devices: %v", err)
			observability.Error(serveCtx, "credimi-runner.lifecycle", "failed to register configured devices", err)
		}
		// Do not leave a newly started runner and its devices offline until the
		// first periodic tick (normally 30 seconds). Resume records host state;
		// this immediate heartbeat records the per-device readiness inventory.
		if err := lifecycleClient.Heartbeat(serveCtx); err != nil {
			cluelog.Printf(serveCtx, "Warning: failed to send initial runner heartbeat: %v", err)
			observability.Error(serveCtx, "credimi-runner.lifecycle", "failed to send initial runner heartbeat", err)
		}

		heartbeatCtx, stopHeartbeat := context.WithCancel(serveCtx)
		defer stopHeartbeat()
		stopHeartbeatLoop := lifecycleClient.StartHeartbeatLoop(heartbeatCtx)
		defer stopHeartbeatLoop()

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
		stopHeartbeatLoop()

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

func registerConfiguredDevices(ctx context.Context) error {
	config, err := dashboardruntime.RuntimeConfigFromEnvironment()
	if err != nil {
		return err
	}
	apiKey := config.Host["CREDIMI_USER_API_KEY"]
	if apiKey == "" {
		apiKey = config.Host["CREDIMI_INTERNAL_ADMIN_KEY"]
	}
	if apiKey == "" || config.Host["CREDIMI_URL"] == "" {
		return fmt.Errorf("Credimi URL and API key are required to register devices")
	}
	client := &dashboardruntime.CredimiClient{BaseURL: config.Host["CREDIMI_URL"], APIKey: apiKey, HTTPClient: http.DefaultClient}
	for _, device := range config.Devices {
		if err := client.RegisterMobileDevice(ctx, dashboardruntime.RegisterDeviceRequest{
			Organization: config.Host["CREDIMI_RUNNER_ORGANIZATION"],
			RunnerID:     config.Host["CREDIMI_RUNNER_ID"],
			DeviceID:     device.ID,
			Name:         device.Name,
			Description:  device.Description,
			Type:         device.Type,
			Serial:       device.Serial,
		}); err != nil {
			return fmt.Errorf("register device %q: %w", device.ID, err)
		}
	}
	return nil
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
