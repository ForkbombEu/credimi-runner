package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSimctlEntries(t *testing.T) {
	label, identifier, ok := parseSimctlDeviceTypeLine("iPhone 16 Pro (com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro)")
	if !ok || label != "iPhone 16 Pro" || identifier != "com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro" {
		t.Fatalf("device type parse = %q %q %v", label, identifier, ok)
	}
	label, identifier, ok = parseSimctlRuntimeLine("iOS 18.0 - com.apple.CoreSimulator.SimRuntime.iOS-18-0")
	if !ok || label != "iOS 18.0" || identifier != "com.apple.CoreSimulator.SimRuntime.iOS-18-0" {
		t.Fatalf("runtime parse = %q %q %v", label, identifier, ok)
	}
}

func TestIOSSimulatorUDID(t *testing.T) {
	if got := iosSimulatorUDID("iPhone 16 Pro (AAAA-BBBB) (Shutdown)\n", "iPhone 16 Pro"); got != "AAAA-BBBB" {
		t.Fatalf("UDID = %q", got)
	}
	if got := iosSimulatorUDID("credimi (CCCC-DDDD)\n", "credimi"); got != "CCCC-DDDD" {
		t.Fatalf("short UDID = %q", got)
	}
}

func TestProvisioningHelpersRejectMalformedIdentifiersAndPaths(t *testing.T) {
	for _, line := range []string{
		"iPhone 16 Pro",
		"iPhone 16 Pro (com.apple.NotADeviceType)",
		"(com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro)",
	} {
		if _, _, ok := parseSimctlDeviceTypeLine(line); ok {
			t.Fatalf("malformed device type accepted: %q", line)
		}
	}
	for _, line := range []string{"iOS 18.0", "iOS 18.0 - com.apple.NotARuntime"} {
		if _, _, ok := parseSimctlRuntimeLine(line); ok {
			t.Fatalf("malformed runtime accepted: %q", line)
		}
	}

	if avdAssetsExistForName("", "credimi") || avdAssetsExistForName(t.TempDir(), "") {
		t.Fatal("empty AVD asset paths were accepted")
	}
	if goldenAssetsPresent("") || goldenAssetsPresentForLeaf("", "credimi-golden") {
		t.Fatal("empty golden asset paths were accepted")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "credimi-golden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !goldenAssetsPresent(filepath.Join(root, "credimi-golden")) {
		t.Fatal("golden leaf root was not recognized")
	}
	if got := goldenLeafFromPath("/", "pixel"); got != "pixel-golden" {
		t.Fatalf("root golden path leaf = %q", got)
	}
	if got := goldenLeafFromPath("", ""); got != "credimi-golden" {
		t.Fatalf("default golden path leaf = %q", got)
	}
}

func TestListIOSSimulatorRuntimesFiltersUnavailable(t *testing.T) {
	original := provisioningCommand
	t.Cleanup(func() { provisioningCommand = original })
	provisioningCommand = func(context.Context, string, ...string) *exec.Cmd {
		return helperCommand(t, "iOS 18.0 - com.apple.CoreSimulator.SimRuntime.iOS-18-0\niOS 17.0 unavailable - com.apple.CoreSimulator.SimRuntime.iOS-17-0")
	}
	options, err := listIOSSimulatorRuntimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].Identifier != "com.apple.CoreSimulator.SimRuntime.iOS-18-0" {
		t.Fatalf("runtimes = %#v", options)
	}
}

func TestListIOSSimulatorDeviceTypes(t *testing.T) {
	original := provisioningCommand
	t.Cleanup(func() { provisioningCommand = original })
	provisioningCommand = func(context.Context, string, ...string) *exec.Cmd {
		return helperCommand(t, "== Device Types ==\niPhone 16 Pro (com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro)\ninvalid line")
	}
	options, err := listIOSSimulatorDeviceTypes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].Label != "iPhone 16 Pro" {
		t.Fatalf("device types = %#v", options)
	}
}

