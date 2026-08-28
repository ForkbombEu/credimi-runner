package cmd

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"net"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strconv"
	"strings"
	"sync"
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

const quickTunnelResolutionTimeout = 2 * time.Minute

type containerLauncherManager interface {
	Start(context.Context) error
	Stop(context.Context) error
	UpdateImage(context.Context) error
	RecreateRunner(context.Context, bool) error
	Configure(dashboardruntime.Values)
	Status(context.Context) dashboardruntime.RuntimeStatus
	VerifyPublicURL(context.Context, string) error
	Close() error
}

var newContainerLauncherManager = func(binaryPath, configDir string, values dashboardruntime.Values) containerLauncherManager {
	return dashboardruntime.NewLifecycleManager(binaryPath, configDir, values, nil)
}

var writeComposeFileForOS = dashboardruntime.WriteComposeFileForOS

var runInternalDashboardFunc = runDashboardOwned
var runInternalServerFunc = func(cmd *cobra.Command, args []string) error { return serverCmd.RunE(cmd, args) }
var ensureEmulatorRuntime = androidtools.EnsureEmulatorReadyAt

// nativeRuntimeReconcile is installed only while the native foreground
// runtime is running. The server control loop invokes it for restart-impact
// configuration changes so native edge/runtime components are rebuilt from the
// newly persisted typed configuration.
var nativeRuntimeReconcile func(context.Context) error

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
	bootstrap := hostBootstrapContext(!fileExists(filepath.Join(configDir, "config.toml")))
	values = map[string]string(bootstrap.Apply(dashboardruntime.Values(values)))
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
	var valuesMu sync.RWMutex
	currentValues := normalized
	snapshotValues := func() dashboardruntime.Values {
		valuesMu.RLock()
		defer valuesMu.RUnlock()
		copy := make(dashboardruntime.Values, len(currentValues))
		for key, value := range currentValues {
			copy[key] = value
		}
		return copy
	}
	var refreshQuickTunnelURL func(context.Context, dashboardruntime.Values) error
	var resolveAndVerifyQuickTunnel func(context.Context, dashboardruntime.Values) error
	reconcile := func(ctx context.Context) error {
		wasRunning := executionRuntimeRunning(configDir)
		config, err := dashboard.LoadConfig(configDir)
		if err != nil {
			return fmt.Errorf("reload configuration for reconciliation: %w", err)
		}
		nextInput := dashboardruntime.Values(config.Snapshot())
		nextInput = hostBootstrapContext(false).Apply(nextInput)
		next, err := dashboardruntime.NormalizeValues(nextInput, stdruntime.GOOS)
		if err != nil {
			return fmt.Errorf("normalize configuration for reconciliation: %w", err)
		}
		valuesMu.RLock()
		previous := make(dashboardruntime.Values, len(currentValues))
		for key, value := range currentValues {
			previous[key] = value
		}
		valuesMu.RUnlock()
		diff := dashboardruntime.DiffValuesForOS(previous, next, stdruntime.GOOS)
		composeRecreate := hasApplyClass(diff, dashboardruntime.ApplyComposeRecreate)
		restartRequired := hasApplyClass(diff, dashboardruntime.ApplyRestartRequired)
		switch {
		case composeRecreate:
			// The first reconcile replaces the runner-only bootstrap Compose
			// file with the final topology. Write it before Stop: Stop uses the
			// active Compose model to target exposure services, and the bootstrap
			// model deliberately has no caddy or tunnel service yet.
			if err := writeComposeFileForOS(configDir, next, stdruntime.GOOS); err != nil {
				manager.Configure(previous)
				return fmt.Errorf("write compose file for configuration reconciliation: %w", err)
			}
			if err := launcher.ClearQuickTunnelURL(configDir); err != nil {
				manager.Configure(previous)
				return err
			}
			manager.Configure(next)
			if wasRunning {
				if err := manager.Stop(ctx); err != nil {
					manager.Configure(previous)
					return fmt.Errorf("stop runtime for configuration reconciliation: %w", err)
				}
				if err := manager.Start(ctx); err != nil {
					manager.Configure(previous)
					return fmt.Errorf("start runtime after configuration reconciliation: %w", err)
				}
				if err := refreshQuickTunnelURL(ctx, next); err != nil {
					return err
				}
			} else if err := manager.RecreateRunner(ctx, true); err != nil {
				manager.Configure(previous)
				return fmt.Errorf("recreate stopped runner for configuration reconciliation: %w", err)
			}
		case restartRequired:
			if err := writeComposeFileForOS(configDir, next, stdruntime.GOOS); err != nil {
				manager.Configure(previous)
				return fmt.Errorf("write compose file for runner restart: %w", err)
			}
			manager.Configure(next)
			if err := manager.RecreateRunner(ctx, false); err != nil {
				manager.Configure(previous)
				return fmt.Errorf("recreate runner for configuration reconciliation: %w", err)
			}
		default:
			manager.Configure(next)
		}
		valuesMu.Lock()
		currentValues = next
		valuesMu.Unlock()
		return nil
	}
	resolveAndVerifyQuickTunnel = func(ctx context.Context, values dashboardruntime.Values) error {
		plan := dashboardruntime.BuildRuntimePlanForOS(configDir, values, stdruntime.GOOS)
		// The bootstrap Compose plan intentionally contains only the runner so
		// the setup dashboard can come up before the user chooses an exposure
		// mode. Do not start a two-minute diagnostics poll for a tunnel that
		// cannot exist yet: the final setup reconciliation is the sole owner of
		// the first quick tunnel.
		if !runtimePlanHasQuickTunnel(plan) {
			return nil
		}
		resolver, ok := manager.(interface {
			QuickTunnelURL(context.Context) (string, error)
		})
		if !ok {
			return errors.New("quick tunnel URL discovery is unavailable")
		}
		// Cloudflared can publish the hostname well after its container has
		// started. Keep querying the same launcher-owned diagnostics endpoint for
		// a bounded minutes-scale window instead of failing while the tunnel is
		// still establishing its connection.
		deadline, cancel := context.WithTimeout(ctx, quickTunnelResolutionTimeout)
		defer cancel()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		var lastErr error
		for {
			url, err := resolver.QuickTunnelURL(deadline)
			if err == nil && strings.TrimSpace(url) != "" {
				if verifyErr := manager.VerifyPublicURL(deadline, url); verifyErr != nil {
					if errors.Is(verifyErr, dashboardruntime.ErrPublicEndpointIdentity) {
						return verifyErr
					}
					lastErr = verifyErr
				} else {
					return launcher.WriteQuickTunnelURL(configDir, url)
				}
			}
			if err != nil {
				lastErr = err
			}
			select {
			case <-deadline.Done():
				if lastErr != nil {
					return fmt.Errorf("resolve quick tunnel URL: %w", lastErr)
				}
				return errors.New("timed out waiting for quick tunnel URL")
			case <-ticker.C:
			}
		}
	}
	refreshQuickTunnelURL = func(ctx context.Context, values dashboardruntime.Values) error {
		plan := dashboardruntime.BuildRuntimePlanForOS(configDir, values, stdruntime.GOOS)
		if !runtimePlanHasQuickTunnel(plan) {
			return launcher.ClearQuickTunnelURL(configDir)
		}
		if err := launcher.ClearQuickTunnelURL(configDir); err != nil {
			return err
		}
		err := resolveAndVerifyQuickTunnel(ctx, values)
		if err == nil {
			return nil
		}
		// A healthy Cloudflare edge returning 502/503/504 proves the hostname
		// is established; rotating it cannot repair the origin path. Identity
		// mismatches are terminal for the same reason.
		if errors.Is(err, dashboardruntime.ErrPublicOriginUnavailable) || errors.Is(err, dashboardruntime.ErrPublicEndpointIdentity) {
			return fmt.Errorf("resolve quick tunnel URL: %w", err)
		}
		// Only transport/establishment failures get one bounded edge restart.
		if stopErr := manager.Stop(ctx); stopErr != nil {
			return fmt.Errorf("restart quick tunnel after readiness failure: stop: %w (original: %v)", stopErr, err)
		}
		if clearErr := launcher.ClearQuickTunnelURL(configDir); clearErr != nil {
			return clearErr
		}
		if startErr := manager.Start(ctx); startErr != nil {
			return fmt.Errorf("restart quick tunnel after readiness failure: start: %w (original: %v)", startErr, err)
		}
		if retryErr := resolveAndVerifyQuickTunnel(ctx, values); retryErr != nil {
			return fmt.Errorf("resolve quick tunnel URL after one restart: %w", retryErr)
		}
		return nil
	}
	upgrade := func(ctx context.Context) error {
		if err := launcher.ClearQuickTunnelURL(configDir); err != nil {
			return err
		}
		if err := manager.UpdateImage(ctx); err != nil {
			return err
		}
		values := snapshotValues()
		return refreshQuickTunnelURL(ctx, values)
	}
	resumePendingSetup := fileExists(filepath.Join(configDir, "setup-pending"))
	reconcileSetup := reconcile
	if resumePendingSetup {
		// A launcher restart discards its in-memory operation result. Replace a
		// stale setup handoff with one fresh launcher-owned operation before the
		// runner container starts, so the Dashboard has a result it can observe.
		reconcileSetup = func(ctx context.Context) error {
			if err := manager.Start(ctx); err != nil {
				return fmt.Errorf("start runtime while resuming setup: %w", err)
			}
			return refreshQuickTunnelURL(ctx, snapshotValues())
		}
	}
	control, err := launcher.ServeWithOperations(filepath.Join(configDir, "control.sock"), upgrade, func() bool {
		status := manager.Status(context.Background())
		return status.PendingRestart || status.PendingRecreate || status.PendingCredimiUpdate || readActiveMobileActivities(configDir)
	}, launcher.Operations{
		ReconcileConfig: reconcile,
		ReconcileSetup:  reconcileSetup,
		QuickTunnelURL: func(ctx context.Context) (string, error) {
			return launcher.ReadQuickTunnelURL(configDir)
		},
		RuntimeStart: func(ctx context.Context) error {
			if err := manager.Start(ctx); err != nil {
				return err
			}
			return refreshQuickTunnelURL(ctx, snapshotValues())
		},
		RuntimeStop: func(ctx context.Context) error {
			if err := manager.Stop(ctx); err != nil {
				return err
			}
			if err := launcher.ClearQuickTunnelURL(configDir); err != nil {
				return err
			}
			return requestRuntimeCommandAndWait(ctx, configDir, "stop", "stopped")
		},
		RuntimeRestart: func(ctx context.Context) error {
			if err := manager.Stop(ctx); err != nil {
				return err
			}
			if err := launcher.ClearQuickTunnelURL(configDir); err != nil {
				return err
			}
			if err := requestRuntimeCommandAndWait(ctx, configDir, "stop", "stopped"); err != nil {
				return err
			}
			if err := manager.Start(ctx); err != nil {
				return err
			}
			return refreshQuickTunnelURL(ctx, snapshotValues())
		},
	})
	if err != nil {
		return err
	}
	defer control.Close()
	if err := os.Setenv("CREDIMI_RUNNER_CONFIG_DIR", configDir); err != nil {
		return err
	}
	if resumePendingSetup {
		// The fresh operation persists setup-operation before it can start the
		// runner. Its completion owns exposure verification and final setup
		// registration; do not race it with the normal launcher start path.
		if _, err := launcher.RequestSetupReconcileAsync(cmd.Context(), filepath.Join(configDir, "control.sock")); err != nil {
			return fmt.Errorf("resume pending setup: %w", err)
		}
	} else {
		// A launcher restart creates a new quick tunnel. Remove any previous
		// endpoint before the new dashboard can resume registration, so Credimi
		// never receives a URL from the stopped tunnel.
		if err := launcher.ClearQuickTunnelURL(configDir); err != nil {
			return err
		}
		if runtimeExecutionStopped(configDir) {
			if err := manager.RecreateRunner(cmd.Context(), true); err != nil {
				return fmt.Errorf("start stopped runner service: %w", err)
			}
		} else {
			if err := manager.Start(cmd.Context()); err != nil {
				return err
			}
			if err := refreshQuickTunnelURL(cmd.Context(), snapshotValues()); err != nil {
				return err
			}
		}
	}
	defer manager.Stop(context.Background())
	defer launcher.ClearQuickTunnelURL(configDir)
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

