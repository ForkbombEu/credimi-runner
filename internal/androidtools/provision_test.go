package androidtools

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
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
	previous := androidToolsDownload
	androidToolsDownload = func(context.Context, string) error { return errors.New("test bootstrap disabled") }
	t.Cleanup(func() { androidToolsDownload = previous })
	if err := Ensure(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "sdkmanager is unavailable") {
		t.Fatalf("Ensure error = %v", err)
	}
}

func TestEnsureAVDIsIdempotentAndUsesPersistentHome(t *testing.T) {
	root := t.TempDir()
	installFakeSDKManager(t)
	if err := os.MkdirAll(filepath.Join(root, "cmdline-tools", "latest", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := filepath.Join(root, "cmdline-tools", "latest", "bin", "avdmanager")
	if err := os.WriteFile(manager, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "avd")
	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, args)
		if err := os.MkdirAll(filepath.Join(home, "pixel.avd"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(home, "pixel.ini"), nil, 0o644)
	}
	if err := EnsureAVD(context.Background(), root, home, "pixel", "system-images;android-35;google_apis;arm64-v8a", run); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "system-images;android-35;google_apis;arm64-v8a") {
		t.Fatalf("avdmanager calls = %#v", calls)
	}
	if err := EnsureAVD(context.Background(), root, home, "pixel", "system-images;android-35;google_apis;arm64-v8a", run); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("existing AVD was recreated: %#v", calls)
	}
}

func TestEnsureBootstrapsPinnedCommandLineToolsArchive(t *testing.T) {
	root := t.TempDir()
	archive := newCommandLineToolsArchive(t)
	previousClient, previousOS := androidToolsHTTP, androidToolsGOOS
	androidToolsGOOS = "linux"
	androidToolsHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(archive)), Header: make(http.Header)}, nil
	})}
	t.Setenv("PATH", t.TempDir())
	t.Cleanup(func() { androidToolsHTTP, androidToolsGOOS = previousClient, previousOS })
	called := false
	if err := EnsureCapabilitiesWithRunner(context.Background(), root, false, "", func(_ context.Context, name string, args ...string) error {
		called = true
		if !strings.HasSuffix(name, "/sdkmanager") || !strings.Contains(strings.Join(args, " "), "platform-tools") {
			t.Fatalf("sdkmanager command = %q %#v", name, args)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called || !fileExists(filepath.Join(root, "cmdline-tools", "latest", "bin", "sdkmanager")) {
		t.Fatal("command-line tools were not bootstrapped")
	}
}

func newCommandLineToolsArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "cmdline-tools/bin/sdkmanager", Method: zip.Store}
	header.SetMode(0o755)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("#!/bin/sh\nexit 0\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestConfigureStableEnvironmentIncludesRunnerOwnedTools(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sdk")
	avd := filepath.Join(t.TempDir(), "avd")
	t.Setenv("PATH", "/usr/bin")
	if err := ConfigureStableEnvironmentWithAVD(root, avd); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ANDROID_SDK_ROOT") != root || os.Getenv("ANDROID_HOME") != root || os.Getenv("ANDROID_AVD_HOME") != avd {
		t.Fatalf("stable Android environment = sdk:%q home:%q avd:%q", os.Getenv("ANDROID_SDK_ROOT"), os.Getenv("ANDROID_HOME"), os.Getenv("ANDROID_AVD_HOME"))
	}
	path := os.Getenv("PATH")
	for _, part := range []string{"cmdline-tools/latest/bin", "platform-tools", "emulator"} {
		if !strings.Contains(path, filepath.Join(root, part)) {
			t.Fatalf("PATH %q does not include %s", path, filepath.Join(root, part))
		}
	}
}

func TestEnsureCapabilitiesUsesCommandWrapper(t *testing.T) {
	installFakeSDKManager(t)
	if err := EnsureCapabilities(context.Background(), t.TempDir(), false, ""); err != nil {
		t.Fatal(err)
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
