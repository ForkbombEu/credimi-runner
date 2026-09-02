package dashboard

import (
	"html/template"
	"net"
	"net/url"
	"runtime"
	"strings"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/maintenance"
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

func (d PageData) ConfiguredDevices() []dashboardruntime.DeviceRuntimeConfig {
	if d.Runner == nil {
		return nil
	}
	config, err := dashboardruntime.ParseRuntimeConfig(d.Runner.Snapshot())
	if err != nil {
		return nil
	}
	return config.Devices
}

type ConfiguredDeviceView struct {
	dashboardruntime.DeviceRuntimeConfig
	ADBWarning bool
}

func (d PageData) ConfiguredDeviceViews() []ConfiguredDeviceView {
	devices := d.ConfiguredDevices()
	views := make([]ConfiguredDeviceView, 0, len(devices))
	for _, configured := range devices {
		view := ConfiguredDeviceView{DeviceRuntimeConfig: configured}
		requiresADB := configured.Enabled && configured.Type == "android_phone" && (configured.Mode == "usb" || configured.Mode == "wifi")
		if requiresADB {
			found := false
			for _, live := range d.Snapshot.Devices {
				if live.Serial == configured.Serial {
					found = true
					view.ADBWarning = live.Status != Online
					break
				}
			}
			view.ADBWarning = view.ADBWarning || !found
		}
		views = append(views, view)
	}
	return views
}

func (d PageData) AndroidDevices() []Device {
	var devices []Device
	for _, device := range d.Snapshot.Devices {
		if isAndroidADBDevice(device) && device.Status == Online {
			devices = append(devices, device)
		}
	}
	return devices
}

func (d PageData) AndroidPhoneDevices() []Device {
	return d.AndroidDevices()
}

func (d PageData) SupportsDeviceType(deviceType string) bool {
	return deviceType != "ios_simulator" || currentGOOS() == "darwin"
}

func isAndroidADBDevice(device Device) bool {
	return device.OS == "Android" || device.Type == "android_phone" || device.Type == "android_emulator"
}

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
	return status.Configured && status.RunnerRunning
}

