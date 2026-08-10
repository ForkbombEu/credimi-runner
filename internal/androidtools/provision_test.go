package androidtools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureWithRunnerReusesCompleteSDK(t *testing.T) {
	installFakeSDKManager(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "platform-tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "platform-tools", "adb"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "emulator"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "emulator", "emulator"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "platform-tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "platform-tools", "adb"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := EnsureWithRunner(context.Background(), root, func(context.Context, string, ...string) error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("complete SDK should not invoke sdkmanager")
	}
}

func TestEnsureWithRunnerInstallsMissingPackages(t *testing.T) {
	installFakeSDKManager(t)
	root := t.TempDir()
	var command string
	var args []string
	if err := EnsureWithRunner(context.Background(), root, func(_ context.Context, name string, got ...string) error { command, args = name, got; return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(command, "/sdkmanager") || !strings.Contains(strings.Join(args, " "), "platform-tools") || !strings.Contains(strings.Join(args, " "), "emulator") {
		t.Fatalf("sdkmanager invocation = %q %#v", command, args)
	}
}

func TestEnsureCapabilitiesPhysicalOnlySkipsEmulator(t *testing.T) {
	installFakeSDKManager(t)
	root := t.TempDir()
	var args []string
	if err := EnsureCapabilitiesWithRunner(context.Background(), root, false, "", func(_ context.Context, _ string, got ...string) error {
		args = got
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "emulator") {
		t.Fatalf("physical-only provisioning installed emulator: %q", joined)
	}
	if !strings.Contains(joined, "platform-tools") {
		t.Fatalf("physical-only provisioning skipped platform-tools: %q", joined)
	}
}

func TestEnsureCapabilitiesEmulatorInstallsSystemImageOnce(t *testing.T) {
	installFakeSDKManager(t)
	root := t.TempDir()
	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, args)
		return nil
	}
	image := "system-images;android-35;google_apis;x86_64"
	if err := EnsureCapabilitiesWithRunner(context.Background(), root, true, image, run); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), image) {
		t.Fatalf("emulator provisioning calls = %#v", calls)
	}
	if err := os.MkdirAll(filepath.Join(root, "emulator"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "emulator", "emulator"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "platform-tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "platform-tools", "adb"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(root, "system-images", "android-35", "google_apis", "x86_64")
	if err := os.MkdirAll(imagePath, 0o755); err != nil {
		t.Fatal(err)
	}
	calls = nil
	if err := EnsureCapabilitiesWithRunner(context.Background(), root, true, image, run); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("already-installed emulator provisioning repeated install: %#v", calls)
	}
}

func TestEnsureReportsMissingSDKManager(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := Ensure(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "sdkmanager is unavailable") {
		t.Fatalf("Ensure error = %v", err)
	}
}

func TestEnsureRejectsEmptySDKRoot(t *testing.T) {
	if err := Ensure(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "SDK root is required") {
		t.Fatalf("empty SDK root error = %v", err)
	}
}

func TestEnsureReusesCompleteSDKThroughCommandWrapper(t *testing.T) {
	installFakeSDKManager(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "platform-tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "platform-tools", "adb"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "emulator"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "emulator", "emulator"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(context.Background(), root); err != nil {
		t.Fatal(err)
	}
}

func installFakeSDKManager(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sdkmanager")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
