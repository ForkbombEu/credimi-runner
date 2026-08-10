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
	"github.com/forkbombeu/credimi-runner/pkg/workermanager"
	"github.com/spf13/cobra"
)

var debugVerbose bool
var configPath string
var bootstrapImage string
var bootstrapPullPolicy string

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
	SilenceUsage:  true,
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
	rootCmd.PersistentFlags().StringVar(&bootstrapImage, "bootstrap-image", "", "Runner image to use before the first config.toml is saved")
	rootCmd.PersistentFlags().StringVar(&bootstrapPullPolicy, "bootstrap-pull-policy", "", "Runner image pull policy to use before the first config.toml is saved (always, if-not-present, never)")
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
	configDir := effectiveConfigDir()
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
	values := config.Snapshot()
	if !config.Exists() {
		if err := applyBootstrapValues(values); err != nil {
			return err
		}
	}
	return runContainerLauncher(cmd, configDir, values)
}

func effectiveConfigDir() string {
	if strings.TrimSpace(dashboardConfigDir) != "" {
		return dashboardConfigDir
	}
	if configPath != "" {
		return filepath.Dir(configPath)
	}
	return dashboard.ConfigDir()
}

func applyBootstrapValues(values map[string]string) error {
	if image := strings.TrimSpace(bootstrapImage); image != "" {
		values["ANDROID_RUNNER_IMAGE"] = image
	}
	if policy := strings.TrimSpace(bootstrapPullPolicy); policy != "" {
		switch policy {
		case "always", "if-not-present", "never":
			values["ANDROID_PULL_POLICY"] = policy
		default:
			return fmt.Errorf("invalid bootstrap pull policy %q: use always, if-not-present, or never", policy)
		}
	}
	return nil
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
		return status.PendingRestart || status.PendingRecreate || status.PendingCredimiUpdate || readActiveMobileActivities(configDir)
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

func readActiveMobileActivities(configDir string) bool {
	raw, err := os.ReadFile(filepath.Join(configDir, "active-mobile-activities"))
	if err != nil {
		return false
	}
	count, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return err == nil && count > 0
}

// runApplicationRuntime is the one foreground application unit used by both
// native macOS startup and the Linux managed container.
func runApplicationRuntime(cmd *cobra.Command, args []string) error {
	configDir := effectiveConfigDir()
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
	var edgeManager *dashboardruntime.LifecycleManager
	defer func() {
		if edgeManager != nil {
			_ = edgeManager.Stop(context.Background())
			_ = edgeManager.Close()
		}
	}()
	startNativeEdges := func() error {
		if stdruntime.GOOS != "darwin" || edgeManager != nil {
			return nil
		}
		values, err := runtimeValuesFromConfig(configDir)
		if err != nil {
			return err
		}
		manager := dashboardruntime.NewLifecycleManagerForOS("", configDir, values, nil, "darwin")
		if err := manager.Start(cmd.Context()); err != nil {
			_ = manager.Close()
			return fmt.Errorf("start macOS edge services: %w", err)
		}
		edgeManager = manager
		return nil
	}
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
		if err := startNativeEdges(); err != nil {
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
					if err := startNativeEdges(); err != nil {
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

func runtimeValuesFromConfig(configDir string) (dashboardruntime.Values, error) {
	config, err := dashboard.LoadConfig(configDir)
	if err != nil {
		return nil, err
	}
	values, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(config.Snapshot()), stdruntime.GOOS)
	if err != nil {
		return nil, err
	}
	return values, nil
}

func prepareInternalRuntime(ctx context.Context, configDir string) error {
	workermanager.ConfigureMobileActivityStateFile(filepath.Join(configDir, "active-mobile-activities"))
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
	return androidtools.EnsureRuntimeCapabilitiesAtWith(ctx, cfg, goos, sdkRoot, ensureAndroidCapabilities)
}

func deviceTypes(cfg runnerconfig.Config) []runnerconfig.DeviceType {
	types := make([]runnerconfig.DeviceType, 0, len(cfg.Devices))
	for _, device := range cfg.Devices {
		types = append(types, device.Type)
	}
	return types
}
