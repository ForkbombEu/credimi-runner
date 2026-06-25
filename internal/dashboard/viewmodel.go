package dashboard

import (
	"html/template"
	"strings"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

// View-model helpers callable from templates. Keeps the templates declarative.

func (d PageData) NavOn(name string) string {
	if d.Active == name {
		return "on"
	}
	return ""
}

func (d PageData) DevicesOnline() int {
	n := 0
	for _, x := range d.Snapshot.Devices {
		if x.Status == Online {
			n++
		}
	}
	return n
}
func (d PageData) DevicesTotal() int { return len(d.Snapshot.Devices) }

func (d PageData) DevicesDegraded() int { return d.countDev(Degraded) }
func (d PageData) DevicesOffline() int  { return d.countDev(Offline) }
func (d PageData) countDev(s Status) int {
	n := 0
	for _, x := range d.Snapshot.Devices {
		if x.Status == s {
			n++
		}
	}
	return n
}

func (d PageData) WorkersOnline() int {
	n := 0
	for _, w := range d.Workers {
		if w.Status == Online {
			n++
		}
	}
	return n
}
func (d PageData) WorkersTotal() int { return len(d.Workers) }

func (d PageData) RuntimeHealthy() bool {
	status := d.RuntimeStatus()
	if !status.Configured {
		return false
	}
	if len(d.Snapshot.Services) > 0 {
		return status.ComposeRunning && d.ServicesAllUp()
	}
	return status.RunnerRunning
}

func (d PageData) RuntimeHeadline() string {
	if d.RuntimeHealthy() {
		return "Running"
	}
	if d.RuntimeStatus().Configured {
		return "Needs attention"
	}
	return "Not configured"
}

func (d PageData) RunnerAPIURL() string {
	host := d.Runner.Get("RUNNER_HOST")
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := d.Runner.Get("RUNNER_PORT")
	if port == "" {
		port = dashboardruntime.DefaultRunnerPort
	}
	return "http://" + host + ":" + port
}

func (d PageData) ConfiguredTargetTitle() string {
	switch d.Runner.Get("CREDIMI_RUNNER_TYPE") {
	case "android_emulator":
		return "Android emulator"
	case "ios_simulator":
		return "iOS simulator"
	case "redroid":
		return "Redroid"
	default:
		switch d.Runner.Get("CREDIMI_RUNNER_DEVICE_MODE") {
		case "wifi":
			return "Android phone over Wi-Fi"
		default:
			return "Android phone over USB"
		}
	}
}

func (d PageData) ConfiguredTargetDetail() string {
	switch d.Runner.Get("CREDIMI_RUNNER_TYPE") {
	case "android_emulator":
		return orDash(d.Runner.Get("BASE_NAME"))
	case "ios_simulator":
		return orDash(d.Runner.Get("BASE_NAME"))
	case "redroid":
		return orDash(d.Runner.Get("CREDIMI_RUNNER_SERIAL"))
	default:
		return orDash(d.Runner.Get("CREDIMI_RUNNER_SERIAL"))
	}
}

func (d PageData) ServicesAllUp() bool {
	for _, s := range d.Snapshot.Services {
		if s.Status != Online {
			return false
		}
	}
	return true
}

// PublicURL computes the externally reachable endpoint from the network config.
func (d PageData) PublicURL() string {
	if publicURL := strings.TrimSpace(d.RuntimeStatus().PublicURL); publicURL != "" {
		return publicURL
	}
	mode := d.Runner.Get("CREDIMI_SERVICE_MODE")
	switch mode {
	case "cloudflare-managed":
		if dom := d.Runner.Get("RUNNER_DOMAIN"); dom != "" {
			return "https://" + dom
		}
		return "https://<runner-domain>"
	case "manual":
		host := d.Runner.Get("RUNNER_HOST")
		if host == "0.0.0.0" || host == "" {
			host = "<host-ip>"
		}
		return "http://" + host + ":" + d.Runner.Get("RUNNER_PORT")
	default:
		return "https://<name>.trycloudflare.com"
	}
}

// FieldVM bundles a field with its current value + any validation error.
type FieldVM struct {
	Field
	Value string
	Err   string
}

func (d PageData) errorsMap() map[string]string {
	if e, ok := d.payload()["Errors"].(map[string]string); ok {
		return e
	}
	return nil
}

func (d PageData) payload() map[string]any {
	if m, ok := d.Data.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// HasErrors reports whether the last save produced validation errors.
func (d PageData) HasErrors() bool { return len(d.errorsMap()) > 0 }

func (d PageData) IsSetup() bool { return d.Active == "setup" }

func (d PageData) Flash() string {
	if s, ok := d.payload()["Flash"].(string); ok {
		return s
	}
	return ""
}

func (d PageData) SetupError() string {
	if s, ok := d.payload()["SetupError"].(string); ok {
		return s
	}
	return ""
}

func (d PageData) RuntimeStatus() dashboardruntime.RuntimeStatus {
	if status, ok := d.payload()["RuntimeStatus"].(dashboardruntime.RuntimeStatus); ok {
		return status
	}
	return dashboardruntime.RuntimeStatus{}
}

// Field returns the render model for one config key.
func (d PageData) Field(key string) FieldVM {
	return FieldVM{Field: fieldByKey[key], Value: d.Runner.Get(key), Err: d.errorsMap()[key]}
}

func (d PageData) SetupSteps() []SetupStep {
	return []SetupStep{
		{
			ID:      "identity",
			Title:   "Identity",
			Summary: "Paste your API key and name this runner.",
			Fields:  []string{"CREDIMI_URL", "CREDIMI_USER_API_KEY", "CREDIMI_RUNNER_NAME", "CREDIMI_RUNNER_DESCRIPTION"},
		},
		{
			ID:      "network",
			Title:   "Networking",
			Summary: "How Credimi reaches this runner.",
			Fields:  []string{"CREDIMI_SERVICE_MODE", "RUNNER_DOMAIN", "RUNNER_PUBLIC_URL", "RUNNER_PUBLIC_PORT", "CLOUDFLARE_TUNNEL_TOKEN"},
		},
		{
			ID:      "device",
			Title:   "Device",
			Summary: "Phone, emulator, simulator, and connection mode.",
			Fields:  []string{"CREDIMI_RUNNER_TYPE", "CREDIMI_RUNNER_DEVICE_MODE", "CREDIMI_RUNNER_SERIAL", "CREDIMI_RUNNER_WIFI_IP", "CREDIMI_RUNNER_WIFI_PORT", "RUNNER_IMAGE", "CREDIMI_TEMP_DIR", "ANDROID_KEYS_DIR", "BASE_NAME", "GOLDEN_PATH", "HOST_AVD_HOME_PATH", "HOST_AVD_GOLDEN_PATH", "AVDCTL_SSH_TARGET", "AVDCTL_SSH_PASSWORD", "AVDCTL_SSH_KNOWN_HOSTS_PATH", "AVDCTL_SUDO", "AVDCTL_SUDO_PASSWORD", "REDROID_DATA_DIR", "REDROID_DATA_TAR"},
		},
		{
			ID:      "advanced",
			Title:   "Advanced",
			Summary: "Observability and telemetry exports.",
			Fields:  []string{"OTEL_ENABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SERVICE_NAME"},
		},
		{
			ID:      "review",
			Title:   "Review",
			Summary: "Write .env, generate docker-compose.yaml, and start services.",
		},
	}
}

type SetupStep struct {
	ID      string
	Title   string
	Summary string
	Fields  []string
}

// MaskedValue is the on-screen value for a field (secrets hidden).
func (v FieldVM) MaskedValue() string {
	if v.Secret {
		return maskSecret(v.Value)
	}
	return v.Value
}

// Selected reports whether a select option is the current value.
func (v FieldVM) Selected(opt string) bool { return v.Value == opt }

// Checked reports the bool state for toggle fields.
func (v FieldVM) Checked() bool {
	s := v.Value
	return s == "true" || s == "1" || s == "yes" || s == "on"
}

func (d PageData) Pretty(env string) template.HTML { return template.HTML(env) }

// AvatarInitials returns up to two uppercase letters for the sidebar avatar.
func (d PageData) AvatarInitials() string {
	org := d.Runner.Get("CREDIMI_RUNNER_ORGANIZATION")
	if org == "" {
		return "CR"
	}
	r := []rune(strings.ToUpper(org))
	if len(r) >= 2 {
		return string(r[:2])
	}
	return string(r)
}

func orDash(s string) string {
	if s == "" {
		return "not configured"
	}
	return s
}
