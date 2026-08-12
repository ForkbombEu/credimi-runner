// Package androidtools provisions the mutable Android SDK assets shared by
// every device in the runner container.
package androidtools

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

type CommandRunner func(context.Context, string, ...string) error

const commandLineToolsVersion = "11076708"

var (
	androidToolsGOOS     = runtime.GOOS
	androidToolsHTTP     = http.DefaultClient
	androidToolsDownload = downloadCommandLineTools
)

type CapabilityEnsurer func(context.Context, string, bool, string) error

// EnsureRuntimeCapabilities provisions only the Android capabilities needed by
// the current typed inventory. It is safe to call after every dashboard save.
func EnsureRuntimeCapabilities(ctx context.Context, cfg runnerconfig.Config, goos string) error {
	return EnsureRuntimeCapabilitiesAtWith(ctx, cfg, goos, "", EnsureCapabilities)
}

func EnsureRuntimeCapabilitiesAtWith(ctx context.Context, cfg runnerconfig.Config, goos, sdkRoot string, ensure CapabilityEnsurer) error {
	return ensureRuntimeCapabilitiesAtWith(ctx, cfg, goos, sdkRoot, ensure, true)
}

func ensureRuntimeCapabilitiesAtWith(ctx context.Context, cfg runnerconfig.Config, goos, sdkRoot string, ensure CapabilityEnsurer, ensureAVD bool) error {
	useDefaultEnsurer := ensure == nil
	if ensure == nil {
		ensure = EnsureCapabilities
	}
	needsAndroid, needsEmulator, systemImage := requiredCapabilities(cfg)
	if !needsAndroid {
		return nil
	}
	sdkRoot = strings.TrimSpace(sdkRoot)
	if sdkRoot == "" {
		sdkRoot = strings.TrimSpace(os.Getenv("ANDROID_SDK_ROOT"))
	}
	if sdkRoot == "" {
		if goos == "darwin" {
			sdkRoot = filepath.Join(cfg.Storage.StateDir, "android", "sdk")
		} else {
			sdkRoot = "/opt/android-sdk"
		}
	}
	if err := ensure(ctx, sdkRoot, needsEmulator, systemImage); err != nil {
		return err
	}
	avdHome := strings.TrimSpace(os.Getenv("ANDROID_AVD_HOME"))
	if avdHome == "" {
		avdHome = strings.TrimSpace(os.Getenv("HOST_AVD_HOME_PATH"))
	}
	if avdHome == "" {
		avdHome = filepath.Join(cfg.Storage.StateDir, "android", "avd")
	}
	if err := ConfigureStableEnvironmentWithAVD(sdkRoot, avdHome); err != nil {
		return err
	}
	if useDefaultEnsurer {
		if err := verifyRuntimeCapabilities(sdkRoot, goos, needsEmulator, systemImage); err != nil {
			return err
		}
	}
	if !needsEmulator || !ensureAVD {
		return nil
	}
	for _, device := range cfg.Devices {
		if !device.Enabled || device.Type != runnerconfig.DeviceAndroidEmulator || device.AndroidEmulator == nil {
			continue
		}
		if err := EnsureAVD(ctx, sdkRoot, avdHome, device.AndroidEmulator.AVDName, device.AndroidEmulator.SystemImage, nil); err != nil {
			return err
		}
	}
	return nil
}

func requiredCapabilities(cfg runnerconfig.Config) (needsAndroid, needsEmulator bool, systemImage string) {
	for _, device := range cfg.Devices {
		if !device.Enabled {
			continue
		}
		switch device.Type {
		case runnerconfig.DeviceAndroidPhysical, runnerconfig.DeviceRedroid:
			needsAndroid = true
		case runnerconfig.DeviceAndroidEmulator:
			needsAndroid = true
			needsEmulator = true
			if device.AndroidEmulator != nil && strings.TrimSpace(device.AndroidEmulator.SystemImage) != "" {
				systemImage = device.AndroidEmulator.SystemImage
			}
		}
	}
	return needsAndroid, needsEmulator, systemImage
}

func Ensure(ctx context.Context, sdkRoot string) error {
	return EnsureCapabilities(ctx, sdkRoot, true, "")
}

func EnsureCapabilities(ctx context.Context, sdkRoot string, needsEmulator bool, systemImage string) error {
	return EnsureCapabilitiesWithRunner(ctx, sdkRoot, needsEmulator, systemImage, func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	})
}

// EnsureWithRunner is injectable so provisioning decisions remain testable
// without downloading SDK packages.
func EnsureWithRunner(ctx context.Context, sdkRoot string, run CommandRunner) error {
	return EnsureCapabilitiesWithRunner(ctx, sdkRoot, true, "", run)
}

