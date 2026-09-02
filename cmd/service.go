package cmd

import (
	"context"

	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
	"github.com/spf13/cobra"
)

var serviceCmd = &cobra.Command{Use: "service", Short: "Control the persistent Credimi Runner service"}

func init() {
	serviceCmd.AddCommand(
		serviceAction("start", func(ctx context.Context, m servicemanager.Manager) error { return m.Start(ctx) }),
		serviceAction("stop", func(ctx context.Context, m servicemanager.Manager) error { return m.Stop(ctx) }),
		serviceAction("restart", func(ctx context.Context, m servicemanager.Manager) error { return m.Restart(ctx) }),
		&cobra.Command{Use: "status", RunE: runServiceStatus},
	)
	rootCmd.AddCommand(serviceCmd)
}

func serviceAction(name string, action func(context.Context, servicemanager.Manager) error) *cobra.Command {
	return &cobra.Command{Use: name, RunE: func(cmd *cobra.Command, _ []string) error {
		return action(cmd.Context(), currentServiceManager())
	}}
}

func runServiceStatus(cmd *cobra.Command, _ []string) error {
	status, err := currentServiceManager().Status(cmd.Context())
	if err != nil {
		return err
	}
	state := "stopped"
	if status.Running {
		state = "running"
	}
	cmd.Printf("Service: %s\nDashboard: %s\nRuntime desired: %s\nRuntime actual: %s\nService restart required: %t\n", state, status.DashboardURL, status.RuntimeDesired, status.RuntimeActual, status.ServiceRestartRequired)
	if status.RuntimeError != "" {
		cmd.Printf("Runtime error: %s\n", status.RuntimeError)
	}
	return nil
}
