package cmd

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/spf13/cobra"
)

var (
	dashboardHost      string
	dashboardPort      int
	dashboardConfigDir string
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Start Credimi Runner dashboard supervisor",
	RunE: func(cmd *cobra.Command, args []string) error {
		configDir := dashboardConfigDir
		if configDir == "" {
			configDir = dashboard.ConfigDir()
		}
		if err := os.Setenv("CREDIMI_RUNNER_CONFIG_DIR", configDir); err != nil {
			return err
		}

		store, err := dashboardruntime.LoadStore(configDir)
		if err != nil {
			return err
		}
		binaryPath, err := os.Executable()
		if err != nil {
			return err
		}
		values, err := dashboardruntime.NormalizeValues(store.Snapshot(), runtime.GOOS)
		if err != nil {
			return err
		}
		listenHost, listenPort := resolveDashboardListenAddress(cmd, values)
		if err := validateDashboardSecurity(listenHost, values); err != nil {
			return err
		}
		manager := dashboardruntime.NewLifecycleManager(binaryPath, configDir, values, nil)

		handler, cancel, err := dashboard.NewHandlerWithManager(configDir, manager)
		if err != nil {
			return err
		}
		defer cancel()

		if configFileExists(configDir) {
			if err := startDashboardRuntime(cmd.Context(), manager, store.Snapshot()); err != nil {
				stdlog.Printf("dashboard runtime start failed: %v", err)
			}
		}

		server := &http.Server{
			Addr:              fmt.Sprintf("%s:%d", listenHost, listenPort),
			Handler:           handler,
			ReadHeaderTimeout: 60 * time.Second,
		}

		errc := make(chan error, 1)
		go func() {
			stdlog.Printf("Credimi Runner dashboard available at http://%s:%d", listenHost, listenPort)
			errc <- server.ListenAndServe()
		}()

		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(sigc)

		select {
		case err := <-errc:
			if err != nil && err != http.ErrServerClosed {
				return err
			}
		case <-sigc:
		}

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if configFileExists(configDir) || manager.Status(context.Background()).RunnerRunning || manager.Status(context.Background()).ComposeRunning {
			if err := manager.Down(shutdownCtx); err != nil {
				stdlog.Printf("dashboard runtime shutdown failed: %v", err)
			}
		}
		return nil
	},
}

func init() {
	dashboardCmd.Flags().StringVar(&dashboardHost, "host", "127.0.0.1", "Dashboard listen host")
	dashboardCmd.Flags().IntVar(&dashboardPort, "port", 8051, "Dashboard listen port")
	dashboardCmd.Flags().StringVar(&dashboardConfigDir, "config-dir", "", "Dashboard config directory")
	dashboardCmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging")
	rootCmd.AddCommand(dashboardCmd)
}

func dashboardEnvPath(configDir string) string {
	return filepath.Join(configDir, ".env")
}

func configFileExists(configDir string) bool {
	_, err := os.Stat(dashboardEnvPath(configDir))
	return err == nil
}