func TestAndroidEmulatorAssetHelpers(t *testing.T) {
	dir := t.TempDir()
	avdHome := filepath.Join(dir, "avd")
	goldenRoot := filepath.Join(dir, "golden")
	if err := os.MkdirAll(filepath.Join(avdHome, "credimi.avd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(avdHome, "credimi.ini"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(goldenRoot, "credimi-golden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(goldenRoot, "pixel-golden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !avdAssetsExistForName(avdHome, "credimi") {
		t.Fatal("expected AVD assets to exist")
	}
	if avdAssetsExistForName(avdHome, "missing") {
		t.Fatal("unexpected AVD assets for missing")
	}
	options := listAVDOptions(avdHome)
	if len(options) != 1 || options[0].Name != "credimi" {
		t.Fatalf("AVD options = %#v", options)
	}
	if !goldenAssetsPresent(goldenRoot) {
		t.Fatal("expected golden assets to exist")
	}
	if !goldenAssetsPresentForLeaf(goldenRoot, "credimi-golden") {
		t.Fatal("expected credimi golden leaf to exist")
	}
	if goldenAssetsPresentForLeaf(goldenRoot, "missing-golden") {
		t.Fatal("unexpected missing golden leaf")
	}
	if got := goldenLeafFromPath("/avd-golden/pixel-golden", "credimi"); got != "pixel-golden" {
		t.Fatalf("goldenLeafFromPath = %q", got)
	}
	if got := goldenLeafFromPath("", "pixel"); got != "pixel-golden" {
		t.Fatalf("default goldenLeafFromPath = %q", got)
	}
	goldenOptions := listGoldenOptions(goldenRoot)
	if len(goldenOptions) != 2 {
		t.Fatalf("expected golden options, got %#v", goldenOptions)
	}
}

func TestIOSSimulatorStatusRoute(t *testing.T) {
	original := provisioningCommand
	t.Cleanup(func() { provisioningCommand = original })
	provisioningCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch strings.Join(args, " ") {
		case "simctl list devices available":
			return helperCommand(t, "credimi (AAAA-BBBB-CCCC)")
		case "simctl list devicetypes":
			return helperCommand(t, "iPhone 16 Pro (com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro)")
		case "simctl list runtimes":
			return helperCommand(t, "iOS 18.0 - com.apple.CoreSimulator.SimRuntime.iOS-18-0")
		default:
			return helperCommand(t, "")
		}
	}

	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/devices/ios-simulator/status?name=credimi", nil)
	s.iosSimulatorStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rec.Code, rec.Body.String())
	}
	var status IOSSimulatorStatus
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.Supported || !status.Exists {
		t.Fatalf("status = %#v", status)
	}
}

func TestIOSSimulatorStatusRouteBranches(t *testing.T) {
	s := newTestServer(t)
	s.lookupPath = func(string) (string, error) { return "", os.ErrNotExist }

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/devices/ios-simulator/status?name=credimi", nil)
	s.iosSimulatorStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unsupported status code = %d body=%s", rec.Code, rec.Body.String())
	}
	var unsupported IOSSimulatorStatus
	if err := json.NewDecoder(rec.Body).Decode(&unsupported); err != nil {
		t.Fatal(err)
	}
	if unsupported.Supported {
		t.Fatalf("unsupported status = %#v", unsupported)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/devices/ios-simulator/status", nil)
	s.iosSimulatorStatus(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name code = %d", rec.Code)
	}
}

func TestIOSSimulatorStatusRouteMissingSimulatorIncludesOptions(t *testing.T) {
	original := provisioningCommand
	t.Cleanup(func() { provisioningCommand = original })
	provisioningCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch strings.Join(args, " ") {
		case "simctl list devices available":
			return helperCommand(t, "other (AAAA-BBBB-CCCC)")
		case "simctl list devicetypes":
			return helperCommand(t, "iPhone 16 Pro (com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro)")
		case "simctl list runtimes":
			return helperCommand(t, "iOS 18.0 - com.apple.CoreSimulator.SimRuntime.iOS-18-0")
		default:
			return helperCommand(t, "")
		}
	}
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/devices/ios-simulator/status?name=credimi", nil)
	s.iosSimulatorStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rec.Code, rec.Body.String())
	}
	var status IOSSimulatorStatus
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Exists || len(status.DeviceTypes) != 1 || len(status.Runtimes) != 1 {
		t.Fatalf("status = %#v", status)
	}
}

