package dashboard

import (
	"os"
	"strings"
	"testing"
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

func TestRenderer_SetupPage(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}

	d := PageData{
		Active:   "setup",
		Title:    "Setup",
		Runner:   &Config{path: "/tmp/credimi/runner/.env", values: Defaults},
		Snapshot: Snapshot{Devices: []Device{{Serial: "device-1", Name: "Pixel 8", Type: "android_phone", Mode: "usb", Status: Online}}},
		Workers:  []Worker{},
		Pill:     PillData{OK: true, Label: "Setup"},
	}

	html, err := r.Page("setup", d)
	if err != nil {
		t.Fatalf("setup page failed: %v", err)
	}
	if !strings.Contains(html, "Set up Credimi Runner") {
		t.Errorf("setup page missing heading: %s", html[:300])
	}
	if !strings.Contains(html, "data-setup-form") {
		t.Errorf("setup page missing wizard form: %s", html[:300])
	}
	if !strings.Contains(html, "credimi.io/my/profile/api-keys") {
		t.Errorf("setup page missing API key link")
	}
	if !strings.Contains(html, `name="RUNNER_PORT"`) {
		t.Errorf("setup page missing runner port field")
	}
	if !strings.Contains(html, `data-android-phone-device-select`) || !strings.Contains(html, "device-1") {
		t.Errorf("setup page missing connected Android device selector")
	}
	if !strings.Contains(html, `data-dev-type="redroid"`) || !strings.Contains(html, `name="CREDIMI_RUNNER_WIFI_IP"`) || !strings.Contains(html, `data-avdctl-ssh-control`) {
		t.Errorf("setup page missing Redroid endpoint or SSH controls")
	}
	if !strings.Contains(html, `data-busy-log`) {
		t.Errorf("base template missing busy log output")
	}
	if !strings.Contains(html, `data-startup-phase=`) || !strings.Contains(html, `data-startup-message=`) {
		t.Errorf("base template missing startup state on busy overlay")
	}
	if strings.Contains(html, "data-runner-conflict-choice") {
		t.Errorf("setup page should not render inline runner conflict controls")
	}
	if !strings.Contains(html, "runner-conflict-modal") {
		t.Errorf("setup page missing runner conflict modal")
	}
	if strings.Contains(html, `class="sb"`) {
		t.Errorf("setup page should not render sidebar")
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
		Runner:   &Config{path: "/tmp/credimi/runner/.env", values: Defaults},
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

func TestRenderer_UsesSimulatorNameLabel(t *testing.T) {
	t.Setenv("GOOS_OVERRIDE", "darwin")
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	d := PageData{
		Active:   "devices",
		Title:    "Devices",
		Runner:   &Config{values: map[string]string{"CREDIMI_RUNNER_TYPE": "ios_simulator", "BASE_NAME": "credimi"}},
		Snapshot: Snapshot{},
		Pill:     PillData{OK: true, Label: "Ready"},
	}
	html, err := r.Page("devices", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "Simulator name") {
		t.Fatalf("devices page missing simulator label: %s", html)
	}
	if !strings.Contains(html, "Emulator base name") {
		t.Fatalf("devices page missing emulator label: %s", html)
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

func TestRenderer_DevicesTargetPageContract(t *testing.T) {
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
		`Configured target`,
		`Save target`,
		`Detected devices`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("devices page missing %q", want)
		}
	}
	if strings.Contains(html, "Add device") {
		t.Fatal("devices page should not present add-device flow")
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
	if strings.Contains(html, "OpenAPI docs at") || strings.Contains(html, "/docs") {
		t.Fatalf("overview should not render obsolete docs copy: %s", html)
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
				{Serial: "a", Status: Online},
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
}
