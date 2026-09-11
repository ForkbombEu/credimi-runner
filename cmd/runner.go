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
var dashboardListen string

var rootCmd = &cobra.Command{Use: "credimi-runner", Short: "Credimi mobile runner", Version: buildinfo.String(), SilenceErrors: true, SilenceUsage: true, RunE: runRoot}

var serviceManagerFactory = func(configDir string, bootstrap servicemanager.BootstrapOptions) servicemanager.Manager {
	return servicemanager.ForCurrentPlatformWithBootstrap(configDir, bootstrap)
}

var loadServiceConfigSnapshot = runnerconfig.LoadFileSnapshot

type snapshotServiceRestarter interface {
	RestartWithConfig(context.Context, runnerconfig.Config) error
}

type snapshotServiceMatcher interface {
	ServiceMatchesConfig(context.Context, runnerconfig.Config) (bool, error)
}

func currentServiceManager() (servicemanager.Manager, string, error) {
	configDir, err := effectiveConfigDir()
	if err != nil {
		return nil, "", err
	}
	return serviceManagerFactory(configDir, servicemanager.BootstrapOptions{Image: bootstrapImage, PullPolicy: bootstrapPullPolicy, DashboardListen: dashboardListen}), configDir, nil
}

var waitForDashboardFunc = func(ctx context.Context) (string, error) {
	configDir, err := effectiveConfigDir()
	if err != nil {
		return "", err
	}
	metadata, err := waitForRunningController(ctx, configDir, "")
	if err != nil {
		return "", err
	}
	return metadata.PublicURL, nil
}

func runRoot(cmd *cobra.Command, _ []string) error {
	configDir, err := effectiveConfigDir()
	if err != nil {
		return err
	}
	coordinationCleanup, err := servicecoordination.StartPresence(cmd.Context(), configDir)
	if err != nil {
		return fmt.Errorf("publish attached host presence: %w", err)
	}
	defer coordinationCleanup()
	manager := serviceManagerFactory(configDir, servicemanager.BootstrapOptions{Image: bootstrapImage, PullPolicy: bootstrapPullPolicy, DashboardListen: dashboardListen})
	if err := startAttachedService(cmd.Context(), manager, configDir); err != nil {
		return err
	}
	url, err := waitForDashboardFunc(cmd.Context())
	if err != nil {
		return err
	}
	if dashboardOpen && dashboardCanOpenBrowser() {
		_ = openDashboardBrowserFunc(url)
	}
	cmd.Printf("Dashboard: %s\n", url)
	return followAttachedService(cmd.Context(), manager, configDir)
}

func startAttachedService(ctx context.Context, manager servicemanager.Manager, configDir string) error {
	return servicecoordination.WithServiceMutation(ctx, configDir, func() error {
		if err := servicecoordination.ResumeService(configDir); err != nil {
			return err
		}
		status, err := manager.Status(ctx)
		if err == nil && status.Running {
			return nil
		}
		return manager.Start(ctx)
	})
}

