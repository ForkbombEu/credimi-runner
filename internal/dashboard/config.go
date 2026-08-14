package dashboard

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	stdruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// Config: compatibility view over the typed TOML runner configuration.
//
// The field registry drives both the rendered form and the TOML compatibility
// mapping, so
// there is exactly one place to add or change a setting.
// ─────────────────────────────────────────────────────────────────────────────

type FieldType string

const (
	TypeText   FieldType = "text"
	TypeSelect FieldType = "select"
	TypeBool   FieldType = "bool"
)

type Field struct {
	Key      string
	Label    string
	Group    string
	Type     FieldType
	Secret   bool
	Required bool
	Hint     string
	Options  []string // for select
}

// Registry — order matters; it is the on-screen and on-disk order.
var Registry = []Field{
	// Identity
	{Key: "CREDIMI_URL", Label: "Credimi platform URL", Group: "Identity", Type: TypeText, Required: true, Hint: "The Credimi instance this runner registers with."},
	{Key: "CREDIMI_RUNNER_ID", Label: "Runner ID", Group: "Identity", Type: TypeText, Required: true, Hint: "org-slug/runner-name. Required for workers to start."},
	{Key: "CREDIMI_RUNNER_NAME", Label: "Runner name", Group: "Identity", Type: TypeText},
	{Key: "CREDIMI_RUNNER_DESCRIPTION", Label: "Runner description", Group: "Identity", Type: TypeText, Hint: "Optional note shown to operators, for example the physical device or simulator version."},
	{Key: "CREDIMI_RUNNER_ORGANIZATION", Label: "Organization", Group: "Identity", Type: TypeText},
	{Key: "CREDIMI_RUNNER_PUBLISHED", Label: "Publish runner", Group: "Identity", Type: TypeBool, Hint: "Allow published Credimi organizations to schedule pipelines on this runner."},
	// Authentication
	{Key: "CREDIMI_USER_API_KEY", Label: "User API key", Group: "Authentication", Type: TypeText, Secret: true, Hint: "Scoped to your Credimi organization. Treat as a secret."},
	{Key: "CREDIMI_INTERNAL_ADMIN_KEY", Label: "Internal admin key", Group: "Authentication", Type: TypeText, Secret: true, Hint: "Forwarded as the Credimi-Api-Key header. Grants admin-scoped workers."},
	// Temporal
	{Key: "TEMPORAL_ADDRESS", Label: "Temporal address", Group: "Temporal", Type: TypeText, Hint: "gRPC endpoint workers poll for tasks."},
	// Network
	{Key: "CREDIMI_SERVICE_MODE", Label: "Service mode", Group: "Network", Type: TypeSelect,
		Options: []string{"auto", "cloudflare-managed", "manual"}, Hint: "auto = quick tunnel · cloudflare-managed = named tunnel · manual = direct."},
	{Key: "RUNNER_HOST", Label: "Bind host", Group: "Network", Type: TypeText},
	{Key: "RUNNER_PORT", Label: "Runner port", Group: "Network", Type: TypeText, Hint: "Local runner API port. Default is 8050."},
	{Key: "RUNNER_CADDY_SITE", Label: "Caddy site address", Group: "Network", Type: TypeText, Hint: "Keep :80 behind Cloudflare Tunnel."},
	{Key: "RUNNER_DOMAIN", Label: "Runner domain", Group: "Network", Type: TypeText, Hint: "Public hostname pointed at http://caddy:80."},
	{Key: "RUNNER_PUBLIC_URL", Label: "Manual public URL", Group: "Network", Type: TypeText, Hint: "Required when service mode is manual."},
	{Key: "RUNNER_PUBLIC_PORT", Label: "Manual public port", Group: "Network", Type: TypeText, Hint: "Optional public port for manual registration."},
	{Key: "CLOUDFLARE_TUNNEL_TOKEN", Label: "Tunnel token", Group: "Network", Type: TypeText, Secret: true},
	{Key: "DASHBOARD_TOKEN", Label: "Dashboard token", Group: "Network", Type: TypeText, Secret: true, Hint: "Optional. When empty, the dashboard is reachable without authentication."},
	// Observability
	{Key: "OTEL_ENABLED", Label: "Export telemetry", Group: "Observability", Type: TypeBool},
	{Key: "OTEL_EXPORTER_OTLP_ENDPOINT", Label: "OTLP endpoint", Group: "Observability", Type: TypeText},
	{Key: "OTEL_SERVICE_NAME", Label: "Service name", Group: "Observability", Type: TypeText},
	// Advanced
	{Key: "CREDIMI_TEMP_DIR", Label: "Temp directory", Group: "Advanced", Type: TypeText},
	{Key: "ANDROID_RUNNER_IMAGE", Label: "Android runner image", Group: "Advanced", Type: TypeText, Hint: "One image serves every Android device. Use credimi-runner:local for local development."},
	{Key: "ANDROID_PULL_POLICY", Label: "Android image pull policy", Group: "Advanced", Type: TypeSelect, Options: []string{"if-not-present", "always", "never"}, Hint: "Use never with a locally built image."},
}