func resolveDashboardListenAddress(cmd *cobra.Command, values dashboardruntime.Values) (string, int) {
	host := strings.TrimSpace(values["DASHBOARD_HOST"])
	if host == "" {
		host = dashboardruntime.DefaultDashboardHost
	}
	portValue := strings.TrimSpace(values["DASHBOARD_PORT"])
	port := dashboardruntime.DefaultDashboardPort
	if portValue != "" {
		port = portValue
	}
	if flag := cmd.Flags().Lookup("host"); flag != nil && flag.Changed {
		host = dashboardHost
	}
	if flag := cmd.Flags().Lookup("port"); flag != nil && flag.Changed {
		return host, dashboardPort
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil {
		return host, dashboardPort
	}
	return host, parsedPort
}

func validateDashboardSecurity(host string, values dashboardruntime.Values) error {
	if isLocalDashboardHost(host) {
		return nil
	}
	if strings.TrimSpace(values["DASHBOARD_TOKEN"]) != "" {
		return nil
	}
	return fmt.Errorf("DASHBOARD_TOKEN is required when dashboard host %q is not localhost", host)
}

func isLocalDashboardHost(host string) bool {
	switch strings.TrimSpace(strings.ToLower(host)) {
	case "", "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func startDashboardRuntime(ctx context.Context, manager dashboardruntime.Manager, values dashboardruntime.Values) error {
	if err := manager.Start(ctx); err != nil {
		return err
	}
	if err := waitForDashboardRunnerReady(ctx, values); err != nil {
		return err
	}
	if strings.TrimSpace(values["CREDIMI_RUNNER_ID"]) == "" {
		return nil
	}
	return registerDashboardRunner(ctx, manager, values)
}

func waitForDashboardRunnerReady(ctx context.Context, values dashboardruntime.Values) error {
	host := strings.TrimSpace(values["RUNNER_HOST"])
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := strings.TrimSpace(values["RUNNER_PORT"])
	if port == "" {
		port = dashboardruntime.DefaultRunnerPort
	}
	endpoint := "http://" + net.JoinHostPort(host, port) + "/healthz"
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(deadline, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("runner did not become ready at %s: %w", endpoint, deadline.Err())
		case <-ticker.C:
		}
	}
}

func registerDashboardRunner(ctx context.Context, manager dashboardruntime.Manager, values dashboardruntime.Values) error {
	apiKey := strings.TrimSpace(values["CREDIMI_USER_API_KEY"])
	if apiKey == "" {
		apiKey = strings.TrimSpace(values["CREDIMI_INTERNAL_ADMIN_KEY"])
	}
	if apiKey == "" {
		return errors.New("missing Credimi API key")
	}
	publicURL, publicPort, err := resolveDashboardRegistrationEndpoint(ctx, manager, values)
	if err != nil {
		return err
	}
	client := &dashboardruntime.CredimiClient{
		BaseURL:    strings.TrimSpace(values["CREDIMI_URL"]),
		APIKey:     apiKey,
		HTTPClient: http.DefaultClient,
	}
	return client.RegisterMobileRunner(ctx, dashboardruntime.RegisterRunnerRequest{
		RunnerID:     strings.TrimSpace(values["CREDIMI_RUNNER_ID"]),
		Name:         strings.TrimSpace(values["CREDIMI_RUNNER_NAME"]),
		IP:           publicURL,
		Description:  strings.TrimSpace(values["CREDIMI_RUNNER_DESCRIPTION"]),
		Type:         strings.TrimSpace(values["CREDIMI_RUNNER_TYPE"]),
		Port:         publicPort,
		Serial:       strings.TrimSpace(values["CREDIMI_RUNNER_SERIAL"]),
		Organization: strings.TrimSpace(values["CREDIMI_RUNNER_ORGANIZATION"]),
	})
}

func resolveDashboardRegistrationEndpoint(ctx context.Context, manager dashboardruntime.Manager, values dashboardruntime.Values) (string, string, error) {
	switch strings.TrimSpace(values["CREDIMI_SERVICE_MODE"]) {
	case "manual":
		publicURL := strings.TrimSpace(values["RUNNER_PUBLIC_URL"])
		if publicURL == "" {
			return "", "", errors.New("RUNNER_PUBLIC_URL is required for manual service mode")
		}
		return publicURL, strings.TrimSpace(values["RUNNER_PUBLIC_PORT"]), nil
	case "cloudflare-managed":
		domain := strings.TrimSpace(values["RUNNER_DOMAIN"])
		if domain == "" {
			return "", "", errors.New("RUNNER_DOMAIN is required for managed tunnel mode")
		}
		if !strings.Contains(domain, "://") {
			domain = "https://" + domain
		}
		return domain, "", nil
	default:
		re := regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.trycloudflare\.com`)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var lastErr error
		for {
			logs, err := manager.Logs(deadline, 200)
			if err != nil {
				lastErr = err
			} else {
				for i := len(logs) - 1; i >= 0; i-- {
					if found := re.FindString(logs[i].Message); found != "" {
						return found, "", nil
					}
				}
				lastErr = errors.New("no trycloudflare URL found in runtime logs")
			}
			select {
			case <-deadline.Done():
				return "", "", lastErr
			case <-ticker.C:
			}
		}
	}
}
