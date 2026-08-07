package dashboard

import (
	"os"
	"strings"
	"testing"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/maintenance"
)

func TestIcons_Present(t *testing.T) {
	required := []string{
		"grid", "phone", "workers", "network", "key", "server", "shield",
		"cloud", "activity", "globe", "check", "x", "plus", "refresh",
		"trash", "wifi", "usb", "info", "warn", "chev", "eye", "copy",
		"gear", "android", "apple",
	}

	for _, name := range required {
		t.Run(name, func(t *testing.T) {
			svg := icon(name)
			if svg == "" {
				t.Errorf("icon %q is missing", name)
			}
			// All icons should contain at least an svg tag
			if svg != "" && !strings.Contains(string(svg), "<svg") {
				t.Errorf("icon %q does not contain svg markup", name)
			}
		})
	}
}

func TestChipClass(t *testing.T) {
	tests := []struct {
		status Status
		class  string
	}{
		{Online, "chip online"},
		{Degraded, "chip degraded"},
		{Offline, "chip offline"},
		{Idle, "chip idle"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := chipClass(tt.status); got != tt.class {
				t.Errorf("chipClass(%s) = %q, want %q", tt.status, got, tt.class)
			}
		})
	}
}

func TestServiceIcon(t *testing.T) {
	tests := []struct{ id, contains string }{
		{"runner", "server"},
		{"caddy", "shield"},
		{"cloudflared", "cloud"},
		{"tunnel", "cloud"},
		{"temporal", "workers"},
		{"unknown", "server"},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			// Just verify it returns non-empty HTML
			got := serviceIcon(tt.id)
			if got == "" {
				t.Errorf("serviceIcon(%q) returned empty", tt.id)
			}
		})
	}
}

func TestDeviceIcon(t *testing.T) {
	if got := deviceIcon("android_phone"); got == "" {
		t.Error("deviceIcon(android_phone) returned empty")
	}
	if got := deviceIcon("ios_simulator"); got == "" {
		t.Error("deviceIcon(ios_simulator) returned empty")
	}
}

func TestNewRenderer(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}
	if r == nil {
		t.Fatal("renderer is nil")
	}
	if r.pages == nil {
		t.Error("pages map is nil")
	}
	if r.frags == nil {
		t.Error("frags template is nil")
	}

	// Verify all expected pages exist.
	expected := []string{"overview", "devices", "workers", "network", "config", "setup"}
	for _, name := range expected {
		if _, ok := r.pages[name]; !ok {
			t.Errorf("missing page template: %s", name)
		}
	}
}

func TestRenderer_Fragment(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}

	// Render a pill fragment.
	html := r.Fragment("pill", PillData{OK: true, Label: "All healthy"})
	if !strings.Contains(html, "All healthy") {
		t.Errorf("pill fragment missing label: %s", html)
	}

	// Render device rows with empty list.
	html = r.Fragment("device_rows", nil)
	if !strings.Contains(html, "No devices attached") {
		t.Errorf("empty device_rows should show placeholder: %s", html)
	}
}

func TestRenderHelpers(t *testing.T) {
	if !hasURL("https://credimi.example") {
		t.Fatal("expected URL detection")
	}
	if hasURL("runner.example") {
		t.Fatal("unexpected URL detection")
	}
	if !isSecret(Field{Secret: true}) || isSecret(Field{}) {
		t.Fatal("isSecret returned unexpected result")
	}
}

func TestRenderer_FragmentPage(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}

	d := PageData{
		Active: "overview",
		Title:  "Overview",
		Runner: &Config{values: Defaults},
		Snapshot: Snapshot{
			Services: []Service{{ID: "runner", Name: "runner", Image: "example.test/runner:latest", Status: Online, Uptime: "Up 2 minutes"}},
		},
		Workers: []Worker{},
		Pill:    PillData{OK: true, Label: "All healthy"},
	}

	html, err := r.FragmentPage("overview", d)
	if err != nil {
		t.Fatalf("FragmentPage failed: %v", err)
	}
	if !strings.Contains(html, "Overview") && !strings.Contains(html, "overview") {
		t.Errorf("fragment page missing content: %s", html[:200])
	}
	if !strings.Contains(html, "Start Runner") {
		t.Fatalf("overview fragment missing runtime start control: %s", html)
	}
	if !strings.Contains(html, "Maintenance") || !strings.Contains(html, "example.test/runner:latest") || !strings.Contains(html, "Up 2 minutes") {
		t.Fatalf("overview fragment missing maintenance details: %s", html)
	}
	if strings.Contains(html, "health-pill") {
		t.Fatalf("overview fragment should not render duplicate health pill: %s", html)
	}
}

