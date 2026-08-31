package cmd

import (
	"github.com/spf13/cobra"
)

var dashboardCommand = &cobra.Command{
	Use:   "dashboard",
	Short: "Open the running Dashboard",
	RunE:  runDashboardCommand,
}

func init() {
	// Keep status as a hidden diagnostic subcommand; dashboard itself is the
	// public command for opening the already-running service UI.
	lifecycleDashboardStatusCmd.Hidden = true
	dashboardCommand.AddCommand(lifecycleDashboardStatusCmd)
	rootCmd.AddCommand(dashboardCommand)
}

func runDashboardCommand(cmd *cobra.Command, _ []string) error {
	metadata, err := requireRunningController(cmd.Context())
	if err != nil {
		return err
	}
	url := controllerBaseURL(metadata)
	if dashboardOpen && dashboardCanOpenBrowser() {
		if err := openDashboardBrowserFunc(url); err == nil {
			return nil
		}
	}
	cmd.Printf("Dashboard: %s\n", url)
	return nil
}
