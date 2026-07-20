package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/controller"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/maintenance"
	"github.com/spf13/cobra"
)

type runnerImageUpgrader interface {
	UpgradeRunnerImage(context.Context, func(string)) error
}

var (
	upgradeBinaryExecutable = os.Executable
	upgradeBinaryDownload   = maintenance.DownloadLatestBinary
	upgradeBinaryHTTPClient = http.DefaultClient
)

const runnerCLIHeader = `
  ____              _ _           _   ____
 / ___|_ __ ___  __| (_)_ __ ___ (_) |  _ \ _   _ _ __  _ __   ___ _ __
| |   | '__/ _ \/ _  | | '_  _ \| | | |_) | | | | '_ \| '_ \ / _ \ '__|
| |___| | |  __/ (_| | | | | | | | | |  _ <| |_| | | | | | | |  __/ |
 \____|_|  \___|\__,_|_|_| |_| |_|_| |_| \_\\__,_|_| |_| |_|_|  \___|_|
`

var upgradeImageCmd = &cobra.Command{
	Use:   "upgrade-image",
	Short: "Replace the runner Docker image with the latest available image",
	Args:  cobra.NoArgs,
	RunE:  runUpgradeImage,
}

var upgradeBinaryCmd = &cobra.Command{
	Use:   "upgrade-binary",
	Short: "Replace the Credimi Runner binary with the latest available release",
	Args:  cobra.NoArgs,
	RunE:  runUpgradeBinary,
}

func init() {
	rootCmd.AddCommand(upgradeImageCmd, upgradeBinaryCmd)
}

func runUpgradeImage(cmd *cobra.Command, _ []string) error {
	printRunnerCLIHeader(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
	defer cancel()
	metadata, err := controller.ReadMetadata(lifecycleConfigDir())
	if err == nil && controller.Probe(ctx, metadata) == nil {
		baseURL := controllerBaseURL(metadata)
		return runLifecycleDashboardEndpointAction(ctx, cmd, baseURL, baseURL+"/api/controller/maintenance/upgrade-image", "Runner image upgraded", "runner image upgrade")
	}

	lease, err := controller.Acquire(lifecycleConfigDir())
	if err != nil {
		if errors.Is(err, controller.ErrAlreadyRunning) {
			return errors.New("dashboard controller is active but could not be verified; refusing direct runner image upgrade")
		}
		return err
	}
	defer lease.Close()
	manager, values, closeManager, err := lifecycleDirectManager()
	if err != nil {
		return err
	}
	defer closeManager()
	upgrader, ok := manager.(runnerImageUpgrader)
	if !ok {
		return errors.New("runner image upgrade is unavailable")
	}
	progress := func(message string) { cmd.Println(message) }
	if err := upgrader.UpgradeRunnerImage(ctx, progress); err != nil {
		return fmt.Errorf("runner image upgrade failed: %w", err)
	}
	lifecycle := controller.RuntimeLifecycle{Manager: manager, Values: values, GOOS: runtime.GOOS, WaitReady: lifecycleRuntimeWaitReady}
	if err := lifecycle.RegisterRunning(ctx); err != nil {
		return fmt.Errorf("runner image upgraded, but Credimi registration failed: %w", err)
	}
	cmd.Println("Runner image upgraded successfully.")
	return nil
}

func runUpgradeBinary(cmd *cobra.Command, _ []string) error {
	printRunnerCLIHeader(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	defer cancel()
	lease, err := controller.Acquire(lifecycleConfigDir())
	if err != nil {
		if errors.Is(err, controller.ErrAlreadyRunning) {
			return errors.New("stop the dashboard before upgrading its binary")
		}
		return err
	}
	defer lease.Close()
	manager, values, closeManager, err := lifecycleDirectManager()
	if err != nil {
		return err
	}
	defer closeManager()
	executable, err := upgradeBinaryExecutable()
	if err != nil {
		return fmt.Errorf("resolve runner binary: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	cmd.Println("Stopping runner services.")
	if err := manager.Stop(ctx); err != nil {
		return fmt.Errorf("stop runner: %w", err)
	}
	for label, address := range map[string]string{
		"runner":    net.JoinHostPort(normalizeUpgradeListenHost(values["RUNNER_HOST"]), defaultUpgradeString(values["RUNNER_PORT"], dashboardruntime.DefaultRunnerPort)),
		"dashboard": net.JoinHostPort(normalizeUpgradeListenHost(values["DASHBOARD_HOST"]), defaultUpgradeString(values["DASHBOARD_PORT"], dashboardruntime.DefaultDashboardPort)),
	} {
		if err := verifyUpgradeAddressFree(address); err != nil {
			return fmt.Errorf("%s port must be free before upgrading: %w", label, err)
		}
	}
	if err := upgradeBinaryDownload(ctx, upgradeBinaryHTTPClient, executable, func(message string) { cmd.Println(message) }); err != nil {
		return err
	}
	cmd.Printf("Runner binary upgraded successfully: %s\n", executable)
	return nil
}

func printRunnerCLIHeader(cmd *cobra.Command) {
	_, _ = fmt.Fprint(cmd.OutOrStdout(), runnerCLIHeader)
}

func verifyUpgradeAddressFree(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("%s is already in use; stop the process before upgrading", address)
	}
	return listener.Close()
}

func normalizeUpgradeListenHost(host string) string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return "0.0.0.0"
	}
	return host
}

func defaultUpgradeString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