func followAttachedService(ctx context.Context, manager servicemanager.Manager, configDir string) error {
	for {
		if !servicecoordination.CoordinatorOwned(configDir) {
			return errors.New("attached Credimi Runner coordinator ownership was lost")
		}
		stopRequested, stopErr := servicecoordination.StopRequested(configDir)
		if stopErr != nil {
			return fmt.Errorf("read explicit service stop request: %w", stopErr)
		}
		if stopRequested {
			return nil
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
				ticker.Stop()
				return nil
			case <-logsDone:
				if ctx.Err() != nil {
					ticker.Stop()
					return nil
				}
				stopRequested, stopErr := servicecoordination.StopRequested(configDir)
				if stopErr != nil {
					ticker.Stop()
					return fmt.Errorf("read explicit service stop request: %w", stopErr)
				}
				if stopRequested {
					ticker.Stop()
					return nil
				}
				request, requestErr := servicecoordination.ReadRestartRequest(configDir)
				if requestErr == nil {
					result, resultErr := servicecoordination.ReadRestartResult(configDir)
					if resultErr != nil || result.RequestID != request.RequestID {
						if err := applyServiceRestartRequest(ctx, manager, configDir, request); err != nil {
							return err
						}
						restartFollower = true
						continue
					}
				}
				status, statusErr := manager.Status(ctx)
				if statusErr == nil && !status.Running {
					ticker.Stop()
					return nil
				}
				// A log stream can end while a service is being replaced or
				// while the service remains healthy. In either case, resume
				// following; an explicit stopped status is the external-stop
				// signal that ends the attached command.
				restartFollower = true
			case <-ticker.C:
				if !servicecoordination.CoordinatorOwned(configDir) {
					cancelLogs()
					<-logsDone
					ticker.Stop()
					return errors.New("attached Credimi Runner coordinator ownership was lost")
				}
				stopRequested, stopErr := servicecoordination.StopRequested(configDir)
				if stopErr != nil {
					cancelLogs()
					<-logsDone
					ticker.Stop()
					return fmt.Errorf("read explicit service stop request: %w", stopErr)
				}
				if stopRequested {
					cancelLogs()
					<-logsDone
					ticker.Stop()
					return nil
				}
				request, err := servicecoordination.ReadRestartRequest(configDir)
				if err == nil {
					if result, resultErr := servicecoordination.ReadRestartResult(configDir); resultErr != nil || result.RequestID != request.RequestID {
						cancelLogs()
						<-logsDone
						ticker.Stop()
						if err := applyServiceRestartRequest(ctx, manager, configDir, request); err != nil {
							return err
						}
						restartFollower = true
						continue
					}
				}
				status, err := manager.Status(ctx)
				if err != nil || status.Running {
					continue
				}
				cancelLogs()
				<-logsDone
				ticker.Stop()
				return nil
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
	stopRequested := func() (bool, error) {
		stopped, err := servicecoordination.StopRequested(configDir)
		if err != nil {
			return false, fmt.Errorf("read explicit service stop request: %w", err)
		}
		return stopped, nil
	}
	if stopped, err := stopRequested(); err != nil {
		return err
	} else if stopped {
		return writeResult(false, "", "service restart canceled by explicit service stop")
	}
	configPath := filepath.Join(configDir, "config.toml")
	cfg, digest, err := loadServiceConfigSnapshot(configPath)
	if err != nil {
		resultErr := writeResult(false, "", fmt.Sprintf("read saved configuration snapshot: %v", err))
		return errors.Join(err, resultErr)
	}
	if digest != request.RequestedConfigDigest {
		return writeResult(false, "", "service restart request was superseded by a newer configuration")
	}
	host, hostErr := servicemanager.ResolveHostContext(configDir)
	if hostErr != nil {
		return writeResult(false, "", fmt.Sprintf("resolve host service configuration: %v", hostErr))
	}
	host = servicemanager.ResolveServiceHostContext(cfg, host)
	expected := servicemanager.ServiceConfigFingerprintForHost(cfg, true, host)
	if !request.ForceRestart {
		if matcher, ok := manager.(snapshotServiceMatcher); ok {
			if matches, matchErr := matcher.ServiceMatchesConfig(ctx, cfg); matchErr == nil && matches {
				return writeResult(true, expected, "")
			}
		} else if status, statusErr := manager.Status(ctx); statusErr == nil && status.Running && !status.ServiceRestartRequired {
			return writeResult(true, expected, "")
		}
	}
	previous, _ := controller.ReadMetadata(configDir)
	restartSkipped := false
	restartErr := servicecoordination.WithServiceMutation(ctx, configDir, func() error {
		stopped, err := stopRequested()
		if err != nil {
			return err
		}
		if stopped {
			restartSkipped = true
			return nil
		}
		if restarter, ok := manager.(snapshotServiceRestarter); ok {
			return restarter.RestartWithConfig(ctx, cfg)
		}
		return manager.Restart(ctx)
	})
	if restartErr != nil {
		resultErr := writeResult(false, "", fmt.Sprintf("service restart failed: %v", restartErr))
		return errors.Join(restartErr, resultErr)
	}
	if restartSkipped {
		return writeResult(false, "", "service restart canceled by explicit service stop")
	}
	if stopped, err := stopRequested(); err != nil {
		return err
	} else if stopped {
		return writeResult(false, "", "service restart canceled by explicit service stop")
	}
	if _, err := waitForRunningControllerUsingWithTimeout(ctx, configDir, previous.IdentityToken, "", serviceApplyTimeout, controller.ReadMetadata, controller.Probe); err != nil {
		resultErr := writeResult(false, "", fmt.Sprintf("replacement service did not become ready: %v", err))
		return errors.Join(err, resultErr)
	}
	if matcher, ok := manager.(snapshotServiceMatcher); ok {
		matches, matchErr := matcher.ServiceMatchesConfig(ctx, cfg)
		if matchErr != nil {
			resultErr := writeResult(false, "", fmt.Sprintf("verify replacement service configuration: %v", matchErr))
			return errors.Join(matchErr, resultErr)
		}
		if !matches {
			err := errors.New("replacement service is not running with the requested configuration")
			resultErr := writeResult(false, "", err.Error())
			return errors.Join(err, resultErr)
		}
	} else {
		status, statusErr := manager.Status(ctx)
		if statusErr != nil {
			resultErr := writeResult(false, "", fmt.Sprintf("verify replacement service configuration: %v", statusErr))
			return errors.Join(statusErr, resultErr)
		}
		if !status.Running || status.ServiceRestartRequired {
			err := errors.New("replacement service is not running with the requested configuration")
			resultErr := writeResult(false, "", err.Error())
			return errors.Join(err, resultErr)
		}
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
	rootCmd.PersistentFlags().StringVar(&dashboardListen, "dashboard-listen", "", "Dashboard bind address for this start (default: 0.0.0.0:8051)")
}

func effectiveConfigDir() (string, error) {
	if strings.TrimSpace(dashboardConfigDir) != "" {
		return dashboardConfigDir, nil
	}
	if configPath != "" {
		return filepath.Dir(configPath), nil
	}
	return dashboard.ConfigDir()
}

func serviceNotRunningError() error {
	return fmt.Errorf("Credimi Runner service is not running. Start it with: credimi-runner service start")
}