// EnsureCapabilitiesWithRunner provisions only the Android SDK capabilities
// required by the configured inventory. Platform-tools are common; the
// emulator package and system image are installed only for emulator targets.
func EnsureCapabilitiesWithRunner(ctx context.Context, sdkRoot string, needsEmulator bool, systemImage string, run CommandRunner) error {
	if sdkRoot == "" {
		return fmt.Errorf("Android SDK root is required")
	}
	if err := os.MkdirAll(sdkRoot, 0o755); err != nil {
		return fmt.Errorf("create Android SDK root: %w", err)
	}
	if err := ensureSDKLicenses(sdkRoot); err != nil {
		return err
	}
	manager, err := ensureSDKManager(ctx, sdkRoot)
	if err != nil {
		return err
	}
	packages := []string{}
	if !platformToolsAvailable(sdkRoot) {
		packages = append(packages, "platform-tools")
	}
	if needsEmulator {
		if !emulatorAvailable(sdkRoot) {
			packages = append(packages, "emulator")
		}
		if image := strings.TrimSpace(systemImage); image != "" && !sdkPackageInstalled(sdkRoot, image) {
			packages = append(packages, image)
		}
	}
	if len(packages) == 0 {
		return nil
	}
	args := append([]string{"--sdk_root=" + sdkRoot}, packages...)
	if err := run(ctx, manager, args...); err != nil {
		return fmt.Errorf("install Android SDK packages %v: %w", packages, err)
	}
	return nil
}

func emulatorAvailable(sdkRoot string) bool {
	// System images live in the mutable SDK volume. The emulator executable
	// must live there too: a bootstrap-only emulator can select its own SDK
	// directory and fail to resolve a valid system image from the volume.
	return fileExists(filepath.Join(sdkRoot, "emulator", "emulator"))
}

func verifyRuntimeCapabilities(sdkRoot, goos string, needsEmulator bool, systemImage string) error {
	if !needsEmulator {
		return nil
	}
	if !emulatorAvailable(sdkRoot) {
		return errors.New("Android emulator executable is unavailable in the persistent SDK")
	}
	if !platformToolsAvailable(sdkRoot) {
		return errors.New("Android platform-tools are unavailable in the persistent SDK")
	}
	if _, err := exec.LookPath("emulator"); err != nil {
		return fmt.Errorf("Android emulator is not resolvable from PATH: %w", err)
	}
	if strings.TrimSpace(systemImage) != "" && !sdkPackageInstalled(sdkRoot, systemImage) {
		return fmt.Errorf("Android system image %q is unavailable under %s", systemImage, sdkRoot)
	}
	if !sdkLicensesAvailable(sdkRoot) {
		return errors.New("Android SDK licenses are unavailable")
	}
	if goos == "linux" {
		if _, err := os.Stat("/dev/kvm"); err != nil {
			return fmt.Errorf("/dev/kvm is required for Android emulator targets: %w", err)
		}
	}
	return nil
}

// ConfigureStableEnvironmentWithAVD adds runner-owned SDK and emulator paths
// once at startup. The AVD directory may be supplied by the typed dashboard
// compatibility adapter when existing Credimi assets use another location.
func ConfigureStableEnvironmentWithAVD(sdkRoot, avdHome string) error {
	if strings.TrimSpace(sdkRoot) == "" {
		return fmt.Errorf("Android SDK root is required")
	}
	if err := os.Setenv("ANDROID_SDK_ROOT", sdkRoot); err != nil {
		return err
	}
	if err := os.Setenv("ANDROID_HOME", sdkRoot); err != nil {
		return err
	}
	if strings.TrimSpace(avdHome) != "" {
		if err := os.Setenv("ANDROID_AVD_HOME", avdHome); err != nil {
			return err
		}
	}
	paths := []string{
		filepath.Join(sdkRoot, "cmdline-tools", "latest", "bin"),
		filepath.Join(sdkRoot, "platform-tools"),
		filepath.Join(sdkRoot, "emulator"),
	}
	if bootstrap := strings.TrimSpace(os.Getenv("ANDROID_SDK_BOOTSTRAP")); bootstrap != "" {
		paths = append(paths,
			filepath.Join(bootstrap, "cmdline-tools", "latest", "bin"),
			filepath.Join(bootstrap, "platform-tools"),
			filepath.Join(bootstrap, "emulator"),
		)
	}
	current := os.Getenv("PATH")
	// Prepending in reverse retains the declared priority: mutable runtime SDK
	// tools must win over immutable bootstrap fallbacks.
	for index := len(paths) - 1; index >= 0; index-- {
		path := paths[index]
		if !strings.Contains(string(filepath.ListSeparator)+current+string(filepath.ListSeparator), string(filepath.ListSeparator)+path+string(filepath.ListSeparator)) {
			current = path + string(filepath.ListSeparator) + current
		}
	}
	return os.Setenv("PATH", current)
}

