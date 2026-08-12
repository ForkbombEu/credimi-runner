package androidtools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

type EmulatorProgress func(string)

// EnsureEmulatorReady is the single activation gate for configured Android
// emulators. It is idempotent and returns only after the executable, SDK
// packages, AVD, and Credimi golden assets are all usable.
func EnsureEmulatorReady(ctx context.Context, cfg runnerconfig.Config, goos string, progress EmulatorProgress) error {
	if !hasEnabledEmulator(cfg) {
		return EnsureRuntimeCapabilities(ctx, cfg, goos)
	}
	emulator, err := firstEnabledEmulator(cfg)
	if err != nil {
		return err
	}
	progressStage(progress, "Checking Android SDK")
	if err := ensureRuntimeCapabilitiesWithoutAVD(ctx, cfg, goos); err != nil {
		return err
	}
	avdHome := effectiveAVDHome(cfg)
	baseName := strings.TrimSpace(emulator.AndroidEmulator.BaseName)
	if baseName == "" {
		baseName = "credimi"
	}
	if !AVDAssetsExist(avdHome, baseName) {
		progressStage(progress, "Preparing emulator AVD")
		if err := DownloadAndExtractTarball(ctx, DefaultBaseAVDArchiveURL, avdHome, nil); err != nil {
			return fmt.Errorf("download base Android AVD: %w", err)
		}
	}
	if !AVDAssetsExist(avdHome, baseName) {
		if err := EnsureAVD(ctx, effectiveSDKRoot(cfg, goos), avdHome, baseName, emulator.AndroidEmulator.SystemImage, nil); err != nil {
			return fmt.Errorf("prepare Android AVD %q: %w", baseName, err)
		}
	}
	if !AVDAssetsExist(avdHome, baseName) {
		return fmt.Errorf("Android AVD %q is not present under %s", baseName, avdHome)
	}
	goldenRoot, goldenLeaf := effectiveGoldenPath(emulator.AndroidEmulator.GoldenSource, baseName)
	if !GoldenAssetsExist(goldenRoot, goldenLeaf) {
		progressStage(progress, "Preparing Credimi golden image")
		if err := DownloadAndExtractTarball(ctx, DefaultGoldenArchiveURL, goldenRoot, nil); err != nil {
			return fmt.Errorf("download Credimi golden image: %w", err)
		}
	}
	if !GoldenAssetsExist(goldenRoot, goldenLeaf) {
		return fmt.Errorf("Credimi golden assets %q are not present under %s", goldenLeaf, goldenRoot)
	}
	progressStage(progress, "Verifying emulator runtime")
	return verifyRuntimeCapabilities(effectiveSDKRoot(cfg, goos), goos, true, emulator.AndroidEmulator.SystemImage)
}

func ensureRuntimeCapabilitiesWithoutAVD(ctx context.Context, cfg runnerconfig.Config, goos string) error {
	return ensureRuntimeCapabilitiesAtWith(ctx, cfg, goos, "", EnsureCapabilities, false)
}

func hasEnabledEmulator(cfg runnerconfig.Config) bool {
	for _, device := range cfg.Devices {
		if device.Enabled && device.Type == runnerconfig.DeviceAndroidEmulator && device.AndroidEmulator != nil {
			return true
		}
	}
	return false
}

func firstEnabledEmulator(cfg runnerconfig.Config) (runnerconfig.DeviceConfig, error) {
	for _, device := range cfg.Devices {
		if device.Enabled && device.Type == runnerconfig.DeviceAndroidEmulator && device.AndroidEmulator != nil {
			return device, nil
		}
	}
	return runnerconfig.DeviceConfig{}, fmt.Errorf("no enabled Android emulator is configured")
}

func effectiveSDKRoot(cfg runnerconfig.Config, goos string) string {
	if root := strings.TrimSpace(os.Getenv("ANDROID_SDK_ROOT")); root != "" {
		return root
	}
	if goos == "darwin" {
		return filepath.Join(cfg.Storage.StateDir, "android", "sdk")
	}
	return "/opt/android-sdk"
}

func effectiveAVDHome(cfg runnerconfig.Config) string {
	for _, key := range []string{"ANDROID_AVD_HOME", "HOST_AVD_HOME_PATH"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return filepath.Join(cfg.Storage.StateDir, "android", "avd")
}

func effectiveGoldenPath(configured, baseName string) (string, string) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "/avd-golden", baseName + "-golden"
	}
	clean := filepath.Clean(configured)
	return filepath.Dir(clean), filepath.Base(clean)
}

func progressStage(progress EmulatorProgress, message string) {
	if progress != nil {
		progress(message)
	}
}
