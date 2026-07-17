package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	controller "github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/lifecyclelog"
	"github.com/spf13/cobra"
)

var (
	dashboardHost                string
	dashboardPort                int
	dashboardConfigDir           string
	dashboardOpen                bool
	dashboardRegistrationTimeout = 30 * time.Second
)

const quickTunnelLogTail = 1000

type dashboardTunnelLogger interface {
	TunnelLogs(context.Context, int) ([]dashboardruntime.LogLine, error)
}

type dashboardProgressStarter interface {
	StartWithProgress(context.Context, func(string)) error
}

var openDashboardBrowserFunc = openDashboardBrowser

func runDashboard(cmd *cobra.Command, args []string) error {
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
	lease, err := controller.Acquire(configDir)
	if err != nil {
		if errors.Is(err, controller.ErrAlreadyRunning) {
			return reopenExistingDashboard(cmd, configDir)
		}
		return err
	}
	defer lease.Close()
	listenHost, listenPort := resolveDashboardListenAddress(cmd, values)
	listener, err := reserveDashboardListener(listenHost, listenPort)
	if err != nil {
		return err
	}
	defer listener.Close()
	manager := dashboardruntime.NewLifecycleManager(binaryPath, configDir, values, nil)
	defer manager.Close()

	dashboardCtx, cancelDashboard := context.WithCancel(context.Background())
	defer cancelDashboard()
	handler, cancelHandler, err := dashboard.NewHandlerWithManagerContext(dashboardCtx, configDir, manager)
	if err != nil {
		return err
	}
	defer cancelHandler()
	if err := lease.Publish(controller.Metadata{
		ControllerID: fmt.Sprintf("controller-%d", time.Now().UnixNano()),
		PID:          os.Getpid(),
		ConfigDir:    configDir,
		ListenHost:   listenHost,
		ListenPort:   listenPort,
		ProbeURL:     fmt.Sprintf("http://127.0.0.1:%d/healthz", listenPort),
		PublicURL:    dashboardBrowserURL(listenHost, listenPort),
	}); err != nil {
		return err
	}
	manager.EmitLifecycle(lifecyclelog.Event{Level: lifecyclelog.LevelInfo, Event: "controller.started", Message: "dashboard controller started", Component: "controller", Phase: "running", Fields: map[string]any{"pid": os.Getpid(), "listen_host": listenHost, "listen_port": listenPort}})

	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", listenHost, listenPort),
		Handler:           handler,
		ReadHeaderTimeout: 60 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		stdlog.Printf("Credimi Runner dashboard available at http://%s:%d", listenHost, listenPort)
		errc <- server.Serve(listener)
	}()
	if configFileExists(configDir) {
		progress := func(line string) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "runner startup: %s\n", line)
		}
		go func() {
			if err := startDashboardRuntimeWithProgress(dashboardCtx, manager, values, progress); err != nil {
				stdlog.Printf("dashboard runtime start failed: %v", err)
			}
		}()
	}
	if dashboardOpen {
		go func() {
			time.Sleep(250 * time.Millisecond)
			if err := openDashboardBrowserFunc(dashboardBrowserURL(listenHost, listenPort)); err != nil {
				stdlog.Printf("dashboard browser open skipped: %v", err)
			}
		}()
	}

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

	cancelDashboard()
	manager.EmitLifecycle(lifecyclelog.Event{Level: lifecyclelog.LevelInfo, Event: "controller.stopped", Message: "dashboard controller stopped", Component: "controller", Phase: "stopped"})
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		stdlog.Printf("dashboard HTTP shutdown did not complete cleanly: %v", err)
		if closeErr := server.Close(); closeErr != nil && closeErr != http.ErrServerClosed {
			stdlog.Printf("dashboard HTTP close failed: %v", closeErr)
		}
	}
	return nil
}

