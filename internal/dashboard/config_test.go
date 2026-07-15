package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
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
			name:   "redroid requires Wi-Fi IP",
			vals:   map[string]string{"CREDIMI_RUNNER_ID": "org/name", "CREDIMI_URL": "https://credimi.io", "CREDIMI_RUNNER_TYPE": "redroid"},
			key:    "CREDIMI_RUNNER_WIFI_IP",
			hasErr: true,
		},
		{
			name:   "redroid accepts Wi-Fi IP",
			vals:   map[string]string{"CREDIMI_RUNNER_ID": "org/name", "CREDIMI_URL": "https://credimi.io", "CREDIMI_RUNNER_TYPE": "redroid", "CREDIMI_RUNNER_WIFI_IP": "192.168.1.30"},
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

func TestNormalizedConfigValuesPreservesSubmittedRedroidFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	current := map[string]string{
		"CREDIMI_RUNNER_TYPE":   "android_phone",
		"CREDIMI_RUNNER_SERIAL": "device-1",
	}
	incoming := map[string]string{
		"CREDIMI_RUNNER_TYPE":    "redroid",
		"CREDIMI_RUNNER_WIFI_IP": "192.168.1.30",
		"AVDCTL_SSH_TARGET":      "credimi@redroid-host",
	}

	values, err := normalizedConfigValues(current, incoming, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_SERIAL"] != "192.168.1.30:5555" || values["CREDIMI_RUNNER_WIFI_PORT"] != "5555" {
		t.Fatalf("Redroid endpoint = %#v", values)
	}
	if values["AVDCTL_SSH_TARGET"] != "credimi@redroid-host" || values["AVDCTL_SSH_KNOWN_HOSTS_PATH"] != filepath.Join(home, ".ssh", "known_hosts") {
		t.Fatalf("Redroid SSH = %#v", values)
	}
}

func TestConfigApplyWritesRedroidEndpointAndSSHDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	errs, err := cfg.Apply(map[string]string{
		"CREDIMI_RUNNER_ID":      "acme/redroid",
		"CREDIMI_RUNNER_TYPE":    "redroid",
		"CREDIMI_RUNNER_WIFI_IP": "192.168.1.30",
		"AVDCTL_SSH_TARGET":      "credimi@redroid-host",
	})
	if err != nil {
		t.Fatalf("Apply failed: %v (errors: %v)", err, errs)
	}
	content, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"CREDIMI_RUNNER_DEVICE_MODE=no_device",
		"CREDIMI_RUNNER_SERIAL=192.168.1.30:5555",
		"CREDIMI_RUNNER_WIFI_IP=192.168.1.30",
		"CREDIMI_RUNNER_WIFI_PORT=5555",
		"AVDCTL_SSH_KNOWN_HOSTS_PATH=" + filepath.Join(home, ".ssh", "known_hosts"),
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf(".env missing %q:\n%s", want, content)
		}
	}
}

func TestConfigApplyResetsTypeDerivedFieldsOnRunnerTypeChange(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	cfg.values["RUNNER_IMAGE"] = defaultPhoneImage
	cfg.values["BASE_NAME"] = ""
	cfg.values["HOST_AVD_HOME_PATH"] = ""
	cfg.values["HOST_AVD_GOLDEN_PATH"] = ""
	cfg.values["GOLDEN_PATH"] = ""

	errs, err := cfg.Apply(map[string]string{
		"CREDIMI_URL":         "https://credimi.io",
		"CREDIMI_RUNNER_ID":   "myorg/runner1",
		"CREDIMI_RUNNER_TYPE": "android_emulator",
	})
	if err != nil {
		t.Fatalf("Apply failed: %v (errors: %v)", err, errs)
	}
	if cfg.Get("RUNNER_IMAGE") != defaultEmulatorImage {
		t.Fatalf("RUNNER_IMAGE = %q", cfg.Get("RUNNER_IMAGE"))
	}
	if cfg.Get("BASE_NAME") != dashboardruntime.DefaultBaseName {
		t.Fatalf("BASE_NAME = %q", cfg.Get("BASE_NAME"))
	}
	if cfg.Get("GOLDEN_PATH") != dashboardruntime.DefaultGoldenPath {
		t.Fatalf("GOLDEN_PATH = %q", cfg.Get("GOLDEN_PATH"))
	}
	if cfg.Get("HOST_AVD_HOME_PATH") == "" || cfg.Get("HOST_AVD_GOLDEN_PATH") == "" {
		t.Fatalf("expected host AVD defaults, got home=%q golden=%q", cfg.Get("HOST_AVD_HOME_PATH"), cfg.Get("HOST_AVD_GOLDEN_PATH"))
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

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
