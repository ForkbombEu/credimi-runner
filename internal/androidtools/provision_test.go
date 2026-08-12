package androidtools

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
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

func TestEnsureRuntimeCapabilitiesAtWithUsesInjectedSDKRootForPhysicalInventory(t *testing.T) {
	root := t.TempDir()
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "usb-1"},
	}}
	var gotRoot string
	if err := EnsureRuntimeCapabilitiesAtWith(context.Background(), cfg, "linux", root, func(_ context.Context, sdkRoot string, needsEmulator bool, image string) error {
		gotRoot = sdkRoot
		if needsEmulator || image != "" {
			t.Fatalf("physical capability request = emulator %v image %q", needsEmulator, image)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if gotRoot != root || os.Getenv("ANDROID_SDK_ROOT") != root {
		t.Fatalf("injected SDK root = %q env=%q", gotRoot, os.Getenv("ANDROID_SDK_ROOT"))
	}
}

func TestEnsureRuntimeCapabilitiesAtWithSkipsUnsupportedInventory(t *testing.T) {
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{ID: "acme/runner/ios", Type: runnerconfig.DeviceIOSSimulator, Enabled: true}}
	called := false
	if err := EnsureRuntimeCapabilitiesAtWith(context.Background(), cfg, "darwin", t.TempDir(), func(context.Context, string, bool, string) error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("non-Android inventory invoked Android capability provisioner")
	}
}

func TestEnsureCapabilitiesWithRunnerReportsPackageInstallFailure(t *testing.T) {
	installFakeSDKManager(t)
	root := t.TempDir()
	err := EnsureCapabilitiesWithRunner(context.Background(), root, false, "", func(context.Context, string, ...string) error {
		return errors.New("sdkmanager failed")
	})
	if err == nil || !strings.Contains(err.Error(), "install Android SDK packages") || !strings.Contains(err.Error(), "sdkmanager failed") {
		t.Fatalf("package install error = %v", err)
	}
}

func TestEnsureSDKLicensesHandlesMissingBootstrapAndExistingFiles(t *testing.T) {
	sdkRoot := t.TempDir()
	t.Setenv("ANDROID_SDK_BOOTSTRAP", "")
	if err := ensureSDKLicenses(sdkRoot); err != nil {
		t.Fatal(err)
	}
	licenses := filepath.Join(sdkRoot, "licenses")
	if _, err := os.Stat(licenses); err != nil {
		t.Fatal(err)
	}
	bootstrap := t.TempDir()
	t.Setenv("ANDROID_SDK_BOOTSTRAP", bootstrap)
	if err := os.MkdirAll(filepath.Join(bootstrap, "licenses"), 0o755); err != nil {
		t.Fatal(err)
	}
	license := filepath.Join(bootstrap, "licenses", "android-sdk-license")
	if err := os.WriteFile(license, []byte("bootstrap"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(licenses, "android-sdk-license"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureSDKLicenses(sdkRoot); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(licenses, "android-sdk-license"))
	if err != nil || string(contents) != "existing" {
		t.Fatalf("existing license = %q err=%v", contents, err)
	}
}

func TestAndroidToolsCanUseBootstrapPlatformAndEmulatorBinaries(t *testing.T) {
	root := t.TempDir()
	bootstrap := t.TempDir()
	for _, relative := range []string{"platform-tools/adb", "emulator/emulator"} {
		path := filepath.Join(bootstrap, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ANDROID_SDK_BOOTSTRAP", bootstrap)
	if !platformToolsAvailable(root) || !emulatorAvailable(root) {
		t.Fatal("bootstrap Android tools were not discovered")
	}
	if sdkPackageInstalled(root, "platform-tools") || sdkPackageInstalled(root, "broken") {
		t.Fatal("invalid SDK package was reported installed")
	}
}

func TestVerifyRuntimeCapabilitiesExplainsEachMissingEmulatorRequirement(t *testing.T) {
	root := t.TempDir()
	image := "system-images;android-35;google_apis;x86_64"
	if err := verifyRuntimeCapabilities(root, "darwin", true, image); err == nil || !strings.Contains(err.Error(), "executable is unavailable") {
		t.Fatalf("missing emulator error = %v", err)
	}
	emulator := filepath.Join(root, "emulator", "emulator")
	if err := os.MkdirAll(filepath.Dir(emulator), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(emulator, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if err := verifyRuntimeCapabilities(root, "darwin", true, image); err == nil || !strings.Contains(err.Error(), "not resolvable") {
		t.Fatalf("unresolvable emulator error = %v", err)
	}
	t.Setenv("PATH", filepath.Dir(emulator))
	if err := verifyRuntimeCapabilities(root, "darwin", true, image); err == nil || !strings.Contains(err.Error(), "system image") {
		t.Fatalf("missing image error = %v", err)
	}
	imagePath := filepath.Join(append([]string{root}, strings.Split(image, ";")...)...)
	if err := os.MkdirAll(imagePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeCapabilities(root, "darwin", true, image); err == nil || !strings.Contains(err.Error(), "licenses") {
		t.Fatalf("missing license error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "licenses"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "licenses", "android-sdk-license"), []byte("accepted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeCapabilities(root, "darwin", true, image); err != nil {
		t.Fatal(err)
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

func TestEnsureAVDReportsInvalidConfigurationAndMissingManager(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name        string
		avdName     string
		systemImage string
		want        string
	}{
		{name: "missing name", systemImage: "system-images;android-35;google_apis;x86_64", want: "AVD name and system image are required"},
		{name: "missing image", avdName: "credimi", want: "AVD name and system image are required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := EnsureAVD(context.Background(), root, t.TempDir(), tc.avdName, tc.systemImage, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("EnsureAVD() error = %v, want %q", err, tc.want)
			}
		})
	}

	t.Setenv("PATH", t.TempDir())
	err := EnsureAVD(context.Background(), root, t.TempDir(), "credimi", "system-images;android-35;google_apis;x86_64", nil)
	if err == nil || !strings.Contains(err.Error(), "avdmanager is unavailable") {
		t.Fatalf("missing avdmanager error = %v", err)
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

func TestDownloadCommandLineToolsRejectsFailedDownload(t *testing.T) {
	previousClient, previousOS := androidToolsHTTP, androidToolsGOOS
	androidToolsGOOS = "linux"
	androidToolsHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Status:     "502 Bad Gateway",
			Body:       io.NopCloser(strings.NewReader("unavailable")),
			Header:     make(http.Header),
		}, nil
	})}
	t.Cleanup(func() { androidToolsHTTP, androidToolsGOOS = previousClient, previousOS })

	err := downloadCommandLineTools(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Fatalf("download error = %v", err)
	}
}

func TestDownloadCommandLineToolsRejectsUnsafeArchivePath(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("cmdline-tools/../../outside")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("must not be written")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	previousClient, previousOS := androidToolsHTTP, androidToolsGOOS
	androidToolsGOOS = "linux"
	androidToolsHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(buffer.Bytes())), Header: make(http.Header)}, nil
	})}
	t.Cleanup(func() { androidToolsHTTP, androidToolsGOOS = previousClient, previousOS })

	root := t.TempDir()
	err = downloadCommandLineTools(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("download error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cmdline-tools", "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe archive wrote outside tools root: %v", err)
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

func TestConfigureStableEnvironmentRejectsEmptySDKRoot(t *testing.T) {
	if err := ConfigureStableEnvironmentWithAVD("", t.TempDir()); err == nil || !strings.Contains(err.Error(), "SDK root is required") {
		t.Fatalf("empty stable environment error = %v", err)
	}
}

func TestEnsureCapabilitiesUsesCommandWrapper(t *testing.T) {
	installFakeSDKManager(t)
	if err := EnsureCapabilities(context.Background(), t.TempDir(), false, ""); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureCapabilitiesCopiesBootstrapLicensesBeforeInstall(t *testing.T) {
	root := t.TempDir()
	bootstrap := t.TempDir()
	managerDir := filepath.Join(root, "cmdline-tools", "latest", "bin")
	if err := os.MkdirAll(managerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managerDir, "sdkmanager"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bootstrap, "licenses"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("accepted-license\n")
	if err := os.WriteFile(filepath.Join(bootstrap, "licenses", "android-sdk-license"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANDROID_SDK_BOOTSTRAP", bootstrap)
	called := false
	err := EnsureCapabilitiesWithRunner(context.Background(), root, false, "", func(_ context.Context, _ string, args ...string) error {
		called = true
		got, readErr := os.ReadFile(filepath.Join(root, "licenses", "android-sdk-license"))
		if readErr != nil {
			return readErr
		}
		if string(got) != string(want) {
			return fmt.Errorf("license contents = %q", got)
		}
		if len(args) == 0 || args[0] != "--sdk_root="+root {
			return fmt.Errorf("sdkmanager args = %v", args)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("sdkmanager was not invoked")
	}
}

func TestEnsureRejectsEmptySDKRoot(t *testing.T) {
	if err := Ensure(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "SDK root is required") {
		t.Fatalf("empty SDK root error = %v", err)
	}
}

func TestEnsureRuntimeCapabilitiesSkipsIOSOnlyInventory(t *testing.T) {
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "runner/ios", Type: runnerconfig.DeviceIOSSimulator, Enabled: true,
		IOSSimulator: &runnerconfig.IOSSimulatorConfig{UDID: "ios-1"},
	}}
	called := false
	err := EnsureRuntimeCapabilitiesAtWith(context.Background(), cfg, "darwin", filepath.Join(t.TempDir(), "sdk"), func(context.Context, string, bool, string) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("iOS-only inventory bootstrapped Android tooling")
	}
}

func TestEnsureRuntimeCapabilitiesUsesTypedCapabilityDetector(t *testing.T) {
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "runner/ios", Type: runnerconfig.DeviceIOSSimulator, Enabled: true,
		IOSSimulator: &runnerconfig.IOSSimulatorConfig{UDID: "ios-1"},
	}}
	if err := EnsureRuntimeCapabilities(context.Background(), cfg, "darwin"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRuntimeCapabilitiesProvisionsPhysicalAndroid(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "sdkmanager"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("ANDROID_SDK_ROOT", root)
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "runner/phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", Serial: "phone:5555"},
	}}
	if err := EnsureRuntimeCapabilities(context.Background(), cfg, "darwin"); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ANDROID_SDK_ROOT") != root || !strings.Contains(os.Getenv("PATH"), filepath.Join(root, "platform-tools")) {
		t.Fatalf("stable Android environment = root:%q path:%q", os.Getenv("ANDROID_SDK_ROOT"), os.Getenv("PATH"))
	}
}

func TestEnsureRuntimeCapabilitiesProvisionsConfiguredEmulatorAssets(t *testing.T) {
	root := t.TempDir()
	managerDir := filepath.Join(root, "cmdline-tools", "latest", "bin")
	if err := os.MkdirAll(managerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managerDir, "avdmanager"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "runner/emulator", Type: runnerconfig.DeviceAndroidEmulator, Enabled: true,
		AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{AVDName: "pixel", SystemImage: "system-images;android-35;google_apis;arm64-v8a"},
	}}
	needsEmulator := false
	if err := EnsureRuntimeCapabilitiesAtWith(context.Background(), cfg, "darwin", root, func(_ context.Context, _ string, emulator bool, image string) error {
		needsEmulator, _ = emulator, image
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !needsEmulator {
		t.Fatal("emulator inventory did not request emulator capability provisioning")
	}
}

func TestEnsureRuntimeCapabilitiesReusesConfiguredAVDHome(t *testing.T) {
	root := t.TempDir()
	avdHome := t.TempDir()
	if err := os.Mkdir(filepath.Join(avdHome, "credimi.avd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(avdHome, "credimi.ini"), []byte("path=credimi.avd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANDROID_AVD_HOME", avdHome)
	cfg := runnerconfig.Bootstrap()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "runner/emulator", Type: runnerconfig.DeviceAndroidEmulator, Enabled: true,
		AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{AVDName: "credimi", SystemImage: "system-images;android-35;google_apis;x86_64"},
	}}
	if err := EnsureRuntimeCapabilitiesAtWith(context.Background(), cfg, "linux", root, func(context.Context, string, bool, string) error { return nil }); err != nil {
		t.Fatalf("existing mounted AVD was not reused: %v", err)
	}
	if got := os.Getenv("ANDROID_AVD_HOME"); got != avdHome {
		t.Fatalf("configured AVD home = %q, want %q", got, avdHome)
	}
}

func TestEnsureRuntimeCapabilitiesTreatsRedroidAsAndroidCapability(t *testing.T) {
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "runner/redroid", Type: runnerconfig.DeviceRedroid, Enabled: true,
		Redroid: &runnerconfig.RedroidConfig{Serial: "redroid:5555"},
	}}
	called := false
	err := EnsureRuntimeCapabilitiesAtWith(context.Background(), cfg, "darwin", filepath.Join(t.TempDir(), "sdk"), func(_ context.Context, _ string, emulator bool, _ string) error {
		called = true
		if emulator {
			t.Fatal("redroid requested emulator tooling")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("redroid did not request required local Android tooling")
	}
}

func TestVerifyRuntimeCapabilitiesRequiresUsableEmulatorTooling(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "emulator"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "emulator", "emulator"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "licenses"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "licenses", "android-sdk-license"), []byte("accepted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "system-images", "android-35", "google_apis", "x86_64"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "emulator"))
	if err := verifyRuntimeCapabilities(root, "darwin", true, ""); err != nil {
		t.Fatalf("complete emulator tooling rejected: %v", err)
	}

	if err := os.Remove(filepath.Join(root, "emulator", "emulator")); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeCapabilities(root, "darwin", true, ""); err == nil || !strings.Contains(err.Error(), "executable is unavailable") {
		t.Fatalf("missing emulator error = %v", err)
	}
}

func TestVerifyRuntimeCapabilitiesRejectsMissingImageAndLicenses(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "emulator", "emulator"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Replace the directory with the executable expected by LookPath.
	if err := os.RemoveAll(filepath.Join(root, "emulator", "emulator")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "emulator", "emulator"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "licenses"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "licenses", "android-sdk-license"), []byte("accepted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "emulator"))
	if err := verifyRuntimeCapabilities(root, "darwin", true, "system-images;android-35;google_apis;x86_64"); err == nil || !strings.Contains(err.Error(), "system image") {
		t.Fatalf("missing system image error = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "licenses")); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeCapabilities(root, "darwin", true, ""); err == nil || !strings.Contains(err.Error(), "licenses") {
		t.Fatalf("missing licenses error = %v", err)
	}
}

func TestEnsureRuntimeCapabilitiesVerifiesDefaultEmulatorPath(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "emulator"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "emulator", "emulator"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "licenses"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "licenses", "android-sdk-license"), []byte("accepted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "system-images", "android-35", "google_apis", "x86_64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sdkmanager"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+filepath.Join(root, "emulator"))
	avdHome := t.TempDir()
	if err := os.Mkdir(filepath.Join(avdHome, "credimi.avd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(avdHome, "credimi.ini"), []byte("path=credimi.avd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANDROID_AVD_HOME", avdHome)
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "runner/emulator", Name: "Emulator", Type: runnerconfig.DeviceAndroidEmulator, Enabled: true,
		AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{AVDName: "credimi", SystemImage: "system-images;android-35;google_apis;x86_64"},
	}}
	if err := EnsureRuntimeCapabilitiesAtWith(context.Background(), cfg, "darwin", root, nil); err != nil {
		t.Fatalf("default emulator capability verification failed: %v", err)
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
