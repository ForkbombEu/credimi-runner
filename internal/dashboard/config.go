package dashboard

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// Config is the dashboard's compatibility view over the typed TOML runner
// configuration. The registry contains runner-global editable fields; the
// indexed device inventory is converted by the runtime package.
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
	{Key: "RUNNER_PORT", Label: "Internal runner API port", Group: "Network", Type: TypeText, Hint: "Internal runtime listener only; this is not the public URL port. Default is 8050."},
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
	{Key: "ADB_SCREEN_RECORD_SIZE", Label: "ADB screen recording size", Group: "Advanced", Type: TypeText, Hint: "Optional, for example 1280x720. Leave empty to use the device default."},
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
	mu     sync.RWMutex
	path   string
	values map[string]string
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
	c.values = map[string]string(dashboardruntime.ValuesFromTypedConfig(cfg))
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
		case "CREDIMI_URL", "OTEL_EXPORTER_OTLP_ENDPOINT":
			if u, err := url.Parse(v); err != nil || u.Host == "" || u.Scheme == "" {
				errs[f.Key] = "Not a valid URL."
			}
		case "RUNNER_PUBLIC_URL":
			if u, err := url.Parse(v); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				errs[f.Key] = "Must be an absolute http:// or https:// URL."
			}
		case "RUNNER_DOMAIN":
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
	if err := c.writeValues(next); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.values = next
	c.mu.Unlock()
	return nil, nil
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

// writeValues converts the candidate compatibility values to typed TOML atomically.
func (c *Config) writeValues(values map[string]string) error {
	cfg, err := dashboardruntime.TypedConfigFromValues(dashboardruntime.Values(values))
	if err != nil {
		return err
	}
	return runnerconfig.WriteFile(c.path, cfg)
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
	if v == "" {
		return ""
	}
	if len(v) <= 8 {
		return strings.Repeat("•", len([]rune(v)))
	}
	return v[:4] + strings.Repeat("•", minInt(len(v)-4, 28))
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
