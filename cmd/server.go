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

	"github.com/forkbombeu/credimi-runner/pkg/server"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
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
		// Load env (optional)
		if err := godotenv.Load(); err != nil {
			stdlog.Println("No .env file found, using environment variables")
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

		store := server.NewProcessStore()
		instances := utils.LoadInstances()
		srv := server.NewRunnerService(store, instances)

		if err := srv.StartExistingWorkers(); err != nil {
			cluelog.Printf(ctx, "Warning: failed to start some existing workers: %v", err)
		}

		// Build HTTP handler (Goa mux + middleware + debug endpoints)
		handler := server.NewHTTPHandler(ctx, srv, debug)

		addr := fmt.Sprintf("%s:%d", host, port)
		httpSrv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 60 * time.Second,
		}

		// Run server
		errc := make(chan error, 1)
		go func() {
			cluelog.Printf(ctx, "HTTP server listening on %q", addr)
			errc <- httpSrv.ListenAndServe()
		}()

		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

		select {
		case sig := <-sigc:
			cluelog.Printf(ctx, "exiting (signal: %v)", sig)
		case err := <-errc:
			// If server failed immediately
			return err
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cluelog.Printf(ctx, "shutting down HTTP server at %q", addr)
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			cluelog.Printf(ctx, "failed to shutdown: %v", err)
			return err
		}

		cluelog.Printf(ctx, "exited")
		return nil
	},
}

func init() {
	serverCmd.Flags().StringVar(&host, "host", "127.0.0.1", "Listen host")
	serverCmd.Flags().IntVar(&port, "port", 8050, "Listen port")
	serverCmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging and /debug endpoints")
	rootCmd.AddCommand(serverCmd)
}