var fieldByKey = func() map[string]Field {
	m := make(map[string]Field, len(Registry))
	for _, f := range Registry {
		m[f.Key] = f
	}
	return m
}()

// Defaults applied when the TOML file is missing a key.
var Defaults = map[string]string(dashboardruntime.DefaultValues())

// Config is a concurrency-safe compatibility view backed by config.toml.
type Config struct {
	mu      sync.RWMutex
	path    string
	values  map[string]string
	rawTail []string // comments / unknown keys preserved verbatim
}

// ConfigDir resolves the runner config directory, honoring an override.
func ConfigDir() string {
	return dashboardruntime.DefaultConfigDir()
}

func LoadConfig(dir string) (*Config, error) {
	c := &Config{path: filepath.Join(dir, "config.toml"), values: map[string]string{}}
	for k, v := range Defaults {
		c.values[k] = v
	}
	if _, err := os.Stat(c.path); os.IsNotExist(err) {
		if image := strings.TrimSpace(os.Getenv(dashboardruntime.BootstrapImageEnv)); image != "" {
			c.values["ANDROID_RUNNER_IMAGE"] = image
		}
		if policy := strings.TrimSpace(os.Getenv(dashboardruntime.BootstrapPullPolicyEnv)); policy != "" {
			c.values["ANDROID_PULL_POLICY"] = policy
		}
		return c, nil
	} else if err != nil {
		return nil, err
	}
	cfg, err := runnerconfig.LoadFile(c.path)
	if err != nil {
		return nil, err
	}
	values, err := valuesFromTOML(cfg)
	if err != nil {
		return nil, err
	}
	c.values = values
	return c, nil
}

func (c *Config) Exists() bool {
	_, err := os.Stat(c.path)
	return err == nil
}

func (c *Config) Path() string {
	return c.path
}

func (c *Config) Get(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.values[key]
}

func (c *Config) Bool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(c.Get(key)))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

func (c *Config) Snapshot() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

// AuthMode reports "admin" when an internal admin key is set, else "user".
func (c *Config) AuthMode() string {
	if c.Get("CREDIMI_INTERNAL_ADMIN_KEY") != "" {
		return "admin"
	}
	return "user"
}

// Validate returns a map of key→error message; empty means valid.
func Validate(vals map[string]string) map[string]string {
	errs := map[string]string{}
	for _, f := range Registry {
		v := strings.TrimSpace(vals[f.Key])
		if f.Required && v == "" {
			errs[f.Key] = "Required."
			continue
		}
		if v == "" {
			continue
		}
		switch f.Key {
		case "CREDIMI_RUNNER_ID":
			if !regexp.MustCompile(`^[\w.-]+/[\w.-]+$`).MatchString(v) {
				errs[f.Key] = "Must be org-slug/runner-name."
			}
		case "CREDIMI_URL", "RUNNER_DOMAIN", "OTEL_EXPORTER_OTLP_ENDPOINT":
			if strings.Contains(v, "://") {
				if u, err := url.Parse(v); err != nil || u.Host == "" {
					errs[f.Key] = "Not a valid URL."
				}
			}
		case "RUNNER_PORT", "RUNNER_PUBLIC_PORT":
			if !validatePort(v) {
				errs[f.Key] = "Must be a port number."
			}
		}
	}
	return errs
}

func validatePort(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	port, err := strconv.Atoi(value)
	return err == nil && port >= 1 && port <= 65535
}