func reopenExistingDashboard(cmd *cobra.Command, configDir string) error {
	metadata, err := controller.ReadMetadata(configDir)
	if err != nil {
		return fmt.Errorf("dashboard is already locked but its metadata is unavailable: %w", err)
	}
	url := metadata.PublicURL
	if strings.TrimSpace(url) == "" {
		url = dashboardBrowserURL(metadata.ListenHost, metadata.ListenPort)
	}
	if cmd != nil {
		cmd.Printf("Credimi Runner dashboard is already running at %s\n", url)
	}
	if dashboardOpen {
		if err := openDashboardBrowserFunc(url); err != nil {
			stdlog.Printf("dashboard browser open skipped: %v", err)
		}
	}
	return nil
}

func reserveDashboardListener(host string, port int) (net.Listener, error) {
	address := net.JoinHostPort(strings.Trim(strings.TrimSpace(host), "[]"), strconv.Itoa(port))
	listener, err := net.Listen("tcp", address)
	if err == nil {
		return listener, nil
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return nil, fmt.Errorf("dashboard cannot start because port %d on %s is already in use; find the process with `lsof -nP -iTCP:%d -sTCP:LISTEN` or `ss -ltnp 'sport = :%d'`, then stop it with `kill <PID>`; alternatively choose another port with `credimi-runner --port <PORT>`", port, host, port, port)
	}
	return nil, fmt.Errorf("listen for dashboard on %s: %w", address, err)
}

func init() {
	rootCmd.Flags().StringVar(&dashboardHost, "host", dashboardruntime.DefaultDashboardHost, "Dashboard listen host")
	rootCmd.Flags().IntVar(&dashboardPort, "port", 8051, "Dashboard listen port")
	rootCmd.Flags().StringVar(&dashboardConfigDir, "config-dir", "", "Dashboard config directory")
	rootCmd.Flags().BoolVar(&dashboardOpen, "open-browser", true, "Open the dashboard in a browser after startup")
	rootCmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging")
}

