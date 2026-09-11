package cmd

import (
	"context"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/servicecoordination"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
	"github.com/spf13/cobra"
)

var serviceCmd = &cobra.Command{Use: "service", Short: "Control the persistent Credimi Runner service"}

func init() {
	serviceCmd.AddCommand(
		serviceAction("start", startService),
		serviceAction("stop", stopService),
		serviceAction("restart", restartService),
		serviceAction("enable", func(ctx context.Context, m servicemanager.Manager, _ string) error { return m.Enable(ctx) }),
		serviceAction("disable", func(ctx context.Context, m servicemanager.Manager, _ string) error { return m.Disable(ctx) }),
		&cobra.Command{Use: "status", RunE: runServiceStatus},
	)
	rootCmd.AddCommand(serviceCmd)
}

func serviceAction(name string, action func(context.Context, servicemanager.Manager, string) error) *cobra.Command {
	return &cobra.Command{Use: name, RunE: func(cmd *cobra.Command, _ []string) error {
		manager, configDir, err := currentServiceManager()
		if err != nil {
			return err
		}
		return action(cmd.Context(), manager, configDir)
	}}
}

func startService(ctx context.Context, manager servicemanager.Manager, configDir string) error {
	return servicecoordination.WithServiceMutation(ctx, configDir, func() error {
		if err := servicecoordination.ResumeService(configDir); err != nil {
			return err
		}
		return manager.Start(ctx)
	})
}

func restartService(ctx context.Context, manager servicemanager.Manager, configDir string) error {
	return servicecoordination.WithServiceMutation(ctx, configDir, func() error {
		if err := servicecoordination.ResumeService(configDir); err != nil {
			return err
		}
		return manager.Restart(ctx)
	})
}

func stopService(ctx context.Context, manager servicemanager.Manager, configDir string) error {
	if err := servicecoordination.RequestStop(configDir, time.Now()); err != nil {
		return err
	}
	return servicecoordination.WithServiceMutation(ctx, configDir, func() error {
		if err := servicecoordination.CancelRestartRequest(configDir); err != nil {
			return err
		}
		return manager.Stop(ctx)
	})
}

func runServiceStatus(cmd *cobra.Command, _ []string) error {
	manager, _, err := currentServiceManager()
	if err != nil {
		return err
	}
	status, err := manager.Status(cmd.Context())
	if err != nil {
		return err
	}
	state := "stopped"
	if status.Running {
		state = "running"
	}
	autostart := "disabled"
	if status.Autostart {
		autostart = "enabled"
	}
	cmd.Printf("Service: %s\nAutostart: %s\nDashboard: %s\nRuntime desired: %s\nRuntime actual: %s\nService restart required: %t\n", state, autostart, status.DashboardURL, status.RuntimeDesired, status.RuntimeActual, status.ServiceRestartRequired)
	if status.RuntimeError != "" {
		cmd.Printf("Runtime error: %s\n", status.RuntimeError)
	}
	return nil
}
