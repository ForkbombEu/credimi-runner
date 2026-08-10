// Package androidtools provisions the mutable Android SDK assets shared by
// every device in the runner container.
package androidtools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type CommandRunner func(context.Context, string, ...string) error

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
	manager, err := exec.LookPath("sdkmanager")
	if err != nil {
		return fmt.Errorf("Android sdkmanager is unavailable: %w", err)
	}
	packages := []string{}
	if !platformToolsAvailable(sdkRoot) {
		packages = append(packages, "platform-tools")
	}
	if needsEmulator {
		if _, err := os.Stat(filepath.Join(sdkRoot, "emulator", "emulator")); err != nil {
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

func sdkPackageInstalled(sdkRoot, packageName string) bool {
	parts := strings.Split(packageName, ";")
	if len(parts) < 2 {
		return false
	}
	return fileExists(filepath.Join(append([]string{sdkRoot}, parts...)...))
}

func platformToolsAvailable(sdkRoot string) bool {
	if fileExists(filepath.Join(sdkRoot, "platform-tools", "adb")) {
		return true
	}
	bootstrap := os.Getenv("ANDROID_SDK_BOOTSTRAP")
	return bootstrap != "" && fileExists(filepath.Join(bootstrap, "platform-tools", "adb"))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
