package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		in  string
		out string
	}{
		{"short", "short"},       // 5 chars, <= 8
		{"12345678", "12345678"}, // exactly 8
		{"test_secret_key_12345", "test" + strings.Repeat("•", 17)}, // 21 chars total = 4 prefix + 17 dots
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := maskSecret(tt.in)
			if got != tt.out {
				t.Errorf("maskSecret(%q) = %q, want %q", tt.in, got, tt.out)
			}
		})
	}
}

func TestQuoteAndUnquote(t *testing.T) {
	tests := []struct {
		raw     string
		quoted  string
		unquote string
	}{
		{"simple", "simple", "simple"},
		{"has space", `"has space"`, "has space"},
		{`has " quote`, `"has \" quote"`, `has " quote`},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := quote(tt.raw)
			if got != tt.quoted {
				t.Errorf("quote(%q) = %q, want %q", tt.raw, got, tt.quoted)
			}
			un := unquote(got)
			if un != tt.unquote {
				t.Errorf("unquote(%q) = %q, want %q", got, un, tt.unquote)
			}
		})
	}
}

func TestBoolStr(t *testing.T) {
	if s := boolStr(true); s != "true" {
		t.Errorf("boolStr(true) = %q", s)
	}
	if s := boolStr(false); s != "false" {
		t.Errorf("boolStr(false) = %q", s)
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Get("CREDIMI_URL") != "https://credimi.io" {
		t.Errorf("default CREDIMI_URL = %q", cfg.Get("CREDIMI_URL"))
	}
	if cfg.Get("TEMPORAL_ADDRESS") != "temporal.credimi.io:7233" {
		t.Errorf("default TEMPORAL_ADDRESS = %q", cfg.Get("TEMPORAL_ADDRESS"))
	}
}

func TestConfig_ApplyAndWrite(t *testing.T) {
	dir := t.TempDir()
	// Set the config dir override so ConfigDir() returns our temp dir.
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", dir)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Override the path to write to our temp dir.
	cfg.path = filepath.Join(dir, ".env")

	incoming := map[string]string{
		"CREDIMI_URL":         "https://custom.credimi.io",
		"CREDIMI_RUNNER_ID":   "myorg/runner1",
		"CREDIMI_RUNNER_TYPE": "android_emulator",
	}

	errs, err := cfg.Apply(incoming)
	if err != nil {
		t.Fatalf("Apply failed: %v (errors: %v)", err, errs)
	}

	// Verify values were updated.
	if cfg.Get("CREDIMI_URL") != "https://custom.credimi.io" {
		t.Errorf("CREDIMI_URL not updated: %q", cfg.Get("CREDIMI_URL"))
	}
	if cfg.Get("CREDIMI_RUNNER_ID") != "myorg/runner1" {
		t.Errorf("CREDIMI_RUNNER_ID not updated: %q", cfg.Get("CREDIMI_RUNNER_ID"))
	}

	// Verify file was written.
	fi, err := os.Stat(cfg.path)
	if err != nil {
		t.Fatalf("env file not written: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("env file perms = %o, want 0600", fi.Mode().Perm())
	}

	// Verify content.
	data, err := os.ReadFile(cfg.path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !contains(content, "CREDIMI_URL=https://custom.credimi.io") {
		t.Errorf("expected CREDIMI_URL in .env, got:\n%s", content)
	}
}

func TestConfigApplyPreservesAbsentBooleanFields(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.values["OTEL_ENABLED"] = "true"

	errs, err := cfg.Apply(map[string]string{
		"CREDIMI_URL":       "https://credimi.io",
		"CREDIMI_RUNNER_ID": "myorg/runner1",
	})
	if err != nil {
		t.Fatalf("Apply failed: %v (errors: %v)", err, errs)
	}
	if cfg.Get("OTEL_ENABLED") != "true" {
		t.Fatalf("absent OTEL_ENABLED should be preserved, got %q", cfg.Get("OTEL_ENABLED"))
	}

	errs, err = cfg.Apply(map[string]string{
		"CREDIMI_URL":       "https://credimi.io",
		"CREDIMI_RUNNER_ID": "myorg/runner1",
		"OTEL_ENABLED":      "false",
	})
	if err != nil {
		t.Fatalf("Apply failed: %v (errors: %v)", err, errs)
	}
	if cfg.Get("OTEL_ENABLED") != "false" {
		t.Fatalf("explicit false OTEL_ENABLED should be saved, got %q", cfg.Get("OTEL_ENABLED"))
	}
}

func TestConfig_AuthMode(t *testing.T) {
	cfg := &Config{values: map[string]string{}}
	if cfg.AuthMode() != "user" {
		t.Error("expected user mode by default")
	}
	cfg.values["CREDIMI_INTERNAL_ADMIN_KEY"] = "secret"
	if cfg.AuthMode() != "admin" {
		t.Error("expected admin mode when admin key set")
	}
}

func TestRawEnv(t *testing.T) {
	cfg := &Config{
		values: map[string]string{
			"CREDIMI_URL":                "https://credimi.io",
			"CREDIMI_USER_API_KEY":       "test-secret-value-123",
			"CREDIMI_INTERNAL_ADMIN_KEY": "",
			"TEMPORAL_ADDRESS":           "temporal.credimi.io:7233",
		},
	}

	masked := cfg.RawEnv(true)
	if contains(masked, "test-secret-value-123") {
		t.Error("masked env should not contain secret plaintext")
	}

	clear := cfg.RawEnv(false)
	if !contains(clear, "test-secret-value-123") {
		t.Error("clear env should contain secret plaintext")
	}
}

func TestConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", dir)
	if got := ConfigDir(); got != dir {
		t.Fatalf("ConfigDir override = %q, want %q", got, dir)
	}
}

func TestLoadConfig_ParsesEnvFile(t *testing.T) {
	dir := t.TempDir()
	env := strings.Join([]string{
		"# comment",
		"CREDIMI_URL=\"https://custom.example\"",
		"CREDIMI_RUNNER_ID=org/runner",
		"OTEL_ENABLED=false",
		"UNKNOWN_KEY=preserved",
		"ignored-without-equals",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Get("CREDIMI_URL") != "https://custom.example" {
		t.Fatalf("CREDIMI_URL = %q", cfg.Get("CREDIMI_URL"))
	}
	if cfg.Bool("OTEL_ENABLED") {
		t.Fatal("OTEL_ENABLED should parse as false")
	}
	if len(cfg.rawTail) != 1 || cfg.rawTail[0] != "UNKNOWN_KEY=preserved" {
		t.Fatalf("rawTail = %#v", cfg.rawTail)
	}
}

func TestGroupedFieldsAndSortedKeys(t *testing.T) {
	groups := GroupedFields()
	if len(groups) == 0 {
		t.Fatal("expected grouped fields")
	}
	if groups[0].Name != Registry[0].Group || groups[0].Fields[0].Key != Registry[0].Key {
		t.Fatalf("first group = %#v", groups[0])
	}

	keys := sortedKeys(map[string]string{"b": "2", "a": "1"})
	if strings.Join(keys, ",") != "a,b" {
		t.Fatalf("sortedKeys = %v", keys)
	}
	if got := minInt(9, 3); got != 3 {
		t.Fatalf("minInt = %d", got)
	}
}

func TestTitleCase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"overview", "Overview"},
		{"devices", "Devices"},
		{"", ""},
		{"a", "A"},
	}
	for _, tt := range tests {
		got := titleCase(tt.in)
		if got != tt.want {
			t.Errorf("titleCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestConfigApplyPersistsBooleanFormValues(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	incoming := map[string]string{
		"CREDIMI_URL":              "https://credimi.example",
		"CREDIMI_RUNNER_ID":        "acme/runner",
		"CREDIMI_RUNNER_BACKEND":   "container",
		"CREDIMI_SERVICE_MODE":     "manual",
		"CREDIMI_RUNNER_PUBLISHED": "on",
		"CREDIMI_USER_API_KEY":     "test-key",
	}
	if errs, err := cfg.Apply(incoming); err != nil {
		t.Fatalf("Apply errors=%v err=%v", errs, err)
	}
	if !cfg.Bool("CREDIMI_RUNNER_PUBLISHED") || cfg.AuthMode() != "user" {
		t.Fatalf("config booleans/auth = published:%t auth:%q", cfg.Bool("CREDIMI_RUNNER_PUBLISHED"), cfg.AuthMode())
	}
	raw, err := os.ReadFile(cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"CREDIMI_RUNNER_ID=acme/runner",
		"CREDIMI_RUNNER_PUBLISHED=true",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("persisted config missing %q:\n%s", want, raw)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
