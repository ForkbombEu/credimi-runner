package dashboard

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// Config: typed view over ~/.config/credimi/runner/.env
//
// The field registry drives both the rendered form and the .env round-trip, so
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
	{Key: "CREDIMI_RUNNER_BACKEND", Label: "Runner backend", Group: "Network", Type: TypeSelect,
		Options: []string{"container", "host"}, Hint: "container runs the published image; host runs the downloaded binary and uses compose only for edge services."},
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
}

var fieldByKey = func() map[string]Field {
	m := make(map[string]Field, len(Registry))
	for _, f := range Registry {
		m[f.Key] = f
	}
	return m
}()

// Defaults applied when the .env is missing a key.
var Defaults = map[string]string(dashboardruntime.DefaultValues())

// Config is a concurrency-safe key/value store backed by the .env file.
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
	c := &Config{path: filepath.Join(dir, ".env"), values: map[string]string{}}
	for k, v := range Defaults {
		c.values[k] = v
	}
	f, err := os.Open(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil // first run — defaults only
		}
		return nil, err
	}
	defer f.Close()
	known := fieldByKey
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = unquote(strings.TrimSpace(v))
		if _, isKnown := known[k]; isKnown || strings.HasPrefix(k, "CREDIMI_DEVICE_") {
			c.values[k] = v
		} else {
			c.rawTail = append(c.rawTail, k+"="+v) // keep unknown keys
		}
	}
	return c, sc.Err()
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
		case "RUNNER_PORT":
			if !regexp.MustCompile(`^\d{1,5}$`).MatchString(v) {
				errs[f.Key] = "Must be a port number."
			}
		}
	}
	return errs
}

// Apply validates and persists incoming form values, then writes .env atomically.
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

// write serializes the config to .env atomically with 0600 perms.
func (c *Config) write() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var b strings.Builder
	b.WriteString("# ~/.config/credimi/runner/.env\n# Managed by the credimi-runner dashboard\n\n")
	group := ""
	for _, f := range Registry {
		if f.Group != group {
			group = f.Group
			fmt.Fprintf(&b, "# ── %s ──\n", group)
		}
		fmt.Fprintf(&b, "%s=%s\n", f.Key, quote(c.values[f.Key]))
	}
	writeIndexedDeviceBlocks(&b, c.values)
	if len(c.rawTail) > 0 {
		b.WriteString("\n# ── Preserved ──\n")
		for _, l := range c.rawTail {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}

	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path) // atomic on same filesystem
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

// RawEnv renders the .env text. When mask is true, secrets are partially hidden.
func (c *Config) RawEnv(mask bool) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var b strings.Builder
	b.WriteString("# ~/.config/credimi/runner/.env\n# Managed by the credimi-runner dashboard\n\n")
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

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
		v = strings.ReplaceAll(v, `\"`, `"`)
	}
	return v
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func boolPtr(value bool) *bool {
	return &value
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