func TestIOSSimulatorCreateRoute(t *testing.T) {
	original := provisioningCommand
	t.Cleanup(func() { provisioningCommand = original })
	var calls []string
	provisioningCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return helperCommand(t, "")
	}
	s := newTestServer(t)
	body := strings.NewReader(`{"name":"credimi","device_type_identifier":"com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro","runtime_identifier":"com.apple.CoreSimulator.SimRuntime.iOS-18-0"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/devices/ios-simulator/create", body)
	s.iosSimulatorCreate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create code = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "simctl create credimi com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro com.apple.CoreSimulator.SimRuntime.iOS-18-0") {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestIOSSimulatorCreateRouteValidation(t *testing.T) {
	s := newTestServer(t)
	s.lookupPath = func(string) (string, error) { return "", os.ErrNotExist }

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/devices/ios-simulator/create", strings.NewReader(`{}`))
	s.iosSimulatorCreate(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing xcrun code = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/devices/ios-simulator/create", strings.NewReader(`{`))
	s = newTestServer(t)
	s.iosSimulatorCreate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid json code = %d", rec.Code)
	}
}

func TestAndroidEmulatorAssetsRoutes(t *testing.T) {
	dir := t.TempDir()
	avdHome := filepath.Join(dir, "avd")
	goldenRoot := filepath.Join(dir, "golden")
	if err := os.MkdirAll(filepath.Join(avdHome, "credimi.avd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(avdHome, "credimi.ini"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(goldenRoot, "credimi-golden"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/devices/android-emulator/assets/status?base_name=credimi&avd_home="+avdHome+"&golden_root="+goldenRoot, nil)
	s.androidEmulatorAssetsStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rec.Code, rec.Body.String())
	}
	var status AndroidEmulatorAssetsStatus
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.AVDPresent || !status.GoldenPresent {
		t.Fatalf("status = %#v", status)
	}
	if status.GoldenLeaf != "credimi-golden" {
		t.Fatalf("golden leaf = %q", status.GoldenLeaf)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/devices/android-emulator/assets/status?base_name=pixel&avd_home="+avdHome+"&golden_root="+goldenRoot, nil)
	s.androidEmulatorAssetsStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("custom status code = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.GoldenPresent || status.GoldenLeaf != "pixel-golden" {
		t.Fatalf("custom status = %#v", status)
	}
}

func TestAndroidEmulatorAssetsSelectRoute(t *testing.T) {
	dir := t.TempDir()
	avdHome := filepath.Join(dir, "avd")
	goldenRoot := filepath.Join(dir, "golden")
	if err := os.MkdirAll(filepath.Join(avdHome, "credimi.avd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(avdHome, "credimi.ini"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(goldenRoot, "credimi-golden"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/devices/android-emulator/assets/select", strings.NewReader(`{`))
	s.androidEmulatorAssetsSelect(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid json code = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/devices/android-emulator/assets/select", strings.NewReader(`{"avd_home":"`+avdHome+`","golden_root":"`+goldenRoot+`","golden_path":"/avd-golden/credimi-golden"}`))
	s.androidEmulatorAssetsSelect(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rec.Code, rec.Body.String())
	}
	var status AndroidEmulatorAssetsStatus
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.BaseName != "credimi" || !status.AVDPresent || !status.GoldenPresent {
		t.Fatalf("status = %#v", status)
	}
}

func TestAndroidEmulatorAssetsDownloadRoute(t *testing.T) {
	originalClient := downloadAndroidAssets
	t.Cleanup(func() { downloadAndroidAssets = originalClient })
	downloadAndroidAssets = func(_ context.Context, url, dest string, progress func(DownloadProgress)) error {
		if progress != nil {
			progress(DownloadProgress{Phase: "downloading"})
			progress(DownloadProgress{Phase: "extracting"})
		}
		if strings.Contains(url, "golden") {
			return os.MkdirAll(filepath.Join(dest, "credimi-golden"), 0o755)
		}
		if err := os.MkdirAll(filepath.Join(dest, "credimi.avd"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, "credimi.ini"), nil, 0o644)
	}
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/devices/android-emulator/assets/download", strings.NewReader(`{}`))
	s.androidEmulatorAssetsDownload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("default paths code = %d", rec.Code)
	}

	avdHome := filepath.Join(t.TempDir(), "avd")
	goldenRoot := filepath.Join(t.TempDir(), "golden")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/devices/android-emulator/assets/download", strings.NewReader(`{"base_name":"credimi","avd_home":"`+avdHome+`","golden_root":"`+goldenRoot+`","golden_path":"/avd-golden/credimi-golden"}`))
	s.androidEmulatorAssetsDownload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("download code = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(body, `"phase":"base_avd_downloading"`) || !strings.Contains(body, `"phase":"golden_downloading"`) || !strings.Contains(body, `"phase":"complete"`) {
		t.Fatalf("progress body = %s", body)
	}
	if _, err := os.Stat(filepath.Join(avdHome, "credimi.avd")); err != nil {
		t.Fatalf("expected avd assets under %s", avdHome)
	}
	if _, err := os.Stat(filepath.Join(goldenRoot, "credimi-golden")); err != nil {
		t.Fatalf("expected golden assets under %s", goldenRoot)
	}
}

func helperCommand(t *testing.T, output string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", output)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i := range args {
		if args[i] == "--" && i+1 < len(args) {
			io.WriteString(os.Stdout, args[i+1])
			break
		}
	}
	os.Exit(0)
}
