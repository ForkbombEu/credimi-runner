package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/spf13/cobra"
)

const runnerCLIHeader = `
  ____              _ _           _   ____
 / ___|_ __ ___  __| (_)_ __ ___ (_) |  _ \ _   _ _ __  _ __   ___ _ __
| |   | '__/ _ \/ _  | | '_  _ \| | | |_) | | | | '_ \| '_ \ / _ \ '__|
| |___| | |  __/ (_| | | | | | | | | |  _ <| |_| | | | | | | |  __/ |
 \____|_|  \___|\__,_|_|_| |_| |_|_| |_| \_\\__,_|_| |_|_| |_|\___|_|
`

var upgradeRunnerImageCmd = &cobra.Command{
	Use:   "upgrade-runner-image",
	Short: "Replace the runner Docker image with the latest available image",
	Args:  cobra.NoArgs,
	RunE:  runUpgradeRunnerImage,
}

func runUpgradeRunnerImage(cmd *cobra.Command, _ []string) error {
	configDir := dashboardConfigDir
	if strings.TrimSpace(configDir) == "" {
		configDir = dashboard.ConfigDir()
	}
	store, err := dashboardruntime.LoadStore(configDir)
	if err != nil {
		return fmt.Errorf("load runner configuration: %w", err)
	}
	values, err := dashboardruntime.NormalizeValues(store.Snapshot(), runtime.GOOS)
	if err != nil {
		return fmt.Errorf("normalize runner configuration: %w", err)
	}
	binaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve runner executable: %w", err)
	}
	manager := dashboardruntime.NewLifecycleManager(binaryPath, configDir, values, nil)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
	defer cancel()

	out := cmd.OutOrStdout()
	printRunnerCLIHeader(out)
	return manager.UpgradeRunnerImage(ctx, func(line string) {
		fmt.Fprintln(out, line)
	})
}

func printRunnerCLIHeader(out io.Writer) {
	fmt.Fprint(out, runnerCLIHeader)
}

func init() {
	upgradeRunnerImageCmd.Flags().StringVar(&dashboardConfigDir, "config-dir", "", "Runner configuration directory")
	rootCmd.AddCommand(upgradeRunnerImageCmd)
}
