package cmd

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"path/filepath"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	"github.com/forkbombeu/credimi-runner/internal/servicecoordination"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
	"github.com/spf13/cobra"
)

var debugVerbose bool
var configPath string
var bootstrapImage string
var bootstrapPullPolicy string

var rootCmd = &cobra.Command{Use: "credimi-runner", Short: "Credimi mobile runner", Version: buildinfo.String(), SilenceErrors: true, SilenceUsage: true, RunE: runRoot}

var serviceManagerFactory = func(configDir string, bootstrap servicemanager.BootstrapOptions) servicemanager.Manager {
	return servicemanager.ForCurrentPlatformWithBootstrap(configDir, bootstrap)
}

func currentServiceManager() servicemanager.Manager {
	return serviceManagerFactory(effectiveConfigDir(), servicemanager.BootstrapOptions{Image: bootstrapImage, PullPolicy: bootstrapPullPolicy})
}

var waitForDashboardFunc = func(ctx context.Context) (string, error) {
	metadata, err := waitForRunningController(ctx, effectiveConfigDir(), "")
	if err != nil {
		return "", err
	}
	return metadata.PublicURL, nil
}

func runRoot(cmd *cobra.Command, _ []string) error {
	coordinationCleanup, err := servicecoordination.StartPresence(cmd.Context(), effectiveConfigDir())
	if err != nil {
		return fmt.Errorf("publish attached host presence: %w", err)
	}
	defer coordinationCleanup()
	manager := currentServiceManager()
	status, err := manager.Status(cmd.Context())
	if err != nil || !status.Running {
		if err := manager.Start(cmd.Context()); err != nil {
			return err
		}
	}
	url, err := waitForDashboardFunc(cmd.Context())
	if err != nil {
		return err
	}
	if dashboardOpen && dashboardCanOpenBrowser() {
		if err := openDashboardBrowserFunc(url); err != nil {
			cmd.Printf("Dashboard: %s\n", url)
		}
	} else {
		cmd.Printf("Dashboard: %s\n", url)
	}
	return followAttachedService(cmd.Context(), manager, effectiveConfigDir())
}

func followAttachedService(ctx context.Context, manager servicemanager.Manager, configDir string) error {
	handled := make(map[string]struct{})
	for {
		if !servicecoordination.CoordinatorOwned(configDir) {
			return errors.New("attached Credimi Runner coordinator ownership was lost")
		}
		logsCtx, cancelLogs := context.WithCancel(ctx)
		logsDone := make(chan error, 1)
		go func() { logsDone <- manager.Logs(logsCtx, servicemanager.LogOptions{Follow: true, Lines: 200}) }()
		ticker := time.NewTicker(500 * time.Millisecond)
		restartFollower := false
		for !restartFollower {
			select {
			case <-ctx.Done():
				cancelLogs()
				<-logsDone
				return nil
			case <-logsDone:
				// A container can disappear while it is being replaced. Keep the
				// attached command alive and resume following its replacement.
				restartFollower = true
			case <-ticker.C:
				if !servicecoordination.CoordinatorOwned(configDir) {
					cancelLogs()
					<-logsDone
					ticker.Stop()
					return errors.New("attached Credimi Runner coordinator ownership was lost")
				}
				request, err := servicecoordination.ReadRestartRequest(configDir)
				if err != nil {
					continue
				}
				if _, alreadyHandled := handled[request.RequestID]; alreadyHandled {
					continue
				}
				if result, resultErr := servicecoordination.ReadRestartResult(configDir); resultErr == nil && result.RequestID == request.RequestID {
					handled[request.RequestID] = struct{}{}
					continue
				}
				cancelLogs()
				<-logsDone
				ticker.Stop()
				handled[request.RequestID] = struct{}{}
				if err := applyServiceRestartRequest(ctx, manager, configDir, request); err != nil {
					return err
				}
				restartFollower = true
			}
		}
		ticker.Stop()
		cancelLogs()
		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}

func applyServiceRestartRequest(ctx context.Context, manager servicemanager.Manager, configDir string, request servicecoordination.RestartRequest) error {
	writeResult := func(success bool, fingerprint, message string) error {
		return servicecoordination.WriteRestartResult(configDir, servicecoordination.RestartResult{
			RequestID: request.RequestID, Success: success, AppliedFingerprint: fingerprint,
			Error: sanitizeServiceError(message, configDir), UpdatedAt: time.Now().UTC(),
		})
	}
	cfg, err := runnerconfig.LoadFile(filepath.Join(configDir, "config.toml"))
	if err != nil {
		resultErr := writeResult(false, "", fmt.Sprintf("load saved service configuration: %v", err))
		return errors.Join(err, resultErr)
	}
	host, hostErr := servicemanager.ResolveHostContext(configDir)
	if hostErr != nil {
		return writeResult(false, "", fmt.Sprintf("resolve host service configuration: %v", hostErr))
	}
	expected := servicemanager.ServiceConfigFingerprintForHost(cfg, true, host)
	if request.RequestedFingerprint != expected {
		return writeResult(false, "", "service restart request was superseded by a newer configuration")
	}
	if status, statusErr := manager.Status(ctx); statusErr == nil && status.Running && !status.ServiceRestartRequired {
		return writeResult(true, expected, "")
	}
	previous, _ := controller.ReadMetadata(configDir)
	if err := manager.Restart(ctx); err != nil {
		resultErr := writeResult(false, "", fmt.Sprintf("service restart failed: %v", err))
		return errors.Join(err, resultErr)
	}
	if _, err := waitForRunningControllerUsingWithTimeout(ctx, configDir, previous.IdentityToken, "", serviceApplyTimeout, controller.ReadMetadata, controller.Probe); err != nil {
		resultErr := writeResult(false, "", fmt.Sprintf("replacement service did not become ready: %v", err))
		return errors.Join(err, resultErr)
	}
	status, err := manager.Status(ctx)
	if err != nil {
		resultErr := writeResult(false, "", fmt.Sprintf("verify replacement service configuration: %v", err))
		return errors.Join(err, resultErr)
	}
	if !status.Running || status.ServiceRestartRequired {
		err := errors.New("replacement service is not running with the requested configuration")
		resultErr := writeResult(false, "", err.Error())
		return errors.Join(err, resultErr)
	}
	return writeResult(true, expected, "")
}

func sanitizeServiceError(message, configDir string) string {
	cfg, err := runnerconfig.LoadFile(filepath.Join(configDir, "config.toml"))
	if err == nil {
		for _, secret := range []string{cfg.Server.DashboardToken, cfg.Credimi.UserAPIKey, cfg.Credimi.InternalAdminKey, cfg.Exposure.CloudflareToken} {
			if secret != "" {
				message = strings.ReplaceAll(message, secret, "[redacted]")
			}
		}
	}
	return message
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		stdlog.Fatal(err)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&debugVerbose, "debug-verbose", false, "Write detailed dashboard and runtime diagnostics to a private log file")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "Path to config.toml")
	rootCmd.PersistentFlags().StringVar(&bootstrapImage, "bootstrap-image", "", "Runner image to use before the first config.toml is saved")
	rootCmd.PersistentFlags().StringVar(&bootstrapPullPolicy, "bootstrap-pull-policy", "", "Runner image pull policy to use before the first config.toml is saved")
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

func serviceNotRunningError() error {
	return fmt.Errorf("Credimi Runner service is not running. Start it with: credimi-runner service start")
}