func dashboardBrowserURL(host string, port int) string {
	switch strings.TrimSpace(host) {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

func openDashboardBrowser(url string) error {
	if strings.TrimSpace(url) == "" {
		return errors.New("dashboard URL is empty")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
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

func startDashboardRuntime(ctx context.Context, manager dashboardruntime.Manager, values dashboardruntime.Values) error {
	return startDashboardRuntimeWithProgress(ctx, manager, values, func(line string) {
		stdlog.Printf("runner startup: %s", line)
	})
}

func startDashboardRuntimeWithProgress(ctx context.Context, manager dashboardruntime.Manager, values dashboardruntime.Values, progress func(string)) error {
	if strings.EqualFold(strings.TrimSpace(values["CREDIMI_SERVICE_MODE"]), "auto") {
		manager.SetPublicURL("")
	}
	var err error
	if starter, ok := manager.(dashboardProgressStarter); ok {
		err = starter.StartWithProgress(ctx, progress)
	} else {
		err = manager.Start(ctx)
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(values["CREDIMI_RUNNER_ID"]) == "" {
		return nil
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, 2*time.Second)
	defer readyCancel()
	if err := waitForDashboardRunnerReady(readyCtx, values); err != nil {
		stdlog.Printf("dashboard runtime start pending: %v\n%s", err, runtimeStartupDiagnostics(ctx, manager, values))
		return nil
	}
	registerCtx, registerCancel := context.WithTimeout(ctx, dashboardRegistrationTimeout)
	defer registerCancel()
	if err := registerDashboardRunner(registerCtx, manager, values); err != nil {
		stdlog.Printf("dashboard runtime registration pending: %v", err)
	}
	return nil
}

func waitForDashboardRunnerReady(ctx context.Context, values dashboardruntime.Values) error {
	if !dashboardruntime.RunnerReadinessRequiredBeforeRegistration(values, currentDashboardGOOS()) {
		return nil
	}
	host := strings.TrimSpace(values["RUNNER_HOST"])
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := strings.TrimSpace(values["RUNNER_PORT"])
	if port == "" {
		port = dashboardruntime.DefaultRunnerPort
	}
	address := net.JoinHostPort(host, port)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var lastErr error
	for {
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(deadline, "tcp", address)
		if err == nil {
			_ = conn.Close()
			if healthErr := waitForDashboardRunnerHealth(deadline, host, port, strings.TrimSpace(values["CREDIMI_RUNNER_SERIAL"])); healthErr == nil {
				return nil
			} else {
				lastErr = healthErr
			}
		} else {
			lastErr = err
		}
		select {
		case <-deadline.Done():
			if lastErr != nil {
				return fmt.Errorf("runner did not become ready on %s: %w", address, lastErr)
			}
			return fmt.Errorf("runner did not become ready on %s: %w", address, deadline.Err())
		case <-ticker.C:
		}
	}
}

func waitForDashboardRunnerHealth(ctx context.Context, host, port, serial string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/health", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("runner health returned %s", response.Status)
	}
	if serial == "" {
		return nil
	}
	var payload struct {
		Devices []struct {
			Serial string `json:"serial"`
			State  string `json:"state"`
		} `json:"devices"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode runner health: %w", err)
	}
	for _, device := range payload.Devices {
		if strings.TrimSpace(device.Serial) == serial && strings.TrimSpace(device.State) == "device" {
			return nil
		}
	}
	return fmt.Errorf("configured device %q is not ready", serial)
}

func currentDashboardGOOS() string {
	if override := strings.ToLower(strings.TrimSpace(os.Getenv("GOOS_OVERRIDE"))); override != "" {
		return override
	}
	return runtime.GOOS
}

func runtimeStartupDiagnostics(ctx context.Context, manager dashboardruntime.Manager, values dashboardruntime.Values) string {
	status := manager.Status(ctx)
	var parts []string
	if status.LastError != "" {
		parts = append(parts, "last runtime error: "+status.LastError)
	}
	plan := dashboardruntime.BuildRuntimePlan(strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_CONFIG_DIR")), values)
	if len(plan.ComposeServices) == 0 {
		if len(parts) == 0 {
			return "diagnostics: host runner did not bind the expected port; check the runner logs above."
		}
		return "diagnostics: " + strings.Join(parts, " | ")
	}
	logs, err := manager.Logs(ctx, 40)
	if err != nil {
		parts = append(parts, "compose logs unavailable: "+err.Error())
	} else if len(logs) > 0 {
		start := 0
		if len(logs) > 8 {
			start = len(logs) - 8
		}
		var tail []string
		for _, line := range logs[start:] {
			tail = append(tail, strings.TrimSpace(line.Message))
		}
		parts = append(parts, "recent runtime logs:\n"+strings.Join(tail, "\n"))
	}
	if len(parts) == 0 {
		return "diagnostics: runtime did not expose the runner port and no compose logs were available."
	}
	return "diagnostics: " + strings.Join(parts, "\n")
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
	manager.SetPublicURL(publicURL)
	client := &dashboardruntime.CredimiClient{
		BaseURL:    strings.TrimSpace(values["CREDIMI_URL"]),
		APIKey:     apiKey,
		HTTPClient: http.DefaultClient,
	}
	err = client.RegisterMobileRunnerResolvingName(ctx, dashboardruntime.RegisterRunnerRequest{
		RunnerID:     strings.TrimSpace(values["CREDIMI_RUNNER_ID"]),
		Name:         strings.TrimSpace(values["CREDIMI_RUNNER_NAME"]),
		IP:           publicURL,
		Description:  strings.TrimSpace(values["CREDIMI_RUNNER_DESCRIPTION"]),
		Type:         strings.TrimSpace(values["CREDIMI_RUNNER_TYPE"]),
		Port:         publicPort,
		Serial:       strings.TrimSpace(values["CREDIMI_RUNNER_SERIAL"]),
		Organization: strings.TrimSpace(values["CREDIMI_RUNNER_ORGANIZATION"]),
	})
	return err
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
		status := manager.Status(ctx)
		if publicURL := strings.TrimSpace(status.PublicURL); publicURL != "" {
			return publicURL, "", nil
		}
		re := regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.trycloudflare\.com`)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var lastErr error
		for {
			logs, err := dashboardQuickTunnelLogs(deadline, manager, quickTunnelLogTail)
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

func dashboardQuickTunnelLogs(ctx context.Context, manager dashboardruntime.Manager, tail int) ([]dashboardruntime.LogLine, error) {
	if logger, ok := manager.(dashboardTunnelLogger); ok {
		return logger.TunnelLogs(ctx, tail)
	}
	return manager.Logs(ctx, tail)
}
