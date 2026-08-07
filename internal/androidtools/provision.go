// Package androidtools provisions the mutable Android SDK assets shared by
// every device in the runner container.
package androidtools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type CommandRunner func(context.Context, string, ...string) error

func Ensure(ctx context.Context, sdkRoot string) error {
	return EnsureWithRunner(ctx, sdkRoot, func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	})
}

// EnsureWithRunner is injectable so provisioning decisions remain testable
// without downloading SDK packages.
func EnsureWithRunner(ctx context.Context, sdkRoot string, run CommandRunner) error {
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
	if _, err := os.Stat(filepath.Join(sdkRoot, "platform-tools", "adb")); err != nil {
		packages = append(packages, "platform-tools")
	}
	if _, err := os.Stat(filepath.Join(sdkRoot, "emulator", "emulator")); err != nil {
		packages = append(packages, "emulator")
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
