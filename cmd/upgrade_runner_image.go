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

type runnerImageUpgradeManager interface {
	dashboardruntime.Manager
	UpgradeRunnerImage(context.Context, func(string)) error
}

var (
	upgradeRunnerExecutable = os.Executable
	newUpgradeRunnerManager = func(binaryPath, configDir string, values dashboardruntime.Values) runnerImageUpgradeManager {
		return dashboardruntime.NewLifecycleManager(binaryPath, configDir, values, nil)
	}
)

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
	binaryPath, err := upgradeRunnerExecutable()
	if err != nil {
		return fmt.Errorf("resolve runner executable: %w", err)
	}
	manager := newUpgradeRunnerManager(binaryPath, configDir, values)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
	defer cancel()

	out := cmd.OutOrStdout()
	printRunnerCLIHeader(out)
	progress := func(line string) {
		fmt.Fprintln(out, line)
	}
	if strings.EqualFold(strings.TrimSpace(values["CREDIMI_SERVICE_MODE"]), "auto") {
		manager.SetPublicURL("")
	}
	if err := manager.UpgradeRunnerImage(ctx, progress); err != nil {
		return err
	}
	progress("Waiting for the restarted runner to become ready.")
	if err := waitForDashboardRunnerReady(ctx, values); err != nil {
		return err
	}
	progress("Discovering the public URL and updating Credimi registration.")
	if err := registerDashboardRunner(ctx, manager, values); err != nil {
		return err
	}
	progress("Credimi registration updated.")
	return nil
}

func printRunnerCLIHeader(out io.Writer) {
	fmt.Fprint(out, runnerCLIHeader)
}

func init() {
	upgradeRunnerImageCmd.Flags().StringVar(&dashboardConfigDir, "config-dir", "", "Runner configuration directory")
	rootCmd.AddCommand(upgradeRunnerImageCmd)
}
