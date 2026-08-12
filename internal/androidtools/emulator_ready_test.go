package androidtools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

func TestEnsureEmulatorReadyReusesMountedAssetsAndEmptySerial(t *testing.T) {
	root := t.TempDir()
	managerDir := filepath.Join(root, "cmdline-tools", "latest", "bin")
	for _, path := range []string{
		filepath.Join(managerDir, "sdkmanager"),
		filepath.Join(managerDir, "avdmanager"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(root, "platform-tools", "adb"),
		filepath.Join(root, "emulator", "emulator"),
		filepath.Join(root, "licenses", "android-sdk-license"),
		filepath.Join(root, "system-images", "android-35", "google_apis", "x86_64", "package.xml"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("ready\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	avdHome := t.TempDir()
	goldenRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(avdHome, "credimi.avd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(avdHome, "credimi.ini"), []byte("path=credimi.avd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(goldenRoot, "credimi-golden"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANDROID_SDK_ROOT", root)
	t.Setenv("ANDROID_AVD_HOME", avdHome)
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/emulator", Type: runnerconfig.DeviceAndroidEmulator, Enabled: true,
		AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{AVDName: "credimi", BaseName: "credimi", GoldenSource: filepath.Join(goldenRoot, "credimi-golden"), SystemImage: "system-images;android-35;google_apis;x86_64"},
	}}
	if err := EnsureEmulatorReady(context.Background(), cfg, "darwin", nil); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureEmulatorReadyWithoutAndroidTargetsIsNoop(t *testing.T) {
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/ios", Type: runnerconfig.DeviceIOSSimulator, Enabled: true,
	}}
	progressed := false
	if err := EnsureEmulatorReady(context.Background(), cfg, "linux", func(string) { progressed = true }); err != nil {
		t.Fatal(err)
	}
	if progressed {
		t.Fatal("emulator readiness emitted Android progress for an iOS-only inventory")
	}
}

func TestEffectiveEmulatorPathsUseConfiguredValues(t *testing.T) {
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = filepath.Join("/state", "runner")
	t.Setenv("ANDROID_SDK_ROOT", "/sdk")
	t.Setenv("ANDROID_AVD_HOME", "/avd")
	if got := effectiveSDKRoot(cfg, "linux"); got != "/sdk" {
		t.Fatalf("SDK root = %q", got)
	}
	if got := effectiveAVDHome(cfg); got != "/avd" {
		t.Fatalf("AVD home = %q", got)
	}
	t.Setenv("ANDROID_SDK_ROOT", "")
	if got := effectiveSDKRoot(cfg, "linux"); got != "/opt/android-sdk" {
		t.Fatalf("default Linux SDK root = %q", got)
	}
	if got := effectiveSDKRoot(cfg, "darwin"); got != filepath.Join("/state", "runner", "android", "sdk") {
		t.Fatalf("default macOS SDK root = %q", got)
	}
	if root, leaf := effectiveGoldenPath("/golden/custom", "credimi"); root != "/golden" || leaf != "custom" {
		t.Fatalf("golden path = %q/%q", root, leaf)
	}
}

func TestEnsureEmulatorReadyDownloadsMissingAssets(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "cmdline-tools", "latest", "bin", "sdkmanager"),
		filepath.Join(root, "cmdline-tools", "latest", "bin", "avdmanager"),
		filepath.Join(root, "platform-tools", "adb"),
		filepath.Join(root, "emulator", "emulator"),
		filepath.Join(root, "licenses", "android-sdk-license"),
		filepath.Join(root, "system-images", "android-35", "google_apis", "x86_64", "package.xml"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("ready\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	avdHome, goldenRoot := filepath.Join(t.TempDir(), "avd"), filepath.Join(t.TempDir(), "golden")
	t.Setenv("ANDROID_SDK_ROOT", root)
	t.Setenv("ANDROID_AVD_HOME", avdHome)
	original := androidAssetHTTPClient
	t.Cleanup(func() { androidAssetHTTPClient = original })
	base := archiveFiles(t, map[string]string{"credimi.avd/config.ini": "base", "credimi.ini": "path=credimi.avd\n"})
	golden := archiveFiles(t, map[string]string{"credimi-golden/config.ini": "golden"})
	androidAssetHTTPClient = &http.Client{Transport: assetRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := base
		if strings.Contains(request.URL.String(), "golden") {
			body = golden
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	cfg := runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/emulator", Type: runnerconfig.DeviceAndroidEmulator, Enabled: true,
		AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{AVDName: "credimi", BaseName: "credimi", GoldenSource: filepath.Join(goldenRoot, "credimi-golden"), SystemImage: "system-images;android-35;google_apis;x86_64"},
	}}
	var stages []string
	if err := EnsureEmulatorReady(context.Background(), cfg, "darwin", func(stage string) { stages = append(stages, stage) }); err != nil {
		t.Fatal(err)
	}
	if !AVDAssetsExist(avdHome, "credimi") || !GoldenAssetsExist(goldenRoot, "credimi-golden") {
		t.Fatalf("downloaded emulator assets missing: avd=%v golden=%v", AVDAssetsExist(avdHome, "credimi"), GoldenAssetsExist(goldenRoot, "credimi-golden"))
	}
	if len(stages) < 3 {
		t.Fatalf("emulator readiness stages = %v", stages)
	}
}

func TestEnsureEmulatorReadyReportsMissingBaseArchive(t *testing.T) {
	root, avdHome, goldenRoot, cfg := emulatorReadyFixture(t)
	t.Setenv("ANDROID_SDK_ROOT", root)
	t.Setenv("ANDROID_AVD_HOME", avdHome)
	original := androidAssetHTTPClient
	t.Cleanup(func() { androidAssetHTTPClient = original })
	androidAssetHTTPClient = &http.Client{Transport: assetRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", Body: io.NopCloser(strings.NewReader("archive unavailable")), Header: make(http.Header)}, nil
	})}
	err := EnsureEmulatorReady(context.Background(), cfg, "darwin", nil)
	if err == nil || !strings.Contains(err.Error(), "download base Android AVD") {
		t.Fatalf("missing base archive error = %v", err)
	}
	if GoldenAssetsExist(goldenRoot, "credimi-golden") {
		t.Fatal("golden assets should not be activated after base provisioning failed")
	}
}

func TestEnsureEmulatorReadyReportsMissingGoldenArchive(t *testing.T) {
	root, avdHome, goldenRoot, cfg := emulatorReadyFixture(t)
	if err := os.MkdirAll(filepath.Join(avdHome, "credimi.avd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(avdHome, "credimi.ini"), []byte("path=credimi.avd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANDROID_SDK_ROOT", root)
	t.Setenv("ANDROID_AVD_HOME", avdHome)
	original := androidAssetHTTPClient
	t.Cleanup(func() { androidAssetHTTPClient = original })
	androidAssetHTTPClient = &http.Client{Transport: assetRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if !strings.Contains(request.URL.String(), "golden") {
			t.Fatalf("unexpected asset request %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("missing")), Header: make(http.Header)}, nil
	})}
	err := EnsureEmulatorReady(context.Background(), cfg, "darwin", nil)
	if err == nil || !strings.Contains(err.Error(), "download Credimi golden image") {
		t.Fatalf("missing golden archive error = %v", err)
	}
	if GoldenAssetsExist(goldenRoot, "credimi-golden") {
		t.Fatal("golden assets should not be activated after golden provisioning failed")
	}
}

func emulatorReadyFixture(t *testing.T) (root, avdHome, goldenRoot string, cfg runnerconfig.Config) {
	t.Helper()
	root = t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "cmdline-tools", "latest", "bin", "sdkmanager"),
		filepath.Join(root, "cmdline-tools", "latest", "bin", "avdmanager"),
		filepath.Join(root, "platform-tools", "adb"),
		filepath.Join(root, "emulator", "emulator"),
		filepath.Join(root, "licenses", "android-sdk-license"),
		filepath.Join(root, "system-images", "android-35", "google_apis", "x86_64", "package.xml"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("ready\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	avdHome = t.TempDir()
	goldenRoot = t.TempDir()
	cfg = runnerconfig.Bootstrap()
	cfg.Storage.StateDir = t.TempDir()
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/emulator", Type: runnerconfig.DeviceAndroidEmulator, Enabled: true,
		AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{AVDName: "credimi", BaseName: "credimi", GoldenSource: filepath.Join(goldenRoot, "credimi-golden"), SystemImage: "system-images;android-35;google_apis;x86_64"},
	}}
	return root, avdHome, goldenRoot, cfg
}

func archiveFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	for name, contents := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
