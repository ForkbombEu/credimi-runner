package dashboard

import (
	"strings"
	"testing"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

func TestPageDataAndroidPhoneDevicesDelegatesToAndroidDevices(t *testing.T) {
	d := PageData{Snapshot: Snapshot{Devices: []Device{
		{Serial: "online", Type: "android_phone", Status: Online},
		{Serial: "offline", Type: "android_phone", Status: Offline},
		{Serial: "ios", Type: "ios_simulator", Status: Online},
	}}}
	devices := d.AndroidPhoneDevices()
	if len(devices) != 1 || devices[0].Serial != "online" {
		t.Fatalf("devices = %#v", devices)
	}
}

func TestConfiguredTargetDetailVariants(t *testing.T) {
	for runnerType, want := range map[string]string{
		"android_emulator": "emulator-base",
		"ios_simulator":    "emulator-base",
	} {
		d := PageData{Runner: &Config{values: map[string]string{
			"CREDIMI_RUNNER_TYPE": runnerType,
			"BASE_NAME":           "emulator-base",
		}}}
		if got := d.ConfiguredTargetDetail(); got != want {
			t.Fatalf("ConfiguredTargetDetail(%s) = %q", runnerType, got)
		}
	}
	d := PageData{Runner: &Config{values: map[string]string{"CREDIMI_RUNNER_TYPE": "unknown"}}}
	if profile := d.ActiveTargetProfile(); profile.Type == "" {
		t.Fatalf("default profile = %#v", profile)
	}
}

func TestPageDataWorkerAndNetworkViewModels(t *testing.T) {
	cfg := &Config{values: map[string]string{
		"CREDIMI_URL":                 "https://credimi.example",
		"CREDIMI_USER_API_KEY":        "user-key",
		"CREDIMI_SERVICE_MODE":        "cloudflare-managed",
		"RUNNER_DOMAIN":               "runner.example",
		"RUNNER_HOST":                 "0.0.0.0",
		"RUNNER_PORT":                 "8050",
		"CREDIMI_RUNNER_ORGANIZATION": "xy",
	}}
	d := PageData{
		Active: "overview",
		Runner: cfg,
		Snapshot: Snapshot{Services: []Service{
			{ID: "runner", Status: Online},
			{ID: "temporal", Status: Online},
		}},
		Workers: []Worker{
			{ID: "runner-mr", Env: "runner", Status: Online},
		},
	}

	if !d.ServicesAllUp() {
		t.Fatal("expected all services up")
	}
	if got := d.PublicURL(); got != "https://runner.example" {
		t.Fatalf("managed PublicURL = %q", got)
	}

	cfg.values["RUNNER_DOMAIN"] = ""
	if got := d.PublicURL(); got != "https://<runner-domain>" {
		t.Fatalf("managed placeholder PublicURL = %q", got)
	}
	cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	if got := d.PublicURL(); got != "Waiting for manual public URL" {
		t.Fatalf("manual PublicURL = %q", got)
	}
	cfg.values["RUNNER_PUBLIC_URL"] = "https://manual.example"
	if got := d.PublicURL(); got != "https://manual.example" {
		t.Fatalf("manual host PublicURL = %q", got)
	}
	cfg.values["RUNNER_PUBLIC_PORT"] = "8443"
	if got := d.PublicURL(); got != "https://manual.example:8443" {
		t.Fatalf("manual host and port PublicURL = %q", got)
	}
	cfg.values["RUNNER_PUBLIC_PORT"] = ""
	cfg.values["CREDIMI_SERVICE_MODE"] = "auto"
	if got := d.PublicURL(); got != "Waiting for quick tunnel URL" {
		t.Fatalf("auto PublicURL = %q", got)
	}
	d.Data = map[string]any{"RuntimeStatus": dashboardruntime.RuntimeStatus{PublicURL: "https://runner.example.trycloudflare.com"}}
	if got := d.PublicURL(); got != "https://runner.example.trycloudflare.com" {
		t.Fatalf("runtime PublicURL = %q", got)
	}
	if got := d.RunnerAPIURL(); got != "http://127.0.0.1:8050" {
		t.Fatalf("RunnerAPIURL = %q", got)
	}
	cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	cfg.values["CREDIMI_RUNNER_DEVICE_MODE"] = "wifi"
	cfg.values["CREDIMI_RUNNER_SERIAL"] = "10.0.0.8:5555"
	if got := d.ConfiguredTargetTitle(); got != "Android phone over Wi-Fi" {
		t.Fatalf("ConfiguredTargetTitle = %q", got)
	}
	if got := d.ConfiguredTargetDetail(); got != "10.0.0.8:5555" {
		t.Fatalf("ConfiguredTargetDetail = %q", got)
	}
}

func TestPageDataFormViewModels(t *testing.T) {
	cfg := &Config{values: map[string]string{
		"CREDIMI_USER_API_KEY":        "test-secret-value-123",
		"CREDIMI_RUNNER_ORGANIZATION": "a",
		"OTEL_ENABLED":                "yes",
	}}
	d := PageData{
		Active: "setup",
		Runner: cfg,
		Data:   map[string]any{"Errors": map[string]string{"CREDIMI_URL": "Required."}},
	}

	if !d.HasErrors() || !d.IsSetup() {
		t.Fatal("expected setup errors")
	}
	field := d.Field("CREDIMI_URL")
	if field.Err != "Required." || field.Key != "CREDIMI_URL" {
		t.Fatalf("Field = %#v", field)
	}
	secret := d.Field("CREDIMI_USER_API_KEY")
	if got := secret.MaskedValue(); strings.Contains(got, "secret123") {
		t.Fatalf("secret was not masked: %q", got)
	}
	if !d.Field("OTEL_ENABLED").Checked() {
		t.Fatal("expected yes bool to be checked")
	}
	selectField := FieldVM{Value: "wifi"}
	if !selectField.Selected("wifi") || selectField.Selected("usb") {
		t.Fatal("Selected returned wrong result")
	}
	if got := string(d.Pretty("<b>prod</b>")); got != "<b>prod</b>" {
		t.Fatalf("Pretty = %q", got)
	}
	if got := d.AvatarInitials(); got != "A" {
		t.Fatalf("single-letter AvatarInitials = %q", got)
	}
	cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = ""
	if got := d.AvatarInitials(); got != "CR" {
		t.Fatalf("default AvatarInitials = %q", got)
	}
	if got := orDash(""); got != "not configured" {
		t.Fatalf("orDash empty = %q", got)
	}
}

func TestPageDataAdditionalHelpers(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	cfg := &Config{values: map[string]string{
		"CREDIMI_RUNNER_TYPE": "redroid",
	}}
	d := PageData{
		Active: "overview",
		Runner: cfg,
		Data: map[string]any{
			"Startup": startupState{Phase: StartupRegistering, Message: "registering"},
		},
	}
	if got := d.StartupPhase(); got != StartupRegistering {
		t.Fatalf("StartupPhase = %q", got)
	}
	if got := d.StartupMessage(); got != "registering" {
		t.Fatalf("StartupMessage = %q", got)
	}
	if got := d.ConfiguredTargetTitle(); got != "Redroid" {
		t.Fatalf("ConfiguredTargetTitle = %q", got)
	}
	cfg.values["CREDIMI_RUNNER_TYPE"] = "ios_simulator"
	cfg.values["BASE_NAME"] = "sim-base"
	if got := d.ConfiguredTargetDetail(); got != "sim-base" {
		t.Fatalf("ConfiguredTargetDetail = %q", got)
	}
	if profile := d.ActiveTargetProfile(); profile.Type != "ios_simulator" {
		t.Fatalf("ActiveTargetProfile = %#v", profile)
	}

	d.Data = map[string]any{"RuntimeStatus": dashboardruntime.RuntimeStatus{RunnerRunning: true}}
	if !d.RuntimeRunning() {
		t.Fatal("expected runtime to be running")
	}
	if got := d.RuntimeTogglePath(); got != "/runtime/stop" {
		t.Fatalf("RuntimeTogglePath = %q", got)
	}
	if got := d.RuntimeToggleLabel(); got != "Stop Runner" {
		t.Fatalf("RuntimeToggleLabel = %q", got)
	}
	if got := d.RuntimeToggleBusyMessage(); !strings.Contains(got, "Stopping") {
		t.Fatalf("RuntimeToggleBusyMessage = %q", got)
	}

	d.Data = map[string]any{"RuntimeStatus": dashboardruntime.RuntimeStatus{}}
	if d.RuntimeRunning() {
		t.Fatal("expected runtime to be stopped")
	}
	if got := d.RuntimeTogglePath(); got != "/runtime/start" {
		t.Fatalf("RuntimeTogglePath stopped = %q", got)
	}
}

func TestPageDataAndroidDevicesIncludesOnlineADBEmulators(t *testing.T) {
	d := PageData{
		Snapshot: Snapshot{Devices: []Device{
			{Serial: "emulator-5554", Name: "Pixel test", Type: "android_emulator", OS: "Android", Status: Online},
			{Serial: "device-1", Name: "Phone", Type: "android_phone", OS: "Android", Status: Online},
			{Serial: "offline-1", Name: "Offline", Type: "android_phone", OS: "Android", Status: Offline},
			{Serial: "ios-1", Name: "iPhone", Type: "ios_simulator", OS: "iOS", Status: Online},
		}},
	}

	devices := d.AndroidDevices()
	if len(devices) != 2 {
		t.Fatalf("AndroidDevices count = %d, devices = %#v", len(devices), devices)
	}
	if devices[0].Serial != "emulator-5554" || devices[1].Serial != "device-1" {
		t.Fatalf("AndroidDevices = %#v", devices)
	}
}

func TestRunnerTypeChoiceHelpers(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	cfg := &Config{values: map[string]string{
		"CREDIMI_RUNNER_TYPE": "ios_simulator",
	}}
	d := PageData{Runner: cfg}

	if got := d.RunnerTypeChoices(); len(got) != 4 || got[1] != "ios_simulator" {
		t.Fatalf("RunnerTypeChoices = %#v", got)
	}
	if !d.SupportsRunnerType("ios_simulator") || d.SupportsRunnerType("missing") {
		t.Fatal("SupportsRunnerType returned unexpected result")
	}
	field := d.Field("CREDIMI_RUNNER_TYPE")
	if len(field.Options) != 4 || field.Options[1] != "ios_simulator" {
		t.Fatalf("Field options = %#v", field.Options)
	}
	if got := TargetProfiles(); len(got) != 4 || got[1].Type != "ios_simulator" {
		t.Fatalf("TargetProfiles = %#v", got)
	}
	if got := d.TargetProfiles(); len(got) != 4 || got[1].Type != "ios_simulator" {
		t.Fatalf("PageData TargetProfiles = %#v", got)
	}
	if got := d.FieldWithLabel("BASE_NAME", "Simulator name"); got.Label != "Simulator name" {
		t.Fatalf("FieldWithLabel = %#v", got)
	}
	if got := d.BaseNameFieldLabel("ios_simulator"); got != "Simulator name" {
		t.Fatalf("BaseNameFieldLabel simulator = %q", got)
	}
	if got := d.BaseNameFieldLabel("android_emulator"); got != "Emulator base name" {
		t.Fatalf("BaseNameFieldLabel emulator = %q", got)
	}
	if got := d.EmulatorBaseNameField(); got.Label != "Emulator base name" {
		t.Fatalf("EmulatorBaseNameField = %#v", got)
	}
	if got := d.SimulatorBaseNameField(); got.Label != "Simulator name" {
		t.Fatalf("SimulatorBaseNameField = %#v", got)
	}
}