func TestRendererOverviewPageIncludesUpgradeLogModal(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	html, err := renderer.Page("overview", PageData{
		Active: "overview",
		Title:  "Overview",
		Runner: &Config{values: Defaults},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "runner-upgrade-modal") || !strings.Contains(html, "data-upgrade-log") || !strings.Contains(html, "data-upgrade-close disabled") {
		t.Fatalf("overview page missing locked upgrade modal: %s", html)
	}
}

func TestRenderer_ConfigPageDropsAdditionalEnvironments(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}

	d := PageData{
		Active: "config",
		Title:  "Config",
		Runner: &Config{values: Defaults},
		Pill:   PillData{OK: true, Label: "Ready"},
	}

	html, err := r.Page("config", d)
	if err != nil {
		t.Fatalf("config page failed: %v", err)
	}
	if strings.Contains(html, "Additional environments") {
		t.Fatalf("config page should not render multi-environment section: %s", html)
	}
}

func TestRenderer_HidesIOSSimulatorOnLinux(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "linux")
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	d := PageData{
		Active:   "setup",
		Title:    "Setup",
		Runner:   &Config{path: "/tmp/credimi/runner/config.toml", values: Defaults},
		Snapshot: Snapshot{},
		Pill:     PillData{OK: true, Label: "Setup"},
	}
	html, err := r.Page("setup", d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, `value="ios_simulator"`) {
		t.Fatalf("linux setup page should not render ios_simulator: %s", html)
	}
}

func TestSetupRendersProgressiveHostWizard(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	html, err := r.Page("setup", PageData{
		Active: "setup",
		Runner: &Config{path: "/tmp/credimi/runner/config.toml", values: Defaults},
		Pill:   PillData{OK: true, Label: "Setup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`data-setup-form`,
		`data-step-target="identity"`,
		`data-step-target="network"`,
		`data-step-target="devices"`,
		`data-step-target="advanced"`,
		`data-step-target="review"`,
		`data-org-value`,
		`data-runner-id-value`,
		`data-auth-seg`,
		`data-net-mode="manual"`,
		`data-runner-conflict-modal-summary`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("setup wizard missing %q", want)
		}
	}
	for _, want := range []string{`data-device-provision`, `data-android-phone-device-select`, `data-android-emulator-assets-panel`, `data-device-provision-template`} {
		if !strings.Contains(html, want) {
			t.Fatalf("setup device provisioning missing %q", want)
		}
	}
}

func TestRenderer_ShowsIOSSimulatorOnDarwin(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	d := PageData{
		Active:   "devices",
		Title:    "Devices",
		Runner:   &Config{values: Defaults},
		Snapshot: Snapshot{},
		Pill:     PillData{OK: true, Label: "Ready"},
	}
	html, err := r.Page("devices", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `value="ios_simulator"`) {
		t.Fatalf("darwin devices page should render ios_simulator: %s", html)
	}
}

func TestRenderer_BaseUsesAuthModeInSidebar(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	d := PageData{
		Active: "overview",
		Title:  "Overview",
		Runner: &Config{values: map[string]string{
			"CREDIMI_RUNNER_ORGANIZATION": "acme",
			"CREDIMI_INTERNAL_ADMIN_KEY":  "secret",
		}},
		Pill: PillData{OK: true, Label: "Ready"},
	}
	html, err := r.Page("overview", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, ">Admin<") {
		t.Fatalf("sidebar should show auth mode, got: %s", html)
	}
	if strings.Contains(html, ">ops<") {
		t.Fatalf("sidebar should not show ops, got: %s", html)
	}
}

func TestRenderer_DevicesInventoryPageContract(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}

	d := PageData{
		Active:   "devices",
		Title:    "Devices",
		Runner:   &Config{values: Defaults},
		Snapshot: Snapshot{},
		Workers:  []Worker{},
		Pill:     PillData{OK: true, Label: "Ready"},
	}

	html, err := r.Page("devices", d)
	if err != nil {
		t.Fatalf("devices page failed: %v", err)
	}
	for _, want := range []string{
		`Configured inventory`,
		`Add device`,
		`IDs are created from the device name and cannot be edited`,
		`Detected devices`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("devices page missing %q", want)
		}
	}
}

func TestStaticCSS_HiddenBeatsModalDisplay(t *testing.T) {
	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), `.modal-bk[hidden], [hidden] { display: none !important; }`) {
		t.Fatal("modal hidden CSS contract is missing")
	}
}

