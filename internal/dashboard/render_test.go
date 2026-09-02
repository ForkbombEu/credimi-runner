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
		"trash", "wifi", "usb", "info", "warn", "eye", "copy",
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
	if !strings.Contains(html, "Maintenance") || !strings.Contains(html, "Up 2 minutes") {
		t.Fatalf("overview fragment missing maintenance details: %s", html)
	}
	if strings.Contains(html, "health-pill") {
		t.Fatalf("overview fragment should not render duplicate health pill: %s", html)
	}
}

func TestRendererNetworkUsesSetupServiceModeLabels(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	html, err := renderer.Page("network", PageData{
		Active: "network",
		Title:  "Network",
		Runner: &Config{values: Defaults},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"data-val=\"auto\"", "Auto", "Quick tunnel", "data-val=\"cloudflare-managed\"", "Managed", "Named tunnel", "data-val=\"manual\"", "Manual", "Self-managed"} {
		if !strings.Contains(html, want) {
			t.Fatalf("network page missing %q: %s", want, html)
		}
	}
	for _, obsolete := range []string{"Instant trycloudflare.com URL", "Your domain via Cloudflare", "Bind host port, no tunnel", ">Direct<"} {
		if strings.Contains(html, obsolete) {
			t.Fatalf("network page contains obsolete label %q: %s", obsolete, html)
		}
	}
	for _, want := range []string{"Advanced infrastructure", "Show internal listener and edge settings", "Internal runner API port"} {
		if !strings.Contains(html, want) {
			t.Fatalf("network page missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "Temporal</span>") || strings.Contains(html, "Edge &amp; tunnel") {
		t.Fatalf("network page exposes unrelated service details: %s", html)
	}
	manual := strings.Index(html, "Manual public URL")
	endpoint := strings.Index(html, "Public endpoint")
	managed := strings.Index(html, "Runner domain")
	if endpoint < 0 || manual < endpoint || managed < manual {
		t.Fatalf("public endpoint fields are ordered incorrectly: endpoint=%d manual=%d managed=%d", endpoint, manual, managed)
	}
}

func TestRendererConfigGroupsTemporalWithCredimiPlatform(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	html, err := renderer.Page("config", PageData{Active: "config", Title: "API & Config", Runner: &Config{values: Defaults}})
	if err != nil {
		t.Fatal(err)
	}
	platform := strings.Index(html, "Credimi platform connection")
	credimi := strings.Index(html, "Credimi platform URL")
	temporal := strings.Index(html, "Temporal address")
	if platform < 0 || credimi < platform || temporal < credimi {
		t.Fatalf("Credimi platform connection fields are ordered incorrectly: platform=%d credimi=%d temporal=%d", platform, credimi, temporal)
	}
}

func TestRendererConfigIncludesDashboardToken(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	html, err := renderer.Page("config", PageData{Active: "config", Title: "API & Config", Runner: &Config{values: Defaults}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `name="DASHBOARD_TOKEN"`) || !strings.Contains(html, `type="password"`) {
		t.Fatalf("config page does not render dashboard token as a secret: %s", html)
	}
}

func TestRuntimeStatusOmitsInternalRunnerAPIAddress(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	html := renderer.Fragment("runtime_status", PageData{
		Runner: &Config{values: map[string]string{"CREDIMI_SERVICE_MODE": "manual"}},
		Data:   map[string]any{"RuntimeStatus": dashboardruntime.RuntimeStatus{Configured: true, RunnerRunning: true}},
	})
	if strings.Contains(html, "Runner API") || strings.Contains(html, "127.0.0.1") {
		t.Fatalf("runtime status exposes an internal runner address: %s", html)
	}
	if !strings.Contains(html, "Public URL") {
		t.Fatalf("runtime status lost public endpoint: %s", html)
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
	if !strings.Contains(html, `name="ADB_SCREEN_RECORD_SIZE"`) {
		t.Fatalf("config page should render the screen recording size setting: %s", html)
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
	if !strings.Contains(html, `name="ADB_SCREEN_RECORD_SIZE"`) {
		t.Fatalf("setup page should render the screen recording size setting: %s", html)
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
		`data-manual-public-url-field`,
		`data-manual-public-url-error`,
		`data-runner-conflict-modal-summary`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("setup wizard missing %q", want)
		}
	}
	for _, want := range []string{`data-device-provision`, `data-android-phone-device-select`, `data-android-emulator-assets-panel`, `data-device-provision-template`, `AVDCTL_SSH_TARGET`, `AVDCTL_SSH_KNOWN_HOSTS_PATH`, `AVDCTL_SUDO`, `type="password" name="AVDCTL_SSH_PASSWORD"`, `type="password" name="AVDCTL_SUDO_PASSWORD"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("setup device provisioning missing %q", want)
		}
	}
	if strings.Count(html, `name="CREDIMI_RUNNER_TYPE"`) != 2 || strings.Count(html, `name="CREDIMI_RUNNER_DEVICE_MODE"`) != 2 {
		t.Fatalf("each rendered device card should have one canonical type and mode field: %s", html)
	}
	if strings.Contains(html, `type="radio" name="CREDIMI_RUNNER_TYPE"`) || strings.Contains(html, `type="radio" name="CREDIMI_RUNNER_DEVICE_MODE"`) {
		t.Fatalf("device radios must use UI-only names: %s", html)
	}
	for _, want := range []string{"data-device-type-value", "data-device-mode-value", "data-device-type-ui", "data-device-mode-ui", "data-setup-device-field"} {
		if !strings.Contains(html, want) {
			t.Fatalf("setup device card missing %q", want)
		}
	}
	if !strings.Contains(html, `data-setup-device-count`) {
		t.Fatalf("setup form missing indexed device count: %s", html)
	}
	script, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "reindexSetupDeviceCards") || !strings.Contains(string(script), "SETUP_DEVICE_${index}_") {
		t.Fatalf("setup script missing indexed device field contract")
	}
	for _, want := range []string{"initializeDeviceProvisionCard", "device_type_ui_${id}", "device_mode_ui_${id}"} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("device card initialization missing %q", want)
		}
	}
	for _, want := range []string{
		"syncManualPublicURLError",
		"data-manual-public-url-error",
		"Enter a complete URL starting with http:// or https://.",
		"if (mode === 'manual') return !valueMissing('RUNNER_PUBLIC_URL');",
		"setupDeviceFieldValue(card, fieldName)",
		"fields.find((field) => !field.disabled)",
	} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("setup manual URL validation missing %q", want)
		}
	}
	for _, want := range []string{
		"const form = document.querySelector('[data-device-add-form]');",
		"if ($('.app.setup-shell'))",
		"window.location.assign(dashboardURL(operation.refresh || '/', operation.recoveryToken));",
	} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("dashboard script missing %q", want)
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

	values := cloneStringMap(Defaults)
	values["CREDIMI_RUNNER_ID"] = "acme/runner"
	values["CREDIMI_DEVICE_COUNT"] = "2"
	values["CREDIMI_DEVICE_1_ID"] = "acme/runner/pixel"
	values["CREDIMI_DEVICE_1_NAME"] = "Pixel"
	values["CREDIMI_DEVICE_1_TYPE"] = "android_phone"
	values["CREDIMI_DEVICE_1_MODE"] = "usb"
	values["CREDIMI_DEVICE_1_SERIAL"] = "pixel-1"
	values["CREDIMI_DEVICE_1_ENABLED"] = "true"
	values["CREDIMI_DEVICE_2_ID"] = "acme/runner/emulator"
	values["CREDIMI_DEVICE_2_NAME"] = "Emulator"
	values["CREDIMI_DEVICE_2_TYPE"] = "android_emulator"
	values["CREDIMI_DEVICE_2_MODE"] = "emulator"
	values["CREDIMI_DEVICE_2_ENABLED"] = "false"
	d := PageData{
		Active:   "devices",
		Title:    "Devices",
		Runner:   &Config{values: values},
		Snapshot: Snapshot{Devices: []Device{{Serial: "detected-1", Status: Online}}},
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
		`data-device-edit`,
		`data-device-form-cancel`,
		`Cancel edit`,
		`data-busy-title="Adding device"`,
		`data-busy-controller-progress="true"`,
		`hx-boost="false"`,
		`IDs are created from the device name and cannot be edited`,
		`Detected devices`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("devices page missing %q", want)
		}
	}
	if strings.Count(html, `data-device-form-cancel`) != 1 {
		t.Fatalf("devices page should render one edit cancellation action: %s", html)
	}
	if !strings.Contains(html, `<a class="sb-env" href="/devices"`) {
		t.Fatal("runner sidebar identity should link to the Devices page")
	}
	if strings.Contains(html, `class="chev"`) {
		t.Fatal("runner sidebar identity should not render a dropdown chevron")
	}
	if !strings.Contains(html, `Devices<span class="count">2</span>`) {
		t.Fatalf("sidebar device count should use configured inventory: %s", html)
	}
	if configured, detected := strings.Index(html, "Configured inventory"), strings.Index(html, "Detected devices"); configured < 0 || detected < 0 || configured > detected {
		t.Fatal("detected devices should appear after configured inventory")
	}
	if detected, add := strings.Index(html, "Detected devices"), strings.Index(html, "Add device"); detected < 0 || add < 0 || detected > add {
		t.Fatal("detected devices should appear before the add-device form")
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

func TestStaticRedroidSSHToggleClearsCanonicalSudo(t *testing.T) {
	script, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	if !strings.Contains(content, "setFieldValue(form, 'AVDCTL_SUDO', 'false')") {
		t.Fatal("disabling Redroid SSH must clear the submitted canonical sudo value")
	}
	if strings.Contains(content, "setToggleValue(form, 'AVDCTL_SUDO', false)") {
		t.Fatal("Redroid SSH toggle must not update only the visual toggle helper")
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

func TestStaticRuntimeRecoveryUsesTokenAndWallClockDeadline(t *testing.T) {
	script, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	for _, want := range []string{
		"const runtimeRecoveryMaxDuration = 15 * 60 * 1000;",
		"const deadline = Date.now() + runtimeRecoveryMaxDuration;",
		"dashboardURL('/startup/status', operation.recoveryToken)",
		"runtimeRecoveryAbort.abort()",
		"clearTimeout(timeout);",
		"finishRuntimeRecoveryTimeout()",
		"Math.min(runtimeRecoveryRequestTimeout, deadline - Date.now())",
		"fetch(dashboardURL(url), { headers: { Accept: 'application/json' } })",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("runtime recovery is missing %q", want)
		}
	}
	if strings.Contains(content, "runtimeRecoveryMaxAttempts") {
		t.Fatal("recovery must use a wall-clock deadline, not an attempt count")
	}
}

func TestStaticDashboardTokenHasOneCurrentSource(t *testing.T) {
	script, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	for _, want := range []string{
		"let currentDashboardToken =",
		"function setDashboardToken(token)",
		"history.replaceState",
		"else url.searchParams.delete('token');",
		"if (operation.recoveryToken !== undefined) setDashboardToken(operation.recoveryToken);",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("dashboard token source is missing %q", want)
		}
	}
	if strings.Contains(content, "new URLSearchParams(window.location.search).get('token') : tokenOverride") {
		t.Fatal("dashboard URL helper must not repeatedly restore a stale query token")
	}
}

func TestStaticDashboardRequestsPreserveQueryToken(t *testing.T) {
	script, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	for _, want := range []string{
		"htmx:configRequest",
		"preserveDashboardToken",
		"[sse-connect]",
		"fetch(dashboardURL(url)",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("dashboard token preservation missing %q", want)
		}
	}
}

func TestStaticBusyPollingIsSequential(t *testing.T) {
	script, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	if strings.Contains(content, "setInterval(") {
		t.Fatal("dashboard network polling must schedule the next request only after the current request completes")
	}
	for _, want := range []string{
		"setTimeout(pollBusyControllerOperation, 500)",
		"setTimeout(pollBusyStartupStatus, 1500)",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("sequential busy polling missing %q", want)
		}
	}
}

func TestStaticAppShowsDeviceProvisioningProgress(t *testing.T) {
	script, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`/api/controller/operations/current`,
		`pollBusyControllerOperation`,
		`trigger.matches('[data-device-add-form]')`,
		`controllerProgress: trigger.dataset.busyControllerProgress === 'true'`,
		`deviceSubmitInFlight`,
	} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("device operation progress UI missing %q", want)
		}
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
			"Maintenance":   maintenance.Status{Runner: maintenance.Component{LatestVersion: "v1.2.4", UpdateAvailable: true}},
		},
	}

	if devices := d.ConfiguredDevices(); len(devices) != 1 || devices[0].ID != "acme/runner/pixel" {
		t.Fatalf("configured devices = %#v", devices)
	}
	if views := d.ConfiguredDeviceViews(); len(views) != 1 || !views[0].ADBWarning {
		t.Fatalf("missing configured ADB warning for absent USB device: %#v", views)
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
	d.Runner.values["RUNNER_HOST"] = "::1"
	if d.RunnerAPIURL() != "http://[::1]:9000" {
		t.Fatalf("IPv6 API URL=%q", d.RunnerAPIURL())
	}
	d.Runner.values["RUNNER_HOST"] = "::"
	if d.RunnerAPIURL() != "http://127.0.0.1:9000" {
		t.Fatalf("wildcard API URL=%q", d.RunnerAPIURL())
	}
	if d.RunnerServiceDetails() != "Online · 2m" {
		t.Fatalf("runner details=%q", d.RunnerServiceDetails())
	}
	if !d.HasErrors() || d.Field("RUNNER_PORT").Err == "" || d.Flash() != "Saved" || d.StartupPhase() != StartupReady || d.StartupMessage() != "Ready" {
		t.Fatalf("template payload was not exposed: %#v", d)
	}
	if d.RunnerVersionState() != "New version available" || d.LatestRunnerVersion() != "v1.2.4" {
		t.Fatalf("maintenance view = runner=%q", d.RunnerVersionState())
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
