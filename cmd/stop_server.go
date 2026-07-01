package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/spf13/cobra"
)

var stopServerCmd = &cobra.Command{
	Use:   "stop-server",
	Short: "Stop the detached runner server",
	RunE:  runStopServer,
}

func init() {
	rootCmd.AddCommand(stopServerCmd)
}

func runStopServer(cmd *cobra.Command, args []string) error {
	configDir := dashboardConfigDir
	if configDir == "" {
		configDir = dashboard.ConfigDir()
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	store, err := dashboardruntime.LoadStore(configDir)
	if err != nil {
		return err
	}
	values, err := dashboardruntime.NormalizeValues(store.Snapshot(), currentDashboardGOOS())
	if err != nil {
		return err
	}
	if err := dashboardruntime.StopRunnerServer(ctx, configDir, values); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("runner server was not found from %s or the configured runner port", dashboardruntime.RunnerPIDPath(configDir))
		}
		return err
	}
	cmd.Printf("Stopped runner server from %s\n", dashboardruntime.RunnerPIDPath(configDir))
	return nil
}