// Apply validates and persists incoming form values as typed TOML atomically.
func (c *Config) Apply(incoming map[string]string) (map[string]string, error) {
	normalized, err := normalizedConfigValues(c.Snapshot(), incoming, currentGOOS())
	if err != nil {
		return map[string]string{"CREDIMI_RUNNER_ID": err.Error()}, fmt.Errorf("validation failed")
	}
	next := map[string]string(normalized)
	if errs := Validate(next); len(errs) > 0 {
		return errs, fmt.Errorf("validation failed")
	}
	c.mu.Lock()
	c.values = next
	c.mu.Unlock()
	return nil, c.write()
}

func normalizedConfigValues(current, incoming map[string]string, goos string) (dashboardruntime.Values, error) {
	next := cloneStringMap(current)
	for _, f := range Registry {
		if f.Type == TypeBool {
			if v, present := incoming[f.Key]; present {
				next[f.Key] = boolStr(isTruthyFormValue(v))
			}
			continue
		}
		if v, ok := incoming[f.Key]; ok {
			next[f.Key] = strings.TrimSpace(v)
		}
	}
	return dashboardruntime.NormalizeValues(dashboardruntime.Values(next), goos)
}

// write converts the compatibility form values to typed TOML atomically.
func (c *Config) write() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cfg, err := configFromValues(c.values)
	if err != nil {
		return err
	}
	return runnerconfig.WriteFile(c.path, cfg)
}

func writeIndexedDeviceBlocks(b *strings.Builder, values map[string]string) {
	config, err := dashboardruntime.ParseRuntimeConfig(dashboardruntime.Values(values))
	if err != nil || len(config.Devices) == 0 {
		return
	}
	fmt.Fprintf(b, "\n# --- Device inventory (managed by Credimi Runner; do not edit generated keys) ---\nCREDIMI_DEVICE_COUNT=%d\n", len(config.Devices))
	for _, device := range config.Devices {
		fmt.Fprintf(b, "\n# --- Device %d: %s ---\n", device.Index, device.Name)
		keys := make([]string, 0, len(device.Values))
		for key := range device.Values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(b, "CREDIMI_DEVICE_%d_%s=%s\n", device.Index, key, quote(device.Values[key]))
		}
	}
}

// RawEnv renders the compatibility view. When mask is true, secrets are
// partially hidden; the persisted source of truth remains TOML.
func (c *Config) RawEnv(mask bool) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var b strings.Builder
	b.WriteString("# config.toml compatibility view\n# Managed by the credimi-runner dashboard\n\n")
	group := ""
	for _, f := range Registry {
		if f.Group != group {
			group = f.Group
			fmt.Fprintf(&b, "# ── %s ──\n", group)
		}
		v := c.values[f.Key]
		if mask && f.Secret {
			v = maskSecret(v)
		}
		fmt.Fprintf(&b, "%s=%s\n", f.Key, v)
	}
	return b.String()
}

