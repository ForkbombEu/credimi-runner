package androidtools

import (
	"bufio"
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
	return EnsureEmulatorReadyAt(ctx, cfg, goos, effectiveSDKRoot(cfg, goos), progress)
}

// EnsureEmulatorReadyAt is the explicit-SDK-root form used by the runner
// entrypoint. The persistent SDK volume must be the same one used later by
// avdctl and the emulator process.
func EnsureEmulatorReadyAt(ctx context.Context, cfg runnerconfig.Config, goos, sdkRoot string, progress EmulatorProgress) error {
	sdkRoot = strings.TrimSpace(sdkRoot)
	if sdkRoot == "" {
		sdkRoot = effectiveSDKRoot(cfg, goos)
	}
	if !hasEnabledEmulator(cfg) {
		return EnsureRuntimeCapabilitiesAtWith(ctx, cfg, goos, sdkRoot, EnsureCapabilities)
	}
	emulator, err := firstEnabledEmulator(cfg)
	if err != nil {
		return err
	}
	progressStage(progress, "Checking Android SDK")
	if err := ensureRuntimeCapabilitiesWithoutAVDAt(ctx, cfg, goos, sdkRoot); err != nil {
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
	baseSystemImage, err := avdSystemImage(avdHome, baseName)
	if err != nil {
		return err
	}
	if !sdkPackageInstalled(sdkRoot, baseSystemImage) {
		progressStage(progress, "Installing Android system image")
	}
	if err := EnsureCapabilities(ctx, sdkRoot, true, baseSystemImage); err != nil {
		return fmt.Errorf("provision Android system image required by AVD %q: %w", baseName, err)
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
	return verifyRuntimeCapabilities(sdkRoot, goos, true, baseSystemImage)
}

func ensureRuntimeCapabilitiesWithoutAVDAt(ctx context.Context, cfg runnerconfig.Config, goos, sdkRoot string) error {
	return ensureRuntimeCapabilitiesAtWith(ctx, cfg, goos, sdkRoot, EnsureCapabilities, false)
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

// avdSystemImage returns the SDK package referenced by an existing AVD. AVD
// archives are authoritative here: cloning an AVD retains image.sysdir.1, so
// provisioning only the typed config's fallback image can leave the emulator
// unable to resolve its own system path.
func avdSystemImage(avdHome, avdName string) (string, error) {
	path := filepath.Join(avdHome, strings.TrimSpace(avdName)+".avd", "config.ini")
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read Android AVD %q configuration: %w", avdName, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if !found || strings.TrimSpace(key) != "image.sysdir.1" {
			continue
		}
		image := strings.Trim(strings.TrimSpace(value), "/\\")
		image = strings.ReplaceAll(image, "\\", "/")
		parts := strings.Split(image, "/")
		if len(parts) < 4 || parts[0] != "system-images" {
			return "", fmt.Errorf("Android AVD %q has invalid image.sysdir.1 %q", avdName, value)
		}
		for _, part := range parts {
			if strings.TrimSpace(part) == "" || part == "." || part == ".." {
				return "", fmt.Errorf("Android AVD %q has invalid image.sysdir.1 %q", avdName, value)
			}
		}
		return strings.Join(parts, ";"), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Android AVD %q configuration: %w", avdName, err)
	}
	return "", fmt.Errorf("Android AVD %q does not declare image.sysdir.1", avdName)
}

func progressStage(progress EmulatorProgress, message string) {
	if progress != nil {
		progress(message)
	}
}
