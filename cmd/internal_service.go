package cmd

import (
	"os/signal"
	"syscall"

	"github.com/forkbombeu/credimi-runner/internal/application"
	"github.com/spf13/cobra"
)

var internalServiceCmd = &cobra.Command{Use: "internal-service", Hidden: true, RunE: runInternalService}

func init() { rootCmd.AddCommand(internalServiceCmd) }
func runInternalService(cmd *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	configDir, err := effectiveConfigDir()
	if err != nil {
		return err
	}
	app, err := application.New(configDir, nil)
	if err != nil {
		return err
	}
	return app.Run(ctx)
}
