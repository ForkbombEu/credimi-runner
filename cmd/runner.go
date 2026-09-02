package cmd

import (
	"context"
	"fmt"
	stdlog "log"
	"path/filepath"
	"strings"

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

func currentServiceManager() servicemanager.Manager {
	return serviceManagerFactory(effectiveConfigDir(), servicemanager.BootstrapOptions{Image: bootstrapImage, PullPolicy: bootstrapPullPolicy})
}

var waitForDashboardFunc = func(ctx context.Context, _ servicemanager.Manager) (string, error) {
	metadata, err := waitForRunningController(ctx, effectiveConfigDir(), "")
	if err != nil {
		return "", err
	}
	return metadata.PublicURL, nil
}

func runRoot(cmd *cobra.Command, _ []string) error {
	manager := currentServiceManager()
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
	err = manager.Logs(cmd.Context(), servicemanager.LogOptions{Follow: true, Lines: 200})
	if err == nil || cmd.Context().Err() != nil {
		return nil
	}
	return err
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