func TestRuntimeBusyOverlaySurvivesUnrelatedMainSwap(t *testing.T) {
	script, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "if (!runtimeOperationActive) hideBusy();") {
		t.Fatal("main swaps must not dismiss an active runtime operation overlay")
	}
}

func TestRenderer_FullPage(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}

	d := PageData{
		Active: "overview",
		Title:  "Overview",
		Runner: &Config{values: Defaults},
		Snapshot: Snapshot{
			Services: []Service{{ID: "runner", Name: "runner", Status: Online}},
		},
		Workers: []Worker{},
		Pill:    PillData{OK: true, Label: "All healthy"},
	}

	html, err := r.Page("overview", d)
	if err != nil {
		t.Fatalf("Page failed: %v", err)
	}
	if !strings.Contains(html, "Credimi Runner") {
		t.Errorf("full page missing title: %s", html[:300])
	}
	if !strings.Contains(html, "htmx") {
		t.Errorf("full page missing htmx script: %s", html[:300])
	}
	if strings.Contains(html, "across environments") {
		t.Fatalf("overview should not render multi-environment copy: %s", html)
	}

	if !strings.Contains(html, `data-copy-value=`) {
		t.Fatalf("overview should render public URL copy action: %s", html)
	}
}

func TestPageData_ViewModels(t *testing.T) {
	d := PageData{
		Active: "overview",
		Runner: &Config{values: Defaults},
		Snapshot: Snapshot{
			Devices: []Device{
				{Serial: "a", Status: Online, Type: "android_phone"},
				{Serial: "b", Status: Online},
				{Serial: "c", Status: Offline},
				{Serial: "d", Status: Degraded},
			},
		},
		Workers: []Worker{
			{ID: "production-mr", Status: Online},
			{ID: "staging-mr", Status: Degraded},
		},
	}

	if n := d.DevicesOnline(); n != 2 {
		t.Errorf("DevicesOnline = %d, want 2", n)
	}
	if n := d.DevicesTotal(); n != 4 {
		t.Errorf("DevicesTotal = %d, want 4", n)
	}
	if n := d.DevicesDegraded(); n != 1 {
		t.Errorf("DevicesDegraded = %d, want 1", n)
	}
	if n := d.DevicesOffline(); n != 1 {
		t.Errorf("DevicesOffline = %d, want 1", n)
	}
	if n := d.WorkersOnline(); n != 1 {
		t.Errorf("WorkersOnline = %d, want 1", n)
	}
	if n := d.WorkersTotal(); n != 2 {
		t.Errorf("WorkersTotal = %d, want 2", n)
	}
	if devices := d.AndroidDevices(); len(devices) != 1 || devices[0].Serial != "a" {
		t.Fatalf("AndroidDevices = %#v", devices)
	}
	if devices := d.AndroidPhoneDevices(); len(devices) != 1 {
		t.Fatalf("AndroidPhoneDevices = %#v", devices)
	}
	if field := d.FieldWithLabel("CREDIMI_URL", "Platform"); field.Label != "Platform" {
		t.Fatalf("FieldWithLabel = %#v", field)
	}
	if got := d.DefaultSSHKnownHostsPath(); got == "" {
		t.Fatal("DefaultSSHKnownHostsPath is empty")
	}
	if steps := d.SetupSteps(); len(steps) != 5 || steps[0].ID != "identity" {
		t.Fatalf("SetupSteps = %#v", steps)
	}
	if got := d.Field("CREDIMI_USER_API_KEY").MaskedValue(); got != "" {
		t.Fatalf("MaskedValue = %q", got)
	}
	if got := d.Pretty("<b>ok</b>"); string(got) != "<b>ok</b>" {
		t.Fatalf("Pretty = %q", got)
	}
}