// valuesFromTOML exposes the historical field names to the unchanged
// dashboard templates and handlers. It is deliberately one-way at the UI
// boundary: the file on disk remains typed TOML.
func valuesFromTOML(cfg runnerconfig.Config) (map[string]string, error) {
	values := map[string]string{}
	for k, v := range Defaults {
		values[k] = v
	}
	defaultValue := func(value, fallback string) string {
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return value
	}
	values["CREDIMI_URL"] = cfg.Credimi.URL
	values["CREDIMI_RUNNER_ID"] = cfg.Runner.ID
	values["CREDIMI_RUNNER_NAME"] = cfg.Runner.Name
	values["CREDIMI_RUNNER_ORGANIZATION"] = cfg.Runner.Organization
	values["CREDIMI_RUNNER_DESCRIPTION"] = cfg.Runner.Description
	values["CREDIMI_RUNNER_PUBLISHED"] = strconv.FormatBool(cfg.Runner.Published)
	values["CREDIMI_USER_API_KEY"] = cfg.Credimi.UserAPIKey
	values["CREDIMI_INTERNAL_ADMIN_KEY"] = cfg.Credimi.InternalAdminKey
	values["TEMPORAL_ADDRESS"] = cfg.Temporal.Address
	values["DASHBOARD_TOKEN"] = cfg.Server.DashboardToken
	values["OTEL_ENABLED"] = strconv.FormatBool(cfg.Observability.Enabled)
	values["OTEL_EXPORTER_OTLP_ENDPOINT"] = defaultValue(cfg.Observability.OTLPEndpoint, values["OTEL_EXPORTER_OTLP_ENDPOINT"])
	values["OTEL_SERVICE_NAME"] = defaultValue(cfg.Observability.ServiceName, values["OTEL_SERVICE_NAME"])
	values["CREDIMI_TEMP_DIR"] = defaultValue(cfg.Storage.TempDir, values["CREDIMI_TEMP_DIR"])
	values["ANDROID_RUNNER_IMAGE"] = defaultValue(cfg.Android.RunnerImage, values["ANDROID_RUNNER_IMAGE"])
	values["ANDROID_PULL_POLICY"] = defaultValue(cfg.Android.PullPolicy, values["ANDROID_PULL_POLICY"])
	values["ANDROID_NETWORK"] = defaultValue(cfg.Android.Network, values["ANDROID_NETWORK"])
	values["ANDROID_STATE_VOLUME"] = defaultValue(cfg.Android.StateVolume, values["ANDROID_STATE_VOLUME"])
	values["ANDROID_TOOL_CACHE_VOLUME"] = defaultValue(cfg.Android.ToolCacheVolume, values["ANDROID_TOOL_CACHE_VOLUME"])
	values["ANDROID_SDK_VOLUME"] = defaultValue(cfg.Android.SDKVolume, values["ANDROID_SDK_VOLUME"])
	values["ANDROID_ADB_KEYS_PATH"] = cfg.Android.ADBKeysPath
	if host, port, err := net.SplitHostPort(cfg.Server.APIListen); err == nil {
		values["RUNNER_HOST"], values["RUNNER_PORT"] = host, port
	}
	if host, port, err := net.SplitHostPort(cfg.Server.DashboardListen); err == nil {
		values["DASHBOARD_HOST"], values["DASHBOARD_PORT"] = host, port
	}
	switch cfg.Exposure.Mode {
	case "quick_tunnel":
		values["CREDIMI_SERVICE_MODE"] = "auto"
	case "named_tunnel":
		values["CREDIMI_SERVICE_MODE"] = "cloudflare-managed"
	default:
		values["CREDIMI_SERVICE_MODE"] = "manual"
	}
	values["RUNNER_PUBLIC_URL"] = cfg.Exposure.PublicURL
	values["RUNNER_PUBLIC_PORT"] = cfg.Exposure.PublicPort
	values["RUNNER_DOMAIN"] = cfg.Exposure.Domain
	values["RUNNER_CADDY_SITE"] = defaultValue(cfg.Exposure.CaddySite, dashboardruntime.DefaultRunnerCaddySite)
	values["CLOUDFLARE_TUNNEL_TOKEN"] = cfg.Exposure.CloudflareToken
	values["CREDIMI_DEVICE_COUNT"] = strconv.Itoa(len(cfg.Devices))
	for i, device := range cfg.Devices {
		prefix := fmt.Sprintf("CREDIMI_DEVICE_%d_", i+1)
		values[prefix+"ID"] = device.ID
		values[prefix+"NAME"] = device.Name
		values[prefix+"DESCRIPTION"] = device.Description
		values[prefix+"ENABLED"] = strconv.FormatBool(device.Enabled)
		legacyType := string(device.Type)
		if device.Type == runnerconfig.DeviceAndroidPhysical {
			legacyType = "android_phone"
		}
		values[prefix+"TYPE"] = legacyType
		switch device.Type {
		case runnerconfig.DeviceAndroidPhysical:
			values[prefix+"MODE"] = device.AndroidPhysical.Transport
			switch device.AndroidPhysical.Transport {
			case "usb":
				values[prefix+"SERIAL"] = device.AndroidPhysical.Serial
			case "wifi":
				values[prefix+"WIFI_IP"] = device.AndroidPhysical.WiFiIP
				values[prefix+"WIFI_PORT"] = device.AndroidPhysical.WiFiPort
				values[prefix+"SERIAL"] = dashboardruntime.AndroidWiFiSerial(device.AndroidPhysical.WiFiIP, device.AndroidPhysical.WiFiPort)
			}
		case runnerconfig.DeviceAndroidEmulator:
			values[prefix+"MODE"] = "emulator"
			values[prefix+"AVD_NAME"] = device.AndroidEmulator.AVDName
			values[prefix+"BASE_NAME"] = device.AndroidEmulator.BaseName
			values[prefix+"GOLDEN_PATH"] = device.AndroidEmulator.GoldenSource
		case runnerconfig.DeviceRedroid:
			values[prefix+"MODE"] = "redroid"
			values[prefix+"WIFI_IP"] = device.Redroid.Host
			values[prefix+"WIFI_PORT"] = strconv.Itoa(device.Redroid.ADBPort)
			values[prefix+"SERIAL"] = dashboardruntime.AndroidWiFiSerial(device.Redroid.Host, strconv.Itoa(device.Redroid.ADBPort))
			values[prefix+"REDROID_DATA_DIR"] = device.Redroid.DataDir
			values[prefix+"REDROID_DATA_TAR"] = device.Redroid.DataArchive
		case runnerconfig.DeviceIOSSimulator:
			values[prefix+"MODE"] = "no_device"
			values[prefix+"IOS_UDID"] = device.IOSSimulator.UDID
		}
	}
	return values, nil
}

