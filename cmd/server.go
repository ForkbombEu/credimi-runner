package cmd

import (
	"context"
	"fmt"
	stdlog "log"
	"net/http"
	"os"
	"os/signal"
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
	host  string
	port  int
	debug bool
)

var serverCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start HTTP server to control  credimi mobile runner",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load env from the working directory first, then from the XDG config dir.
		envPath, err := loadDotEnv()
		if err != nil {
			stdlog.Printf("Failed to load .env file: %v", err)
		} else if envPath == "" {
			stdlog.Println("No .env file found in current directory or config directory, using environment variables")
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
		instances := utils.LoadInstances()
		srv := server.NewRunnerService(store, instances)

		if err := srv.StartExistingWorkers(serveCtx); err != nil {
			serveSpan.RecordError(err)
			serveSpan.SetStatus(codes.Error, "start existing workers failed")
			cluelog.Printf(serveCtx, "Warning: failed to start some existing workers: %v", err)
			observability.Error(serveCtx, "credimi-runner.lifecycle", "failed to start existing workers", err)
		}

		// Build HTTP handler (Goa mux + middleware + debug endpoints)
		handler := observability.WrapHandler(server.NewHTTPHandler(serveCtx, srv, debug), "credimi-runner.http")

		addr := fmt.Sprintf("%s:%d", host, port)
		httpSrv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 60 * time.Second,
		}

		// Run server
		errc := make(chan error, 1)
		go func() {
			cluelog.Printf(serveCtx, "HTTP server listening on %q", addr)
			observability.Info(serveCtx, "credimi-runner.lifecycle", "http server listening",
				observability.String("address", addr),
			)
			errc <- httpSrv.ListenAndServe()
		}()

		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

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

func init() {
	serverCmd.Flags().StringVar(&host, "host", "127.0.0.1", "Listen host")
	serverCmd.Flags().IntVar(&port, "port", 8050, "Listen port")
	serverCmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging and /debug endpoints")
	rootCmd.AddCommand(serverCmd)
}
