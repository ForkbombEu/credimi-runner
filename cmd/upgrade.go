package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/maintenance"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
	"github.com/spf13/cobra"
)

var downloadLatestBinary = maintenance.DownloadLatestBinary

var upgradeBinaryCmd = &cobra.Command{Use: "upgrade-binary", Short: "Upgrade the host Credimi Runner CLI binary", RunE: runUpgradeBinary}
var upgradeImageCmd = &cobra.Command{Use: "upgrade-image", Short: "Upgrade the persistent Docker service image", RunE: runUpgradeImage}

func init() { rootCmd.AddCommand(upgradeBinaryCmd, upgradeImageCmd) }

func runUpgradeBinary(cmd *cobra.Command, _ []string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	manager := currentServiceManager()
	running := false
	if runtime.GOOS == "darwin" {
		status, err := manager.Status(cmd.Context())
		if err != nil {
			return err
		}
		running = status.Running
	}
	if err := downloadLatestBinary(cmd.Context(), http.DefaultClient, executable, func(message string) { cmd.Println(message) }); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		cmd.Println("Credimi Runner CLI upgraded successfully. The persistent Docker service image was not changed; use `credimi-runner upgrade-image` to upgrade it.")
		return nil
	}
	if !running {
		return nil
	}
	if err := manager.Restart(cmd.Context()); err != nil {
		return fmt.Errorf("binary was upgraded but persistent service restart failed: %w", err)
	}
	_, err = waitForRunningController(cmd.Context(), effectiveConfigDir(), "")
	return err
}

func runUpgradeImage(cmd *cobra.Command, _ []string) error {
	upgrader, ok := currentServiceManager().(servicemanager.ImageUpgrader)
	if !ok {
		return errors.New("runner service image upgrade is only available for the Docker-backed persistent service")
	}
	return upgrader.UpgradeImage(cmd.Context(), func(message string) { cmd.Println(message) })
}

func waitForRunningController(ctx context.Context, configDir, previousIdentityToken string) (controller.Metadata, error) {
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		metadata, err := controller.ReadMetadata(configDir)
		if err == nil && (previousIdentityToken == "" || metadata.IdentityToken != previousIdentityToken) {
			if err = controller.Probe(deadline, metadata); err == nil {
				return metadata, nil
			}
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-deadline.Done():
			if lastErr != nil {
				return controller.Metadata{}, fmt.Errorf("wait for running controller: %w", lastErr)
			}
			return controller.Metadata{}, deadline.Err()
		case <-ticker.C:
		}
	}
}