func configFromValues(values map[string]string) (runnerconfig.Config, error) {
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(values), currentGOOS())
	if err != nil {
		return runnerconfig.Config{}, err
	}
	cfg := runnerconfig.Bootstrap()
	cfg.Runner.ID = normalized["CREDIMI_RUNNER_ID"]
	cfg.Runner.Name = normalized["CREDIMI_RUNNER_NAME"]
	cfg.Runner.Organization = normalized["CREDIMI_RUNNER_ORGANIZATION"]
	cfg.Runner.Description = normalized["CREDIMI_RUNNER_DESCRIPTION"]
	cfg.Runner.Published = isTruthyFormValue(normalized["CREDIMI_RUNNER_PUBLISHED"])
	cfg.Credimi.URL = normalized["CREDIMI_URL"]
	cfg.Credimi.UserAPIKey = normalized["CREDIMI_USER_API_KEY"]
	cfg.Credimi.InternalAdminKey = normalized["CREDIMI_INTERNAL_ADMIN_KEY"]
	if cfg.Credimi.InternalAdminKey != "" {
		cfg.Credimi.AuthMode = "internal_admin"
	}
	cfg.Temporal.Address = normalized["TEMPORAL_ADDRESS"]
	cfg.Server.APIListen = net.JoinHostPort(normalized["RUNNER_HOST"], normalized["RUNNER_PORT"])
	cfg.Server.DashboardListen = net.JoinHostPort(normalized["DASHBOARD_HOST"], normalized["DASHBOARD_PORT"])
	cfg.Server.DashboardToken = normalized["DASHBOARD_TOKEN"]
	cfg.Observability.Enabled = isTruthyFormValue(normalized["OTEL_ENABLED"])
	cfg.Observability.OTLPEndpoint = normalized["OTEL_EXPORTER_OTLP_ENDPOINT"]
	cfg.Observability.ServiceName = normalized["OTEL_SERVICE_NAME"]
	cfg.Storage.TempDir = normalized["CREDIMI_TEMP_DIR"]
	cfg.Android.RunnerImage = normalized["ANDROID_RUNNER_IMAGE"]
	cfg.Android.PullPolicy = normalized["ANDROID_PULL_POLICY"]
	cfg.Android.Network = normalized["ANDROID_NETWORK"]
	cfg.Android.StateVolume = normalized["ANDROID_STATE_VOLUME"]
	cfg.Android.ToolCacheVolume = normalized["ANDROID_TOOL_CACHE_VOLUME"]
	cfg.Android.SDKVolume = normalized["ANDROID_SDK_VOLUME"]
	cfg.Android.ADBKeysPath = normalized["ANDROID_ADB_KEYS_PATH"]
	switch normalized["CREDIMI_SERVICE_MODE"] {
	case "cloudflare-managed":
		cfg.Exposure.Mode = "named_tunnel"
	case "manual":
		cfg.Exposure.Mode = "manual"
	default:
		cfg.Exposure.Mode = "quick_tunnel"
	}
	cfg.Exposure.PublicURL = normalized["RUNNER_PUBLIC_URL"]
	cfg.Exposure.PublicPort = normalized["RUNNER_PUBLIC_PORT"]
	cfg.Exposure.Domain = normalized["RUNNER_DOMAIN"]
	cfg.Exposure.CaddySite = strings.TrimSpace(normalized["RUNNER_CADDY_SITE"])
	if cfg.Exposure.CaddySite == "" {
		cfg.Exposure.CaddySite = dashboardruntime.DefaultRunnerCaddySite
	}
	cfg.Exposure.CloudflareToken = normalized["CLOUDFLARE_TUNNEL_TOKEN"]
	parsed, err := dashboardruntime.ParseRuntimeConfig(normalized)
	if err != nil && strings.TrimSpace(normalized["CREDIMI_DEVICE_COUNT"]) != "" {
		return runnerconfig.Config{}, err
	}
	for _, device := range parsed.Devices {
		entry := runnerconfig.DeviceConfig{ID: device.ID, Name: device.Name, Description: device.Description, Enabled: device.Enabled}
		switch device.Type {
		case "android_phone":
			entry.Type = runnerconfig.DeviceAndroidPhysical
			physical := &runnerconfig.AndroidPhysicalConfig{Transport: device.Mode}
			if device.Mode == "usb" {
				physical.Serial = device.Serial
			} else if device.Mode == "wifi" {
				physical.WiFiIP, physical.WiFiPort = device.WiFiIP, device.WiFiPort
				if physical.WiFiIP == "" {
					physical.WiFiIP = device.Values["WIFI_IP"]
				}
				if physical.WiFiPort == "" {
					physical.WiFiPort = device.Values["WIFI_PORT"]
				}
				if physical.WiFiPort == "" {
					physical.WiFiPort = dashboardruntime.DefaultWiFiPort
				}
			}
			entry.AndroidPhysical = physical
		case "android_emulator":
			entry.Type = runnerconfig.DeviceAndroidEmulator
			abi := dashboardruntime.DefaultEmulatorABI(stdruntime.GOOS, stdruntime.GOARCH)
			entry.AndroidEmulator = &runnerconfig.AndroidEmulatorConfig{AVDName: device.Values["AVD_NAME"], BaseName: device.Values["BASE_NAME"], GoldenSource: device.Values["GOLDEN_PATH"], ABI: abi, SystemImage: "system-images;android-35;google_apis;" + abi, APILevel: 35, MemoryMB: 2048, Cores: 2}
		case "redroid":
			entry.Type = runnerconfig.DeviceRedroid
			port := device.WiFiPort
			if port == "" {
				port = device.Values["WIFI_PORT"]
			}
			adbPort, _ := strconv.Atoi(port)
			if adbPort == 0 {
				adbPort = 5555
			}
			entry.Redroid = &runnerconfig.RedroidConfig{Host: device.WiFiIP, DataDir: device.Values["REDROID_DATA_DIR"], DataArchive: device.Values["REDROID_DATA_TAR"], Image: "redroid:latest", ADBPort: adbPort}
		case "ios_simulator":
			entry.Type = runnerconfig.DeviceIOSSimulator
			entry.IOSSimulator = &runnerconfig.IOSSimulatorConfig{UDID: device.Values["IOS_UDID"]}
		default:
			return runnerconfig.Config{}, fmt.Errorf("unsupported device type %q", device.Type)
		}
		cfg.Devices = append(cfg.Devices, entry)
	}
	return cfg, nil
}

// GroupedFields returns the registry grouped, preserving order.
func GroupedFields() []struct {
	Name   string
	Fields []Field
} {
	var groups []struct {
		Name   string
		Fields []Field
	}
	idx := map[string]int{}
	for _, f := range Registry {
		if i, ok := idx[f.Group]; ok {
			groups[i].Fields = append(groups[i].Fields, f)
		} else {
			idx[f.Group] = len(groups)
			groups = append(groups, struct {
				Name   string
				Fields []Field
			}{f.Group, []Field{f}})
		}
	}
	return groups
}

// ── helpers ──

func maskSecret(v string) string {
	if len(v) <= 8 {
		return v
	}
	return v[:4] + strings.Repeat("•", minInt(len(v)-4, 28))
}

func quote(v string) string {
	if v == "" {
		return ""
	}
	if strings.ContainsAny(v, " \t#\"'") {
		return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
	}
	return v
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func isTruthyFormValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// titleCase returns s with the first letter uppercased (ASCII only).
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
