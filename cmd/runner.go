package cmd

import (
	"context"
	"fmt"
	stdlog "log"
	"net"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/launcher"
	runnerplacement "github.com/forkbombeu/credimi-runner/internal/runtime"
	"github.com/spf13/cobra"
)

var debugVerbose bool
var configPath string

type containerLauncherManager interface {
	Start(context.Context) error
	Stop(context.Context) error
	UpdateImage(context.Context) error
	Status(context.Context) dashboardruntime.RuntimeStatus
	Close() error
}

var newContainerLauncherManager = func(binaryPath, configDir string, values dashboardruntime.Values) containerLauncherManager {
	return dashboardruntime.NewLifecycleManager(binaryPath, configDir, values, nil)
}

var runInternalDashboardFunc = runDashboardOwned
var runInternalServerFunc = func(cmd *cobra.Command, args []string) error { return serverCmd.RunE(cmd, args) }
var ensureAndroidCapabilities = androidtools.EnsureCapabilities

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
		RunE:   runApplicationRuntime,
	})
}

// runPublic is the host launcher. Container mode owns the operational
// dashboard inside the managed container; native mode keeps the dashboard in
// this process because CoreSimulator must execute on macOS.
func runPublic(cmd *cobra.Command, args []string) error {
	return runPublicForOS(cmd, args, stdruntime.GOOS)
}

func runPublicForOS(cmd *cobra.Command, args []string, goos string) error {
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
		_, err := runnerconfig.LoadFile(config.Path())
		if err != nil {
			return err
		}
	}
	backend, err := runnerplacement.Select(goos)
	if err != nil {
		return err
	}
	if backend == runnerplacement.Native {
		return runApplicationRuntime(cmd, args)
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
	manager := newContainerLauncherManager(binaryPath, configDir, normalized)
	defer manager.Close()
	control, err := launcher.Serve(filepath.Join(configDir, "control.sock"), manager.UpdateImage, func() bool {
		status := manager.Status(context.Background())
		return status.PendingRestart || status.PendingRecreate || status.PendingCredimiUpdate
	})
	if err != nil {
		return err
	}
	defer control.Close()
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

// runApplicationRuntime is the one foreground application unit used by both
// native macOS startup and the Linux managed container.
func runApplicationRuntime(cmd *cobra.Command, args []string) error {
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
	serverHost := host
	if serverHost == "127.0.0.1" || serverHost == "" {
		serverHost = "0.0.0.0"
	}
	previousHost := host
	host = serverHost
	defer func() { host = previousHost }()
	errCh := make(chan error, 2)
	go func() { errCh <- runInternalDashboardFunc(cmd, args) }()
	serverStarted := false
	startServer := func() {
		if serverStarted {
			return
		}
		serverStarted = true
		go func() { errCh <- runInternalServerFunc(cmd, args) }()
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.toml")); err == nil {
		if err := prepareInternalRuntime(cmd.Context(), configDir); err != nil {
			return err
		}
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
				if !serverStarted {
					if err := prepareInternalRuntime(cmd.Context(), configDir); err != nil {
						return err
					}
				}
				startServer()
			}
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		}
	}
}

func prepareInternalRuntime(ctx context.Context, configDir string) error {
	if err := configureInternalListeners(configDir); err != nil {
		return err
	}
	if err := hydrateTypedRuntimeEnvironment(configDir); err != nil {
		return err
	}
	return provisionInternalRuntime(ctx, configDir)
}

// hydrateTypedRuntimeEnvironment is the compatibility boundary for existing
// Credimi services that still read process-global configuration. TOML remains
// authoritative; this snapshot contains only stable runner/device inventory
// values and is never changed per activity or selected device.
func hydrateTypedRuntimeEnvironment(configDir string) error {
	store, err := dashboardruntime.LoadStore(configDir)
	if err != nil {
		return err
	}
	if !store.Exists() {
		return nil
	}
	for key, value := range store.Snapshot() {
		if strings.HasPrefix(key, "CREDIMI_DEVICE_") {
			continue
		}
		if value == "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("hydrate %s from typed configuration: %w", key, err)
		}
	}
	return nil
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
	cfg, err := runnerconfig.LoadFile(filepath.Join(configDir, "config.toml"))
	if err != nil || len(cfg.Devices) == 0 {
		return nil
	}
	sdkRoot := os.Getenv("ANDROID_SDK_ROOT")
	if sdkRoot == "" {
		if stdruntime.GOOS == "darwin" {
			sdkRoot = filepath.Join(cfg.Storage.StateDir, "android", "sdk")
		} else {
			sdkRoot = "/opt/android-sdk"
		}
	}
	return provisionInternalRuntimeAt(ctx, configDir, sdkRoot)
}

func provisionInternalRuntimeAt(ctx context.Context, configDir, sdkRoot string) error {
	return provisionInternalRuntimeAtForOS(ctx, configDir, sdkRoot, stdruntime.GOOS)
}

func provisionInternalRuntimeAtForOS(ctx context.Context, configDir, sdkRoot, goos string) error {
	cfg, err := runnerconfig.LoadFile(filepath.Join(configDir, "config.toml"))
	if err != nil || len(cfg.Devices) == 0 {
		return nil
	}
	if err := runnerplacement.ValidateDeviceTypes(deviceTypes(cfg), goos); err != nil {
		return err
	}
	needsEmulator := false
	systemImage := ""
	for _, device := range cfg.Devices {
		if !device.Enabled || device.Type != runnerconfig.DeviceAndroidEmulator {
			continue
		}
		needsEmulator = true
		if device.AndroidEmulator != nil {
			systemImage = device.AndroidEmulator.SystemImage
		}
	}
	if err := ensureAndroidCapabilities(ctx, sdkRoot, needsEmulator, systemImage); err != nil {
		return err
	}
	avdHome := os.Getenv("ANDROID_AVD_HOME")
	if avdHome == "" {
		avdHome = os.Getenv("HOST_AVD_HOME_PATH")
	}
	if avdHome == "" {
		avdHome = filepath.Join(cfg.Storage.StateDir, "android", "avd")
	}
	if err := androidtools.ConfigureStableEnvironmentWithAVD(sdkRoot, avdHome); err != nil {
		return err
	}
	if needsEmulator {
		for _, device := range cfg.Devices {
			if !device.Enabled || device.Type != runnerconfig.DeviceAndroidEmulator || device.AndroidEmulator == nil {
				continue
			}
			if err := androidtools.EnsureAVD(ctx, sdkRoot, avdHome, device.AndroidEmulator.AVDName, device.AndroidEmulator.SystemImage, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func deviceTypes(cfg runnerconfig.Config) []runnerconfig.DeviceType {
	types := make([]runnerconfig.DeviceType, 0, len(cfg.Devices))
	for _, device := range cfg.Devices {
		types = append(types, device.Type)
	}
	return types
}