// EnsureAVD creates the configured local AVD only when its persistent assets
// are absent. Existing dashboard-downloaded AVDs are reused unchanged.
func EnsureAVD(ctx context.Context, sdkRoot, avdHome, avdName, systemImage string, run CommandRunner) error {
	avdName = strings.TrimSpace(avdName)
	systemImage = strings.TrimSpace(systemImage)
	if avdName == "" || systemImage == "" {
		return fmt.Errorf("Android emulator AVD name and system image are required")
	}
	if strings.TrimSpace(avdHome) == "" {
		avdHome = filepath.Join(filepath.Dir(sdkRoot), "avd")
	}
	if fileExists(filepath.Join(avdHome, avdName+".ini")) && fileExists(filepath.Join(avdHome, avdName+".avd")) {
		return nil
	}
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		}
	}
	manager := filepath.Join(sdkRoot, "cmdline-tools", "latest", "bin", "avdmanager")
	if _, err := os.Stat(manager); err != nil {
		if found, lookErr := exec.LookPath("avdmanager"); lookErr == nil {
			manager = found
		} else {
			return fmt.Errorf("Android avdmanager is unavailable: %w", err)
		}
	}
	if err := os.MkdirAll(avdHome, 0o755); err != nil {
		return fmt.Errorf("create Android AVD home: %w", err)
	}
	if err := run(ctx, manager, "create", "avd", "--force", "--name", avdName, "--package", systemImage, "--device", "pixel"); err != nil {
		return fmt.Errorf("create Android AVD %q: %w", avdName, err)
	}
	return nil
}

func ensureSDKManager(ctx context.Context, sdkRoot string) (string, error) {
	candidates := []string{
		filepath.Join(sdkRoot, "cmdline-tools", "latest", "bin", "sdkmanager"),
	}
	if bootstrap := strings.TrimSpace(os.Getenv("ANDROID_SDK_BOOTSTRAP")); bootstrap != "" {
		candidates = append(candidates, filepath.Join(bootstrap, "cmdline-tools", "latest", "bin", "sdkmanager"))
	}
	if manager, err := exec.LookPath("sdkmanager"); err == nil {
		candidates = append(candidates, manager)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if err := androidToolsDownload(ctx, sdkRoot); err != nil {
		return "", fmt.Errorf("Android sdkmanager is unavailable and command-line tools bootstrap failed: %w", err)
	}
	manager := filepath.Join(sdkRoot, "cmdline-tools", "latest", "bin", "sdkmanager")
	if info, err := os.Stat(manager); err != nil || info.IsDir() {
		return "", fmt.Errorf("Android sdkmanager bootstrap completed without %s", manager)
	}
	return manager, nil
}

func downloadCommandLineTools(ctx context.Context, sdkRoot string) error {
	name := "commandlinetools-linux-" + commandLineToolsVersion + "_latest.zip"
	if androidToolsGOOS == "darwin" {
		name = "commandlinetools-mac-" + commandLineToolsVersion + "_latest.zip"
	}
	url := "https://dl.google.com/android/repository/" + name
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := androidToolsHTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download command-line tools: %s", response.Status)
	}
	temporary, err := os.CreateTemp("", "credimi-android-tools-*.zip")
	if err != nil {
		return err
	}
	path := temporary.Name()
	defer os.Remove(path)
	if _, err := io.Copy(temporary, response.Body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer archive.Close()
	root := filepath.Join(sdkRoot, "cmdline-tools", "latest")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	for _, file := range archive.File {
		name := filepath.ToSlash(file.Name)
		name = strings.TrimPrefix(name, "cmdline-tools/")
		if name == "" || name == "." {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if !withinDirectory(root, target) {
			return fmt.Errorf("command-line tools archive contains unsafe path %q", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		input, err := file.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fileMode(file.Mode()))
		if err != nil {
			_ = input.Close()
			return err
		}
		_, err = io.Copy(output, input)
		_ = input.Close()
		if closeErr := output.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func withinDirectory(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func fileMode(mode os.FileMode) os.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func sdkPackageInstalled(sdkRoot, packageName string) bool {
	parts := strings.Split(packageName, ";")
	if len(parts) < 2 {
		return false
	}
	return fileExists(filepath.Join(append([]string{sdkRoot}, parts...)...))
}

func platformToolsAvailable(sdkRoot string) bool {
	// An emulator validates ANDROID_SDK_ROOT itself. It therefore needs the
	// platform-tools directory in that exact mutable SDK, not just an adb
	// fallback from the immutable bootstrap image.
	return fileExists(filepath.Join(sdkRoot, "platform-tools", "adb"))
}

func ensureSDKLicenses(sdkRoot string) error {
	licensesDir := filepath.Join(sdkRoot, "licenses")
	if err := os.MkdirAll(licensesDir, 0o755); err != nil {
		return fmt.Errorf("create Android SDK licenses directory: %w", err)
	}
	bootstrap := strings.TrimSpace(os.Getenv("ANDROID_SDK_BOOTSTRAP"))
	if bootstrap == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(bootstrap, "licenses"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Android SDK bootstrap licenses: %w", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		target := filepath.Join(licensesDir, entry.Name())
		if _, err := os.Stat(target); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect Android SDK license %s: %w", entry.Name(), err)
		}
		contents, err := os.ReadFile(filepath.Join(bootstrap, "licenses", entry.Name()))
		if err != nil {
			return fmt.Errorf("read Android SDK license %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(target, contents, 0o644); err != nil {
			return fmt.Errorf("copy Android SDK license %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func sdkLicensesAvailable(sdkRoot string) bool {
	entries, err := os.ReadDir(filepath.Join(sdkRoot, "licenses"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
