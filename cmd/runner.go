package cmd

import (
	"context"
	"fmt"
	stdlog "log"
	"path/filepath"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	"github.com/forkbombeu/credimi-runner/internal/dashboard"
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

var waitForDashboardFunc = func(ctx context.Context, manager servicemanager.Manager) (string, error) {
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		status, err := manager.Status(deadline)
		if err == nil && strings.TrimSpace(status.DashboardURL) != "" {
			return status.DashboardURL, nil
		}
		select {
		case <-deadline.Done():
			if err != nil {
				return "", err
			}
			return "", deadline.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func runRoot(cmd *cobra.Command, _ []string) error {
	manager := serviceManagerFactory(effectiveConfigDir(), servicemanager.BootstrapOptions{Image: bootstrapImage, PullPolicy: bootstrapPullPolicy})
	status, err := manager.Status(cmd.Context())
	if err != nil || !status.Running {
		if err := manager.Start(cmd.Context()); err != nil {
			return err
		}
	}
	url, err := waitForDashboardFunc(cmd.Context(), manager)
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
	return manager.Logs(cmd.Context(), servicemanager.LogOptions{Follow: true, Lines: 200})
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
