package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/maintenance"
	"github.com/spf13/cobra"
)

var (
	upgradeBinaryExecutable = os.Executable
	upgradeBinaryDownload   = maintenance.DownloadLatestBinary
	upgradeBinaryHTTPClient = http.DefaultClient
)

var upgradeRunnerBinaryCmd = &cobra.Command{
	Use:   "upgrade-runner-binary",
	Short: "Replace the runner binary with the latest available release",
	Args:  cobra.NoArgs,
	RunE:  runUpgradeRunnerBinary,
}

func runUpgradeRunnerBinary(cmd *cobra.Command, _ []string) error {
	configDir := dashboardConfigDir
	if configDir == "" {
		configDir = dashboard.ConfigDir()
	}
	store, err := dashboardruntime.LoadStore(configDir)
	if err != nil {
		return fmt.Errorf("load runner configuration: %w", err)
	}
	values, err := dashboardruntime.NormalizeValues(store.Snapshot(), currentDashboardGOOS())
	if err != nil {
		return fmt.Errorf("normalize runner configuration: %w", err)
	}
	executable, err := upgradeBinaryExecutable()
	if err != nil {
		return fmt.Errorf("resolve runner binary: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	manager := dashboardruntime.NewLifecycleManager(executable, configDir, values, nil)
	cmd.Println("Stopping runner services.")
	if err := manager.Stop(ctx); err != nil {
		return fmt.Errorf("stop runner: %w", err)
	}
	for label, address := range map[string]string{
		"runner":    net.JoinHostPort(normalizeListenHost(values["RUNNER_HOST"]), defaultString(values["RUNNER_PORT"], dashboardruntime.DefaultRunnerPort)),
		"dashboard": net.JoinHostPort(normalizeListenHost(values["DASHBOARD_HOST"]), defaultString(values["DASHBOARD_PORT"], dashboardruntime.DefaultDashboardPort)),
	} {
		if err := verifyAddressFree(address); err != nil {
			return fmt.Errorf("%s port must be free before upgrading: %w", label, err)
		}
	}
	progress := func(line string) { cmd.Println(line) }
	if err := upgradeBinaryDownload(ctx, upgradeBinaryHTTPClient, executable, progress); err != nil {
		return err
	}
	cmd.Printf("Runner binary upgrade complete: %s\n", executable)
	return nil
}

func verifyAddressFree(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("%s is already in use; find its process with `lsof -nP -iTCP:%s -sTCP:LISTEN` or `ss -ltnp`, then stop it with `kill <PID>`", address, portFromAddress(address))
	}
	return listener.Close()
}

func normalizeListenHost(host string) string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return "0.0.0.0"
	}
	return host
}
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
func portFromAddress(address string) string {
	_, port, _ := net.SplitHostPort(address)
	if _, err := strconv.Atoi(port); err != nil {
		return "PORT"
	}
	return port
}

func init() {
	upgradeRunnerBinaryCmd.Flags().StringVar(&dashboardConfigDir, "config-dir", "", "Runner configuration directory")
	rootCmd.AddCommand(upgradeRunnerBinaryCmd)
}
