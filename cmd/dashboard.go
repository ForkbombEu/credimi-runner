package cmd

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
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
	dashboardHost      string
	dashboardPort      int
	dashboardConfigDir string
	dashboardOpen      bool
)

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
	controllerID := fmt.Sprintf("controller-%d", time.Now().UnixNano())
	identityToken, err := controller.NewIdentityToken()
	if err != nil {
		return err
	}
	plan := dashboardruntime.BuildRuntimePlan(configDir, values)

	dashboardCtx, cancelDashboard := context.WithCancel(context.Background())
	defer cancelDashboard()
	operations := controller.NewCoordinator(dashboardCtx)
	handler, cancelHandler, err := dashboard.NewHandlerWithManagerContextAndIdentityAndCoordinatorAndBootstrapProgress(dashboardCtx, configDir, manager, controllerID, identityToken, plan.ConfigFingerprint, operations, func(message string) {
		cmd.Println(message)
	})
	if err != nil {
		return err
	}
	defer cancelHandler()
	if err := lease.Publish(controller.Metadata{
		ControllerID:      controllerID,
		PID:               os.Getpid(),
		ConfigDir:         configDir,
		ListenHost:        listenHost,
		ListenPort:        listenPort,
		ProbeURL:          fmt.Sprintf("http://127.0.0.1:%d/internal/controller/identity", listenPort),
		PublicURL:         dashboardDisplayURL(listenHost, listenPort),
		ConfigFingerprint: plan.ConfigFingerprint,
		IdentityToken:     identityToken,
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
	if dashboardOpen && dashboardCanOpenBrowser() {
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := controller.Probe(ctx, metadata); err != nil {
		return fmt.Errorf("dashboard lock is held but controller identity verification failed: %w", err)
	}
	url := metadata.PublicURL
	if strings.TrimSpace(url) == "" {
		url = dashboardDisplayURL(metadata.ListenHost, metadata.ListenPort)
	}
	if cmd != nil {
		cmd.Printf("Credimi Runner dashboard is already running at %s\n", url)
	}
	if dashboardOpen && dashboardCanOpenBrowser() {
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

func dashboardDisplayURL(host string, port int) string {
	if strings.TrimSpace(host) != "0.0.0.0" && strings.TrimSpace(host) != "::" && strings.TrimSpace(host) != "[::]" {
		return dashboardBrowserURL(host, port)
	}
	if hostname, err := os.Hostname(); err == nil && strings.TrimSpace(hostname) != "" {
		return fmt.Sprintf("http://%s:%d", hostname, port)
	}
	return dashboardBrowserURL(host, port)
}

func dashboardCanOpenBrowser() bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return true
	}
	return strings.TrimSpace(os.Getenv("DISPLAY")) != "" || strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != ""
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

func currentDashboardGOOS() string {
	if override := strings.ToLower(strings.TrimSpace(os.Getenv("GOOS_OVERRIDE"))); override != "" {
		return override
	}
	return runtime.GOOS
}