func TestPageDataRuntimeAndMaintenanceViews(t *testing.T) {
	runner := &Config{values: map[string]string{
		"CREDIMI_RUNNER_ID":           "acme/runner",
		"CREDIMI_DEVICE_COUNT":        "1",
		"CREDIMI_DEVICE_1_ID":         "acme/runner/pixel",
		"CREDIMI_DEVICE_1_NAME":       "Pixel",
		"CREDIMI_DEVICE_1_TYPE":       "android_phone",
		"CREDIMI_DEVICE_1_MODE":       "usb",
		"ANDROID_RUNNER_IMAGE":        "registry.example/runner:v1",
		"ANDROID_PULL_POLICY":         "never",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
		"RUNNER_HOST":                 "0.0.0.0",
		"RUNNER_PORT":                 "9000",
		"CREDIMI_SERVICE_MODE":        "manual",
		"RUNNER_PUBLIC_URL":           "https://runner.example",
		"RUNNER_PUBLIC_PORT":          "443",
		"CREDIMI_USER_API_KEY":        "secret-value",
	}}
	d := PageData{
		Active:   "overview",
		Runner:   runner,
		Snapshot: Snapshot{Services: []Service{{ID: "runner", Expected: true, Critical: true, Status: Online, Image: "registry.example/runner:v2", Uptime: "2m"}}},
		Data: map[string]any{
			"Errors":        map[string]string{"RUNNER_PORT": "Must be a port number."},
			"Flash":         "Saved",
			"RuntimeStatus": dashboardruntime.RuntimeStatus{Configured: true, RunnerRunning: true},
			"Startup":       startupState{Phase: StartupReady, Message: "Ready"},
			"RunnerVersion": "v1.2.3",
			"Maintenance":   maintenance.Status{Runner: maintenance.Component{LatestVersion: "v1.2.4", UpdateAvailable: true}, Image: maintenance.Component{LatestVersion: "v2"}},
		},
	}

	if devices := d.ConfiguredDevices(); len(devices) != 1 || devices[0].ID != "acme/runner/pixel" {
		t.Fatalf("configured devices = %#v", devices)
	}
	if !d.RuntimeHealthy() || d.RuntimeHeadline() != "Running" || !d.RuntimeRunning() {
		t.Fatalf("runtime state headline=%q healthy=%t running=%t", d.RuntimeHeadline(), d.RuntimeHealthy(), d.RuntimeRunning())
	}
	if d.RuntimeTogglePath() != "/runtime/stop" || d.RuntimeToggleLabel() != "Stop Runner" || !strings.Contains(d.RuntimeToggleBusyMessage(), "Stopping") {
		t.Fatalf("runtime toggle = %q %q %q", d.RuntimeTogglePath(), d.RuntimeToggleLabel(), d.RuntimeToggleBusyMessage())
	}
	if d.RunnerAPIURL() != "http://127.0.0.1:9000" || d.PublicURL() != "https://runner.example:443" {
		t.Fatalf("URLs api=%q public=%q", d.RunnerAPIURL(), d.PublicURL())
	}
	if d.RunnerImage() != "registry.example/runner:v2" || d.RunnerContainerDetails() != "Online · 2m" {
		t.Fatalf("runner display image=%q details=%q", d.RunnerImage(), d.RunnerContainerDetails())
	}
	if !d.HasErrors() || d.Field("RUNNER_PORT").Err == "" || d.Flash() != "Saved" || d.StartupPhase() != StartupReady || d.StartupMessage() != "Ready" {
		t.Fatalf("template payload was not exposed: %#v", d)
	}
	if !d.UpgradeAvailable() || d.RunnerVersionState() != "New version available" || d.ImageVersionState() != "Registry check disabled" || d.LatestRunnerVersion() != "v1.2.4" || d.LatestImageVersion() != "v2" {
		t.Fatalf("maintenance view = runner=%q image=%q", d.RunnerVersionState(), d.ImageVersionState())
	}
	if d.RunnerVersion() != "v1.2.3" || d.AvatarInitials() != "AC" || d.Field("CREDIMI_USER_API_KEY").MaskedValue() == "secret-value" {
		t.Fatalf("identity view runner=%q avatar=%q", d.RunnerVersion(), d.AvatarInitials())
	}

	d.Runner.values["CREDIMI_SERVICE_MODE"] = "cloudflare-managed"
	d.Runner.values["RUNNER_DOMAIN"] = ""
	if d.PublicURL() != "https://<runner-domain>" {
		t.Fatalf("managed public URL = %q", d.PublicURL())
	}
	d.Runner.values["CREDIMI_SERVICE_MODE"] = "auto"
	d.Data.(map[string]any)["RuntimeStatus"] = dashboardruntime.RuntimeStatus{Configured: true}
	d.Snapshot.Services[0].Status = Offline
	if d.PublicURL() != "Waiting for quick tunnel URL" || d.RuntimeTogglePath() != "/runtime/start" || d.RuntimeHeadline() != "Needs attention" {
		t.Fatalf("stopped runtime view url=%q toggle=%q headline=%q", d.PublicURL(), d.RuntimeTogglePath(), d.RuntimeHeadline())
	}
}