func runtimePlanHasQuickTunnel(plan dashboardruntime.RuntimePlan) bool {
	if plan.ServiceMode != "auto" {
		return false
	}
	for _, service := range plan.ComposeServices {
		if service == "tunnel" {
			return true
		}
	}
	return false
}

func executionRuntimeRunning(configDir string) bool {
	raw, err := os.ReadFile(filepath.Join(configDir, "runtime-state"))
	if err != nil {
		return true
	}
	switch strings.TrimSpace(string(raw)) {
	case "running", "starting", "restarting":
		return true
	default:
		return false
	}
}

func runtimeExecutionStopped(configDir string) bool {
	raw, err := os.ReadFile(filepath.Join(configDir, "runtime-state"))
	return err == nil && strings.TrimSpace(string(raw)) == "stopped"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func hostBootstrapContext(beforeSetup bool) dashboardruntime.BootstrapContext {
	home, _ := os.UserHomeDir()
	return dashboardruntime.BootstrapContext{
		RunnerImage: func() string {
			if beforeSetup {
				return bootstrapImage
			}
			return ""
		}(),
		PullPolicy: func() string {
			if beforeSetup {
				return bootstrapPullPolicy
			}
			return ""
		}(),
		HostUID:             os.Getuid(),
		HostGID:             os.Getgid(),
		HostHome:            home,
		HostAndroidDir:      filepath.Join(home, ".android"),
		HostGoldenRoot:      filepath.Join(home, "avd-golden"),
		ContainerAndroidDir: "/root/.android",
		ContainerAVDHome:    "/root/.android/avd",
		ContainerGoldenRoot: "/avd-golden",
		HostNetwork:         beforeSetup,
		BeforeSetup:         beforeSetup,
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

func hasApplyClass(diff dashboardruntime.ConfigDiff, class dashboardruntime.ApplyClass) bool {
	for _, candidate := range diff.Classes {
		if candidate == class {
			return true
		}
	}
	return false
}

func writeRuntimeCommand(configDir, action string) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("unsupported runtime action %q", action)
	}
	temporary, err := os.CreateTemp(configDir, ".runtime-control-*")
	if err != nil {
		return fmt.Errorf("create runtime control request: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(action + "\n"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(configDir, "runtime-control"))
}

func requestRuntimeCommandAndWait(ctx context.Context, configDir, action, expected string) error {
	sequence := int64(0)
	if raw, err := os.ReadFile(filepath.Join(configDir, "runtime-state-sequence")); err == nil {
		sequence, _ = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	}
	if err := writeRuntimeCommand(configDir, action); err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, err := os.ReadFile(filepath.Join(configDir, "runtime-state"))
		currentSequence := int64(0)
		if sequenceRaw, sequenceErr := os.ReadFile(filepath.Join(configDir, "runtime-state-sequence")); sequenceErr == nil {
			currentSequence, _ = strconv.ParseInt(strings.TrimSpace(string(sequenceRaw)), 10, 64)
		}
		if err == nil && currentSequence > sequence {
			state := strings.TrimSpace(string(raw))
			if strings.HasPrefix(state, "failed:") {
				return fmt.Errorf("runtime %s failed: %s", action, strings.TrimSpace(strings.TrimPrefix(state, "failed:")))
			}
			if state == expected {
				return nil
			}
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("timed out waiting for runtime %s to reach %s", action, expected)
		case <-ticker.C:
		}
	}
}

// runApplicationRuntime is the one foreground application unit used by both
// native macOS startup and the Linux managed container.
func runApplicationRuntime(cmd *cobra.Command, args []string) error {
	configDir := effectiveConfigDir()
	if err := os.Setenv("CREDIMI_RUNNER_CONFIG_DIR", configDir); err != nil {
		return err
	}
	previousRuntimeControl, hadRuntimeControl := os.LookupEnv(dashboard.RuntimeControlFileEnv)
	if err := os.Setenv(dashboard.RuntimeControlFileEnv, filepath.Join(configDir, "runtime-control")); err != nil {
		return err
	}
	defer func() {
		if hadRuntimeControl {
			_ = os.Setenv(dashboard.RuntimeControlFileEnv, previousRuntimeControl)
		} else {
			_ = os.Unsetenv(dashboard.RuntimeControlFileEnv)
		}
	}()
	serverHost := host
	if serverHost == "127.0.0.1" || serverHost == "" {
		serverHost = "0.0.0.0"
	}
	previousHost := host
	host = serverHost
	defer func() { host = previousHost }()
	errCh := make(chan error, 2)
	startDashboard := func() {
		go func() { errCh <- runInternalDashboardFunc(cmd, args) }()
	}
	serverStarted := false
	var edgeManager *dashboardruntime.LifecycleManager
	restoreNativeResolver := dashboard.SetNativeQuickTunnelResolver(nil)
	previousNativeReconcile := nativeRuntimeReconcile
	defer func() {
		nativeRuntimeReconcile = previousNativeReconcile
		restoreNativeResolver()
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
		// Registration in the runtime-owned dashboard must query this same
		// native edge manager rather than constructing a competing manager.
		dashboard.SetNativeQuickTunnelResolver(edgeManager.QuickTunnelURL)
		return nil
	}
	nativeRuntimeReconcile = func(ctx context.Context) error {
		if stdruntime.GOOS != "darwin" {
			return nil
		}
		if edgeManager != nil {
			if err := edgeManager.Stop(ctx); err != nil {
				return err
			}
			if err := edgeManager.Close(); err != nil {
				return err
			}
			edgeManager = nil
		}
		if err := hydrateTypedRuntimeEnvironment(configDir); err != nil {
			return err
		}
		if err := configureInternalListeners(configDir); err != nil {
			return err
		}
		return startNativeEdges()
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
		// The owned dashboard begins registration immediately. Provision the
		// configured Android capabilities first so its physical-device probe
		// never races the installation of persistent platform-tools.
		startDashboard()
		startServer()
	} else {
		startDashboard()
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
	if err := provisionInternalRuntime(ctx, configDir); err != nil {
		return err
	}
	cfg, err := runnerconfig.LoadFile(filepath.Join(configDir, "config.toml"))
	if err != nil {
		return err
	}
	return androidtools.ConnectConfiguredWiFiDevices(ctx, cfg)
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
	return ensureEmulatorRuntime(ctx, cfg, goos, sdkRoot, nil)
}

func deviceTypes(cfg runnerconfig.Config) []runnerconfig.DeviceType {
	types := make([]runnerconfig.DeviceType, 0, len(cfg.Devices))
	for _, device := range cfg.Devices {
		types = append(types, device.Type)
	}
	return types
}
