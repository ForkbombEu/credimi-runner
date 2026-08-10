package cmd

import (
	"context"
	stdlog "log"
	"net"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strconv"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	runnerplacement "github.com/forkbombeu/credimi-runner/internal/runtime"
	"github.com/spf13/cobra"
)

var debugVerbose bool
var configPath string

var rootCmd = &cobra.Command{
	Use:           "credimi-runner",
	Short:         "Credimi mobile runner",
	Version:       buildinfo.String(),
	SilenceErrors: true,
	SilenceUsage:  false,
	RunE:          runPublic,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		stdlog.Fatal(err)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&debugVerbose, "debug-verbose", false, "Write detailed dashboard, runtime, and container diagnostics to a private log file")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "Path to config.toml")
	rootCmd.AddCommand(&cobra.Command{
		Use:    "internal-runtime",
		Short:  "Run the foreground runtime inside the managed container",
		Hidden: true,
		RunE:   runInternalRuntime,
	})
}

// runPublic is the host launcher. Container mode owns the operational
// dashboard inside the managed container; native mode keeps the dashboard in
// this process because CoreSimulator must execute on macOS.
func runPublic(cmd *cobra.Command, args []string) error {
	configDir := dashboardConfigDir
	if configPath != "" {
		configDir = filepath.Dir(configPath)
	}
	if configDir == "" {
		configDir = dashboard.ConfigDir()
	}
	config, err := dashboard.LoadConfig(configDir)
	if err != nil {
		return err
	}
	if config.Exists() {
		typed, err := runnerconfig.LoadFile(config.Path())
		if err != nil {
			return err
		}
		backend, err := runnerplacement.Select(typed, stdruntime.GOOS)
		if err != nil {
			return err
		}
		if backend == runnerplacement.Native {
			return runDashboard(cmd, args)
		}
	}
	return runContainerLauncher(cmd, configDir, config.Snapshot())
}

func runContainerLauncher(cmd *cobra.Command, configDir string, values map[string]string) error {
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(values), stdruntime.GOOS)
	if err != nil {
		return err
	}
	binaryPath, err := os.Executable()
	if err != nil {
		return err
	}
	manager := dashboardruntime.NewLifecycleManager(binaryPath, configDir, normalized, nil)
	defer manager.Close()
	if err := os.Setenv("CREDIMI_RUNNER_CONFIG_DIR", configDir); err != nil {
		return err
	}
	if err := manager.Start(cmd.Context()); err != nil {
		return err
	}
	defer manager.Stop(context.Background())
	listenHost, listenPort := resolveDashboardListenAddress(cmd, normalized)
	if dashboardOpen && dashboardCanOpenBrowser() {
		go func() {
			time.Sleep(250 * time.Millisecond)
			if err := openDashboardBrowserFunc(dashboardBrowserURL(listenHost, listenPort)); err != nil {
				stdlog.Printf("dashboard browser open skipped: %v", err)
			}
		}()
	}
	sigc, stopSignals := dashboardSignalSource()
	defer stopSignals()
	select {
	case <-sigc:
		return nil
	case <-cmd.Context().Done():
		return cmd.Context().Err()
	}
}

// runInternalRuntime is the foreground application inside the managed
// container. It starts the dashboard first for first-run setup, then starts
// GoA/workers as soon as the dashboard has atomically written config.toml.
func runInternalRuntime(cmd *cobra.Command, args []string) error {
	configDir := dashboardConfigDir
	if configPath != "" {
		configDir = filepath.Dir(configPath)
	}
	if configDir == "" {
		configDir = dashboard.ConfigDir()
	}
	if err := os.Setenv("CREDIMI_RUNNER_CONFIG_DIR", configDir); err != nil {
		return err
	}
	if err := configureInternalListeners(configDir); err != nil {
		return err
	}
	if err := provisionInternalRuntime(cmd.Context(), configDir); err != nil {
		return err
	}
	serverHost := host
	if serverHost == "127.0.0.1" || serverHost == "" {
		serverHost = "0.0.0.0"
	}
	previousHost := host
	host = serverHost
	defer func() { host = previousHost }()
	errCh := make(chan error, 2)
	go func() { errCh <- runDashboardOwned(cmd, args) }()
	serverStarted := false
	startServer := func() {
		if serverStarted {
			return
		}
		serverStarted = true
		go func() { errCh <- serverCmd.RunE(cmd, args) }()
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.toml")); err == nil {
		startServer()
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-errCh:
			return err
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(configDir, "config.toml")); err == nil {
				startServer()
			}
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		}
	}
}

func configureInternalListeners(configDir string) error {
	cfg, err := runnerconfig.LoadFile(filepath.Join(configDir, "config.toml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if configuredHost, configuredPort, err := net.SplitHostPort(cfg.Server.APIListen); err == nil {
		if configuredHost == "" || configuredHost == "127.0.0.1" || configuredHost == "::1" {
			configuredHost = "0.0.0.0"
		}
		host = configuredHost
		if parsed, err := strconv.Atoi(configuredPort); err == nil {
			port = parsed
		}
	}
	return nil
}

func provisionInternalRuntime(ctx context.Context, configDir string) error {
	return provisionInternalRuntimeAt(ctx, configDir, "/opt/android-sdk")
}

func provisionInternalRuntimeAt(ctx context.Context, configDir, sdkRoot string) error {
	cfg, err := runnerconfig.LoadFile(filepath.Join(configDir, "config.toml"))
	if err != nil || len(cfg.Devices) == 0 {
		return nil
	}
	backend, err := runnerplacement.Select(cfg, stdruntime.GOOS)
	if err != nil {
		return err
	}
	if backend == runnerplacement.Container {
		return androidtools.Ensure(ctx, sdkRoot)
	}
	return nil
}