func (d PageData) RuntimeRunning() bool {
	status := d.RuntimeStatus()
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

func (d PageData) RuntimeTogglePath() string {
	if d.RuntimeRunning() {
		return "/runtime/stop"
	}
	return "/runtime/start"
}

func (d PageData) RuntimeToggleLabel() string {
	if d.RuntimeRunning() {
		return "Stop Runner"
	}
	return "Start Runner"
}

func (d PageData) RuntimeToggleBusyMessage() string {
	if d.RuntimeRunning() {
		return "Stopping runner services. Keep this page open."
	}
	return "Starting runner services. Keep this page open."
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

func (d PageData) ServicesAllUp() bool {
	for _, s := range d.Snapshot.Services {
		if !s.Expected || !s.Critical {
			continue
		}
		if s.Status != Online {
			return false
		}
	}
	return true
}

func (d PageData) HasCriticalServices() bool {
	for _, s := range d.Snapshot.Services {
		if s.Expected && s.Critical {
			return true
		}
	}
	return false
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
		if publicURL := strings.TrimSpace(d.Runner.Get("RUNNER_PUBLIC_URL")); publicURL != "" {
			return publicURLWithPort(publicURL, d.Runner.Get("RUNNER_PUBLIC_PORT"))
		}
		return "Waiting for manual public URL"
	default:
		return "Waiting for quick tunnel URL"
	}
}

func publicURLWithPort(rawURL, port string) string {
	rawURL = strings.TrimSpace(rawURL)
	port = strings.TrimSpace(port)
	if rawURL == "" || port == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return strings.TrimRight(rawURL, "/") + ":" + port
	}
	host := parsed.Hostname()
	if host == "" {
		return rawURL
	}
	parsed.Host = net.JoinHostPort(host, port)
	return parsed.String()
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

func (d PageData) RuntimeControlsAvailable() bool {
	return true
}

func (d PageData) StartupPhase() StartupPhase {
	if startup, ok := d.payload()["Startup"].(startupState); ok {
		return startup.Phase
	}
	return StartupIdle
}

func (d PageData) StartupMessage() string {
	if startup, ok := d.payload()["Startup"].(startupState); ok {
		return startup.Message
	}
	return ""
}

func (d PageData) RunnerVersion() string {
	if version, ok := d.payload()["RunnerVersion"].(string); ok && strings.TrimSpace(version) != "" {
		return version
	}
	return "dev"
}

func (d PageData) RunnerServiceDetails() string {
	for _, service := range d.Snapshot.Services {
		if service.ID != "runner" {
			continue
		}
		detail := statusLabel(service.Status)
		if strings.TrimSpace(service.Uptime) != "" {
			detail += " · " + service.Uptime
		}
		return detail
	}
	return "Not present"
}

func (d PageData) MaintenanceStatus() maintenance.Status {
	if status, ok := d.payload()["Maintenance"].(maintenance.Status); ok {
		return status
	}
	return maintenance.Status{}
}

func (d PageData) RuntimeImageVisible() bool {
	return runtimeGOOS() != "darwin" && d.Runner != nil && d.Runner.Get("CREDIMI_SERVICE_MODE") != "manual"
}

func (d PageData) ImageVersionState() string { return componentState(d.MaintenanceStatus().Image) }
func (d PageData) ImageCurrentVersion() string {
	return orDash(shortDigest(d.MaintenanceStatus().Image.CurrentVersion))
}
func (d PageData) ImageLatestVersion() string {
	return orDash(shortDigest(d.MaintenanceStatus().Image.LatestVersion))
}

func componentState(component maintenance.Component) string {
	if component.UpdateAvailable {
		return "New version available"
	}
	if component.LatestVersion != "" {
		return "Latest version installed"
	}
	return "Version not checked"
}

func (d PageData) RunnerVersionState() string { return componentState(d.MaintenanceStatus().Runner) }
func (d PageData) RunnerCurrentBuiltAt() string {
	return formatMaintenanceTime(d.MaintenanceStatus().Runner.CurrentBuiltAt)
}
func (d PageData) RunnerLatestBuiltAt() string {
	return formatMaintenanceTime(d.MaintenanceStatus().Runner.LatestBuiltAt)
}
func (d PageData) LatestRunnerVersion() string {
	return orDash(d.MaintenanceStatus().Runner.LatestVersion)
}
func shortDigest(value string) string {
	if len(value) > 19 && strings.HasPrefix(value, "sha256:") {
		return value[:19]
	}
	return value
}
func (d PageData) MaintenanceError() string { return d.MaintenanceStatus().Error }

func formatMaintenanceTime(value time.Time) string {
	if value.IsZero() {
		return "Unavailable"
	}
	return value.Local().Format("2 Jan 2006, 15:04 MST")
}

// Field returns the render model for one config key.
func (d PageData) Field(key string) FieldVM {
	field := fieldByKey[key]
	return FieldVM{Field: field, Value: d.Runner.Get(key), Err: d.errorsMap()[key]}
}

func (d PageData) FieldWithLabel(key, label string) FieldVM {
	field := d.Field(key)
	field.Label = label
	return field
}

func (d PageData) DefaultSSHKnownHostsPath() string {
	return dashboardruntime.DefaultSSHKnownHostsPath()
}

func (d PageData) DefaultWiFiPort() string { return dashboardruntime.DefaultWiFiPort }

func (d PageData) DefaultRedroidDataDir() string { return dashboardruntime.DefaultRedroidDataDir }

func (d PageData) DefaultRedroidDataTar() string { return dashboardruntime.DefaultRedroidDataTar }

func (d PageData) SetupSteps() []SetupStep {
	return []SetupStep{
		{
			ID:      "identity",
			Title:   "Identity",
			Summary: "Paste your API key and name this runner.",
			Fields:  []string{"CREDIMI_URL", "CREDIMI_USER_API_KEY", "RUNNER_PORT", "CREDIMI_RUNNER_NAME", "CREDIMI_RUNNER_DESCRIPTION"},
		},
		{
			ID:      "network",
			Title:   "Networking",
			Summary: "How Credimi reaches this runner.",
			Fields:  []string{"CREDIMI_SERVICE_MODE", "RUNNER_DOMAIN", "RUNNER_PUBLIC_URL", "RUNNER_PUBLIC_PORT", "CLOUDFLARE_TUNNEL_TOKEN"},
		},
		{
			ID:      "devices",
			Title:   "Devices",
			Summary: "Add the execution targets this shared runner will serve.",
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
			Summary: "Write config.toml and start the runner services.",
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

func currentGOOS() string {
	goos := runtimeGOOS()
	if goos != "" {
		return goos
	}
	return runtime.GOOS
}
