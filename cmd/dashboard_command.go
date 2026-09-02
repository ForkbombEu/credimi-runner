package cmd

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"
)

var dashboardCommand = &cobra.Command{
	Use:   "dashboard",
	Short: "Open the running Dashboard",
	RunE:  runDashboardCommand,
}

func init() {
	rootCmd.AddCommand(dashboardCommand)
}

func runDashboardCommand(cmd *cobra.Command, _ []string) error {
	metadata, err := verifiedController(cmd.Context(), effectiveConfigDir())
	if err != nil {
		return serviceNotRunningError()
	}
	url := strings.TrimRight(metadata.PublicURL, "/")
	if url == "" {
		return errors.New("controller metadata has no Dashboard URL")
	}
	if dashboardOpen && dashboardCanOpenBrowser() {
		if err := openDashboardBrowserFunc(url); err == nil {
			return nil
		}
	}
	cmd.Printf("Dashboard: %s\n", url)
	return nil
}
