package dashboard

import (
	"strings"
	"testing"
)

func TestIcons_Present(t *testing.T) {
	required := []string{
		"grid", "phone", "workers", "network", "key", "server", "shield",
		"cloud", "activity", "globe", "check", "x", "plus", "refresh",
		"trash", "wifi", "usb", "info", "warn", "chev", "eye", "copy",
		"android", "apple",
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
			Services: []Service{{ID: "runner", Name: "runner", Status: Online}},
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
		Snapshot: Snapshot{},
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
	if strings.Contains(html, `class="sb"`) {
		t.Errorf("setup page should not render sidebar")
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
