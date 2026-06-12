package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		vals   map[string]string
		key    string
		hasErr bool
	}{
		{
			name:   "valid runner ID",
			vals:   map[string]string{"CREDIMI_RUNNER_ID": "myorg/my-runner", "CREDIMI_URL": "https://credimi.io", "CREDIMI_RUNNER_TYPE": "android_phone"},
			hasErr: false,
		},
		{
			name:   "missing required runner ID",
			vals:   map[string]string{"CREDIMI_URL": "https://credimi.io", "CREDIMI_RUNNER_TYPE": "android_phone"},
			key:    "CREDIMI_RUNNER_ID",
			hasErr: true,
		},
		{
			name:   "invalid runner ID format (no slash)",
			vals:   map[string]string{"CREDIMI_RUNNER_ID": "norunner", "CREDIMI_URL": "https://credimi.io", "CREDIMI_RUNNER_TYPE": "android_phone"},
			key:    "CREDIMI_RUNNER_ID",
			hasErr: true,
		},
		{
			name:   "invalid port",
			vals:   map[string]string{"CREDIMI_RUNNER_ID": "org/name", "CREDIMI_URL": "https://credimi.io", "CREDIMI_RUNNER_TYPE": "android_phone", "RUNNER_PORT": "abc"},
			key:    "RUNNER_PORT",
			hasErr: true,
		},
		{
			name:   "valid port",
			vals:   map[string]string{"CREDIMI_RUNNER_ID": "org/name", "CREDIMI_URL": "https://credimi.io", "CREDIMI_RUNNER_TYPE": "android_phone", "RUNNER_PORT": "8050"},
			hasErr: false,
		},
		{
			name:   "optional staging URL ok empty",
			vals:   map[string]string{"CREDIMI_RUNNER_ID": "org/name", "CREDIMI_URL": "https://credimi.io", "CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_STAGING_URL": ""},
			hasErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(tt.vals)
			if tt.hasErr {
				if len(errs) == 0 {
					t.Fatal("expected validation errors, got none")
				}
				if tt.key != "" {
					if _, ok := errs[tt.key]; !ok {
						t.Errorf("expected error for key %q, got: %v", tt.key, errs)
					}
				}
			} else {
				if len(errs) > 0 {
					t.Errorf("unexpected validation errors: %v", errs)
				}
			}
		})
	}
}

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		in  string
		out string
	}{
		{"short", "short"},       // 5 chars, <= 8
		{"12345678", "12345678"}, // exactly 8
		{"cmu_live_secret_key_12345", "cmu_" + strings.Repeat("•", 21)}, // 25 chars total = 4 prefix + 21 dots
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
			"CREDIMI_USER_API_KEY":       "cmu_live_secret123",
			"CREDIMI_INTERNAL_ADMIN_KEY": "",
			"TEMPORAL_ADDRESS":           "temporal.credimi.io:7233",
		},
	}

	masked := cfg.RawEnv(true)
	if contains(masked, "cmu_live_secret123") {
		t.Error("masked env should not contain secret plaintext")
	}

	clear := cfg.RawEnv(false)
	if !contains(clear, "cmu_live_secret123") {
		t.Error("clear env should contain secret plaintext")
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

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
