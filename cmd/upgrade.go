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

const serviceApplyTimeout = 15 * time.Minute

var upgradeBinaryCmd = &cobra.Command{Use: "upgrade-binary", Short: "Upgrade the host Credimi Runner CLI binary", RunE: runUpgradeBinary}
var upgradeImageCmd = &cobra.Command{Use: "upgrade-image", Short: "Upgrade the persistent Docker service image", RunE: runUpgradeImage}

func init() { rootCmd.AddCommand(upgradeBinaryCmd, upgradeImageCmd) }

func runUpgradeBinary(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
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
	var previousIdentity string
	if runtime.GOOS == "darwin" {
		status, err := manager.Status(ctx)
		if err != nil {
			return err
		}
		running = status.Running
		if running {
			metadata, err := verifiedController(ctx, effectiveConfigDir())
			if err != nil {
				return err
			}
			previousIdentity = metadata.IdentityToken
		}
	}
	if err := downloadLatestBinary(ctx, http.DefaultClient, executable, func(message string) { cmd.Println(message) }); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		cmd.Println("Credimi Runner CLI upgraded successfully. The persistent Docker service image was not changed; use `credimi-runner upgrade-image` to upgrade it.")
		return nil
	}
	if !running {
		return nil
	}
	if err := manager.Restart(ctx); err != nil {
		return fmt.Errorf("binary was upgraded but persistent service restart failed: %w", err)
	}
	_, err = waitForRunningController(ctx, effectiveConfigDir(), previousIdentity)
	return err
}

func runUpgradeImage(cmd *cobra.Command, _ []string) error {
	if runtime.GOOS == "darwin" {
		return errors.New("runner service image upgrade is only available for the Docker-backed persistent service")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	manager := currentServiceManager()
	status, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	var previousIdentity string
	if status.Running {
		metadata, err := verifiedController(ctx, effectiveConfigDir())
		if err != nil {
			return err
		}
		previousIdentity = metadata.IdentityToken
	}
	upgrader, ok := manager.(servicemanager.ImageUpgrader)
	if !ok {
		return errors.New("runner service image upgrade is only available for the Docker-backed persistent service")
	}
	if err := upgrader.UpgradeImage(ctx, func(message string) { cmd.Println(message) }); err != nil {
		return err
	}
	if !status.Running {
		return nil
	}
	_, err = waitForRunningController(ctx, effectiveConfigDir(), previousIdentity)
	return err
}

func waitForRunningController(ctx context.Context, configDir, previousIdentityToken string) (controller.Metadata, error) {
	return waitForRunningControllerUsing(ctx, configDir, previousIdentityToken, controller.ReadMetadata, controller.Probe)
}

func waitForRunningControllerUsing(
	ctx context.Context,
	configDir, previousIdentityToken string,
	readMetadata func(string) (controller.Metadata, error),
	probe func(context.Context, controller.Metadata) error,
) (controller.Metadata, error) {
	return waitForRunningControllerUsingWithTimeout(ctx, configDir, previousIdentityToken, "", 30*time.Second, readMetadata, probe)
}

func waitForRunningControllerUsingWithTimeout(
	ctx context.Context,
	configDir, previousIdentityToken, expectedFingerprint string,
	maximum time.Duration,
	readMetadata func(string) (controller.Metadata, error),
	probe func(context.Context, controller.Metadata) error,
) (controller.Metadata, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline, cancel := context.WithTimeout(ctx, maximum)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		metadata, err := readMetadata(configDir)
		if err == nil && (previousIdentityToken == "" || metadata.IdentityToken != previousIdentityToken) && (expectedFingerprint == "" || metadata.ConfigFingerprint == expectedFingerprint) {
			probeCtx, probeCancel := context.WithTimeout(deadline, controller.ProbeTimeout)
			err = probe(probeCtx, metadata)
			probeCancel()
			if err == nil {
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
