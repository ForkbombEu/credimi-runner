package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Runner:        RunnerConfig{ID: "acme/runner", Name: "Runner", Organization: "acme"},
		Credimi:       CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"},
		Temporal:      TemporalConfig{Address: "temporal.example:7233"},
		Server:        ServerConfig{APIListen: "127.0.0.1:8050", DashboardListen: "127.0.0.1:8051", ReadHeaderTimeout: Duration(1), ShutdownTimeout: Duration(1)},
		Exposure:      ExposureConfig{Mode: "quick_tunnel"},
		Storage:       StorageConfig{StateDir: "/tmp/credimi-state", ArtifactRetention: Duration(1)},
		Android:       AndroidConfig{RunnerImage: "runner:latest", PullPolicy: "never", Network: "net", StateVolume: "data", ToolCacheVolume: "tools", SDKVolume: "sdk"},
	}
}

func physical(id, name, transport, serial string) DeviceConfig {
	physical := &AndroidPhysicalConfig{Transport: transport}
	if transport == "wifi" {
		physical.WiFiIP, physical.WiFiPort = strings.TrimSuffix(serial, ":5555"), "5555"
	} else {
		physical.Serial = serial
	}
	return DeviceConfig{ID: id, Name: name, Type: DeviceAndroidPhysical, Enabled: true, AndroidPhysical: physical}
}

func TestValidateForPlatformAcceptsSupportedInventory(t *testing.T) {
	linux := validConfig()
	linux.Devices = []DeviceConfig{physical("acme/runner/pixel", "Pixel", "wifi", "wifi:5555"), {ID: "acme/runner/emulator", Name: "Emulator", Type: DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &AndroidEmulatorConfig{APILevel: 35, ABI: "x86_64", SystemImage: "google", BaseName: "credimi", GoldenSource: "/golden", MemoryMB: 4096, Cores: 4}}, {ID: "acme/runner/redroid", Name: "Redroid", Type: DeviceRedroid, Redroid: &RedroidConfig{Host: "redroid", Image: "redroid:latest", DataDir: "/data", DataArchive: "/data.tar", ADBPort: 5555}}}
	if err := ValidateForPlatform(linux, "linux"); err != nil {
		t.Fatal(err)
	}
	mac := validConfig()
	mac.Devices = []DeviceConfig{physical("acme/runner/pixel", "Pixel", "wifi", "wifi:5555"), {ID: "acme/runner/ios", Name: "iOS", Type: DeviceIOSSimulator, IOSSimulator: &IOSSimulatorConfig{UDID: "udid"}}}
	if err := ValidateForPlatform(mac, "darwin"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateForPlatform(validConfig(), "linux"); err != nil {
		t.Fatalf("zero devices: %v", err)
	}
}

func TestValidatePhysicalAddressModes(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*Config)
		valid bool
	}{
		{name: "usb", setup: func(c *Config) {
			c.Devices = []DeviceConfig{physical("acme/runner/usb", "USB", "usb", "SERIAL_ONE")}
		}, valid: true},
		{name: "wifi", setup: func(c *Config) {
			c.Devices = []DeviceConfig{{ID: "acme/runner/wifi", Name: "Wi-Fi", Type: DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.168.1.20", WiFiPort: "5555"}}}
		}, valid: true},
		{name: "no device", setup: func(c *Config) {
			c.Devices = []DeviceConfig{{ID: "acme/runner/idle", Name: "Idle", Type: DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &AndroidPhysicalConfig{Transport: "no_device"}}}
		}, valid: true},
		{name: "wifi serial is contradictory", setup: func(c *Config) {
			c.Devices = []DeviceConfig{{ID: "acme/runner/wifi", Name: "Wi-Fi", Type: DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &AndroidPhysicalConfig{Transport: "wifi", Serial: "192.168.1.20:5555", WiFiIP: "192.168.1.20", WiFiPort: "5555"}}}
		}},
		{name: "no device address is contradictory", setup: func(c *Config) {
			c.Devices = []DeviceConfig{{ID: "acme/runner/idle", Name: "Idle", Type: DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &AndroidPhysicalConfig{Transport: "no_device", WiFiIP: "192.168.1.20"}}}
		}},
		{name: "redroid", setup: func(c *Config) {
			c.Devices = []DeviceConfig{{ID: "acme/runner/redroid", Name: "Redroid", Type: DeviceRedroid, Enabled: true, Redroid: &RedroidConfig{Host: "192.168.1.30", Image: "redroid:latest", DataDir: "/data", DataArchive: "/data.tar", ADBPort: 5555}}}
		}, valid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.setup(&cfg)
			err := ValidateForPlatform(cfg, "linux")
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && err == nil {
				t.Fatal("contradictory address accepted")
			}
		})
	}
}

func TestWiFiConfigRoundTripDoesNotPersistDerivedSerial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := validConfig()
	cfg.Devices = []DeviceConfig{{ID: "acme/runner/wifi", Name: "Wi-Fi", Type: DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "2001:db8::20", WiFiPort: "5555"}}}
	if err := WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	physical := loaded.Devices[0].AndroidPhysical
	if physical.Serial != "" || physical.WiFiIP != "2001:db8::20" || physical.WiFiPort != "5555" {
		t.Fatalf("loaded Wi-Fi config = %#v", physical)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "serial =") {
		t.Fatalf("derived serial was persisted:\n%s", data)
	}
}

func TestConfiguredOwnerReadsLauncherIdentityWithoutWeakeningMode(t *testing.T) {
	t.Setenv("CREDIMI_CONFIG_OWNER_UID", "1001")
	t.Setenv("CREDIMI_CONFIG_OWNER_GID", "1002")
	uid, gid, ok := configuredOwner()
	if !ok || uid != 1001 || gid != 1002 {
		t.Fatalf("configured owner = %d:%d, ok=%v", uid, gid, ok)
	}
	t.Setenv("CREDIMI_CONFIG_OWNER_UID", "not-a-user")
	if _, _, ok := configuredOwner(); ok {
		t.Fatal("invalid launcher owner was accepted")
	}
}

func TestValidateForPlatformRejectsInvalidCombinations(t *testing.T) {
	cases := []struct {
		name, goos, want string
		mutate           func(*Config)
	}{
		{"schema", "linux", "unsupported schema", func(c *Config) { c.SchemaVersion = 2 }},
		{"runner ID", "linux", "canonical", func(c *Config) { c.Runner.ID = "/bad" }},
		{"runner name", "linux", "runner.name is required", func(c *Config) { c.Runner.Name = "" }},
		{"runner organization", "linux", "runner.organization is required", func(c *Config) { c.Runner.Organization = "" }},
		{"wrong organization", "linux", "runner.organization", func(c *Config) { c.Runner.Organization = "other" }},
		{"auth", "linux", "exactly user_api_key", func(c *Config) { c.Credimi.UserAPIKey = "" }},
		{"both auth keys", "linux", "exactly user_api_key", func(c *Config) { c.Credimi.InternalAdminKey = "admin" }},
		{"admin auth", "linux", "exactly internal_admin_key", func(c *Config) { c.Credimi.AuthMode = "internal_admin" }},
		{"bad credimi URL", "linux", "absolute URL", func(c *Config) { c.Credimi.URL = "bad" }},
		{"temporal address", "linux", "temporal.address is required", func(c *Config) { c.Temporal.Address = "" }},
		{"bad listen", "linux", "server.api_listen", func(c *Config) { c.Server.APIListen = "bad" }},
		{"bad dashboard listen", "linux", "server.dashboard_listen", func(c *Config) { c.Server.DashboardListen = "bad" }},
		{"server duration", "linux", "server durations", func(c *Config) { c.Server.ShutdownTimeout = 0 }},
		{"retention", "linux", "artifact_retention", func(c *Config) { c.Storage.ArtifactRetention = 0 }},
		{"state directory", "linux", "storage.state_dir", func(c *Config) { c.Storage.StateDir = "relative" }},
		{"manual URL", "linux", "manual exposure", func(c *Config) { c.Exposure.Mode = "manual" }},
		{"named token", "linux", "named_tunnel", func(c *Config) { c.Exposure.Mode = "named_tunnel" }},
		{"quick token", "linux", "quick_tunnel", func(c *Config) { c.Exposure.CloudflareToken = "token" }},
		{"bad exposure", "linux", "exposure.mode", func(c *Config) { c.Exposure.Mode = "other" }},
		{"bad policy", "linux", "android.pull_policy", func(c *Config) { c.Android.PullPolicy = "later" }},
		{"missing runner image", "linux", "android.runner_image", func(c *Config) { c.Android.RunnerImage = "" }},
		{"missing network", "linux", "android.network", func(c *Config) { c.Android.Network = "" }},
		{"missing state volume", "linux", "android.state_volume", func(c *Config) { c.Android.StateVolume = "" }},
		{"missing tool cache volume", "linux", "android.tool_cache_volume", func(c *Config) { c.Android.ToolCacheVolume = "" }},
		{"missing sdk volume", "linux", "android.sdk_volume", func(c *Config) { c.Android.SDKVolume = "" }},
		{"linux ios", "linux", "require macOS", func(c *Config) {
			c.Devices = []DeviceConfig{{ID: "acme/runner/i", Name: "I", Type: DeviceIOSSimulator, IOSSimulator: &IOSSimulatorConfig{UDID: "id"}}}
		}},
		{"duplicate serial", "linux", "duplicate devices[1].redroid", func(c *Config) {
			c.Devices = []DeviceConfig{physical("acme/runner/p", "P", "wifi", "same"), {ID: "acme/runner/r", Name: "R", Type: DeviceRedroid, Redroid: &RedroidConfig{Host: "same", Image: "r", DataDir: "/d", DataArchive: "/a", ADBPort: 5555}}}
		}},
		{"wrong subtype", "linux", "exactly one type-specific", func(c *Config) {
			c.Devices = []DeviceConfig{{ID: "acme/runner/p", Name: "P", Type: DeviceAndroidPhysical}}
		}},
		{"bad transport", "linux", "transport", func(c *Config) { c.Devices = []DeviceConfig{physical("acme/runner/p", "P", "bluetooth", "x")} }},
		{"bad redroid port", "linux", "adb_port", func(c *Config) {
			c.Devices = []DeviceConfig{{ID: "acme/runner/r", Name: "R", Type: DeviceRedroid, Redroid: &RedroidConfig{Host: "r", Image: "r", DataDir: "/d", DataArchive: "/a"}}}
		}},
		{"unsupported emulator platform", "windows", "require Linux or macOS", func(c *Config) {
			c.Devices = []DeviceConfig{{ID: "acme/runner/emulator", Name: "E", Type: DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &AndroidEmulatorConfig{APILevel: 35, ABI: "x86_64", SystemImage: "google", BaseName: "credimi", GoldenSource: "/golden", MemoryMB: 2048, Cores: 2}}}
		}},
		{"duplicate emulator", "linux", "only one Android emulator", func(c *Config) {
			emulator := DeviceConfig{ID: "acme/runner/emulator", Name: "E", Type: DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &AndroidEmulatorConfig{APILevel: 35, ABI: "x86_64", SystemImage: "google", BaseName: "credimi", GoldenSource: "/golden", MemoryMB: 2048, Cores: 2}}
			second := emulator
			second.ID, second.Name, second.AndroidEmulator = "acme/runner/emulator-2", "E2", &AndroidEmulatorConfig{APILevel: 35, ABI: "x86_64", SystemImage: "google", BaseName: "credimi", GoldenSource: "/golden", MemoryMB: 2048, Cores: 2}
			c.Devices = []DeviceConfig{emulator, second}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			err := ValidateForPlatform(cfg, tc.goos)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadWriteAndRedactConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := validConfig()
	cfg.Devices = []DeviceConfig{physical("acme/runner/pixel", "Pixel", "wifi", "wifi:5555")}
	if err := WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Runner.ID != cfg.Runner.ID || len(loaded.Devices) != 1 || loaded.Server.ReadHeaderTimeout.Duration() == 0 {
		t.Fatalf("loaded = %#v", loaded)
	}
	if loaded.Redacted().Credimi.UserAPIKey != "[redacted]" {
		t.Fatal("secret was not redacted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "group- or world-readable") {
		t.Fatalf("permission error = %v", err)
	}
	if err := os.WriteFile(path, []byte("schema_version = 1\nunknown = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("unknown key error = %v", err)
	}
}

func TestExposurePublicPortRoundTripsTypedTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := validConfig()
	cfg.Exposure = ExposureConfig{Mode: "manual", PublicURL: "https://runner.example", PublicPort: "8050"}
	if err := WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Exposure.PublicPort != "8050" {
		t.Fatalf("public port = %q", loaded.Exposure.PublicPort)
	}
}

func TestManagedExposureRoundTripsTypedTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := validConfig()
	cfg.Exposure = ExposureConfig{Mode: "named_tunnel", Domain: "runner.example.com", CaddySite: ":80", CloudflareToken: "secret"}
	if err := WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Exposure.Domain != "runner.example.com" || loaded.Exposure.CaddySite != ":80" || loaded.Exposure.CloudflareToken != "secret" {
		t.Fatalf("managed exposure = %#v", loaded.Exposure)
	}
}

func TestEmulatorBaseNameIsTheOnlyTypedAVDIdentifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := validConfig()
	cfg.Devices = []DeviceConfig{{ID: "acme/runner/emulator", Name: "Emulator", Type: DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &AndroidEmulatorConfig{BaseName: "credimi", APILevel: 35, ABI: "x86_64", SystemImage: "system-images;android-35;google_apis;x86_64", GoldenSource: "/golden", MemoryMB: 2048, Cores: 2}}}
	if err := WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Devices[0].AndroidEmulator.BaseName != "credimi" {
		t.Fatalf("base name = %q", loaded.Devices[0].AndroidEmulator.BaseName)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "avd_name") {
		t.Fatalf("typed TOML contains derived avd_name:\n%s", data)
	}
}

func TestDefaultsPathsAndLoadResolution(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	state, err := DefaultStateDir()
	if err != nil || !strings.HasSuffix(state, "credimi-runner") {
		t.Fatalf("state=%q err=%v", state, err)
	}
	path, err := ResolvePath("relative.toml")
	if err != nil || !filepath.IsAbs(path) {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if _, err := ResolvePath(""); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	if err := ApplyDefaults(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != 0 {
		t.Fatal("schema_version must be explicitly configured")
	}
	if cfg.Android.RunnerImage == "" || cfg.Storage.StateDir == "" || cfg.Server.APIListen == "" {
		t.Fatalf("defaults=%#v", cfg)
	}
	if _, _, err := Load(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("missing config should fail")
	}
	var duration Duration
	if err := duration.UnmarshalText([]byte("broken")); err == nil {
		t.Fatal("malformed duration should fail")
	}
}

func TestBootstrapProvidesFirstRunServerDefaults(t *testing.T) {
	cfg := Bootstrap()
	if cfg.SchemaVersion != SchemaVersion || cfg.Server.APIListen != "0.0.0.0:8050" || cfg.Server.DashboardListen != "127.0.0.1:8051" || cfg.Exposure.Mode != "quick_tunnel" || !cfg.Server.OpenBrowser {
		t.Fatalf("bootstrap config = %#v", cfg)
	}
}

func TestDurationRoundTripsText(t *testing.T) {
	var duration Duration
	if err := duration.UnmarshalText([]byte("1500ms")); err != nil || duration.Duration() != 1500*time.Millisecond {
		t.Fatalf("unmarshal duration=%v err=%v", duration.Duration(), err)
	}
	encoded, err := duration.MarshalText()
	if err != nil || string(encoded) != "1.5s" {
		t.Fatalf("marshal duration=%q err=%v", encoded, err)
	}
}

func TestDefaultPathsUseXDGAndWriteCreatesPrivateParent(t *testing.T) {
	xdgConfig := t.TempDir()
	xdgState := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_STATE_HOME", xdgState)
	configPath, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdgConfig, "credimi-runner", "config.toml"); configPath != want {
		t.Fatalf("DefaultPath = %q, want %q", configPath, want)
	}
	statePath, err := DefaultStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdgState, "credimi-runner"); statePath != want {
		t.Fatalf("DefaultStateDir = %q, want %q", statePath, want)
	}

	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := WriteFile(path, validConfig()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("config directory mode = %o", info.Mode().Perm())
	}
}

func TestDefaultStateDirFallsBackToHomeStateDirectory(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	statePath, err := DefaultStateDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "state", "credimi-runner"); statePath != want {
		t.Fatalf("DefaultStateDir = %q, want %q", statePath, want)
	}
}

func TestValidateForPlatformRejectsDuplicateDeviceIdentityAndLimits(t *testing.T) {
	tests := []struct {
		name, want string
		devices    []DeviceConfig
	}{
		{
			name: "duplicate ID",
			want: "duplicate devices[1].id",
			devices: []DeviceConfig{
				physical("acme/runner/a", "A", "wifi", "one"),
				physical("acme/runner/a", "B", "wifi", "two"),
			},
		},
		{
			name: "duplicate name",
			want: "duplicate devices[1].name",
			devices: []DeviceConfig{
				physical("acme/runner/a", "A", "wifi", "one"),
				physical("acme/runner/b", "A", "wifi", "two"),
			},
		},
		{
			name: "duplicate emulator",
			want: "only one Android emulator",
			devices: []DeviceConfig{
				{ID: "acme/runner/a", Name: "A", Type: DeviceAndroidEmulator, AndroidEmulator: &AndroidEmulatorConfig{APILevel: 1, ABI: "x", SystemImage: "x", BaseName: "a", GoldenSource: "/a", MemoryMB: 1, Cores: 1}},
				{ID: "acme/runner/b", Name: "B", Type: DeviceAndroidEmulator, AndroidEmulator: &AndroidEmulatorConfig{APILevel: 1, ABI: "x", SystemImage: "x", BaseName: "b", GoldenSource: "/b", MemoryMB: 1, Cores: 1}},
			},
		},
		{
			name: "two simulators",
			want: "only one iOS Simulator",
			devices: []DeviceConfig{
				{ID: "acme/runner/a", Name: "A", Type: DeviceIOSSimulator, IOSSimulator: &IOSSimulatorConfig{UDID: "one"}},
				{ID: "acme/runner/b", Name: "B", Type: DeviceIOSSimulator, IOSSimulator: &IOSSimulatorConfig{UDID: "two"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Devices = test.devices
			goos := "linux"
			if test.name == "two simulators" {
				goos = "darwin"
			}
			err := ValidateForPlatform(cfg, goos)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadFileRejectsDirectoriesAndMalformedTOML(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadFile(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error = %v", err)
	}
	path := filepath.Join(dir, "invalid.toml")
	if err := os.WriteFile(path, []byte("schema_version = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "decode TOML") {
		t.Fatalf("malformed TOML error = %v", err)
	}
}

func TestLoadExplicitPathAndRedactsEverySecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := validConfig()
	cfg.Credimi.InternalAdminKey = ""
	cfg.Server.DashboardToken = "dashboard-secret"
	cfg.Exposure = ExposureConfig{Mode: "named_tunnel", Domain: "runner.example.com", CloudflareToken: "cloudflare-secret"}
	if err := WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, resolved, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != path {
		t.Fatalf("resolved path = %q, want %q", resolved, path)
	}
	redacted := loaded.Redacted()
	for name, value := range map[string]string{
		"user API key":     redacted.Credimi.UserAPIKey,
		"dashboard token":  redacted.Server.DashboardToken,
		"Cloudflare token": redacted.Exposure.CloudflareToken,
	} {
		if value != "[redacted]" {
			t.Fatalf("%s = %q", name, value)
		}
	}

	admin := loaded
	admin.Credimi.AuthMode = "internal_admin"
	admin.Credimi.UserAPIKey = ""
	admin.Credimi.InternalAdminKey = "admin-secret"
	if admin.Redacted().Credimi.InternalAdminKey != "[redacted]" {
		t.Fatal("internal admin key was not redacted")
	}
}

func TestValidationHelpersCoverSupportedModesAndDeviceErrors(t *testing.T) {
	for _, auth := range []CredimiConfig{{AuthMode: "user", UserAPIKey: "key"}, {AuthMode: "internal_admin", InternalAdminKey: "key"}} {
		if err := validateAuth(auth); err != nil {
			t.Fatalf("auth %#v: %v", auth, err)
		}
	}
	for _, exposure := range []ExposureConfig{{Mode: "manual", PublicURL: "https://runner.example"}, {Mode: "quick_tunnel"}, {Mode: "named_tunnel", Domain: "runner.example.com", CloudflareToken: "token"}} {
		if err := validateExposure(exposure); err != nil {
			t.Fatalf("exposure %#v: %v", exposure, err)
		}
	}
	cfg := validConfig().Android
	for _, policy := range []string{"always", "if-not-present", "never"} {
		cfg.PullPolicy = policy
		if err := validateAndroid(cfg); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateListen("listen", "127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if err := required("field", " "); err == nil {
		t.Fatal("empty required string accepted")
	}

	base := validConfig()
	cases := []struct {
		name, want string
		device     DeviceConfig
		goos       string
	}{
		{"missing physical subtype", "requires", DeviceConfig{Type: DeviceAndroidPhysical, AndroidEmulator: &AndroidEmulatorConfig{}}, "linux"},
		{"missing emulator field", "is required", DeviceConfig{Type: DeviceAndroidEmulator, AndroidEmulator: &AndroidEmulatorConfig{APILevel: 1, ABI: "x", SystemImage: "x", BaseName: "x", GoldenSource: "", MemoryMB: 1, Cores: 1}}, "linux"},
		{"bad emulator resources", "must be positive", DeviceConfig{Type: DeviceAndroidEmulator, AndroidEmulator: &AndroidEmulatorConfig{APILevel: 0, ABI: "x", SystemImage: "x", BaseName: "x", GoldenSource: "/x", MemoryMB: 1, Cores: 1}}, "linux"},
		{"missing redroid field", "is required", DeviceConfig{Type: DeviceRedroid, Redroid: &RedroidConfig{Host: "r", Image: "", DataDir: "/d", DataArchive: "/a", ADBPort: 5555}}, "linux"},
		{"ios missing udid", "udid is required", DeviceConfig{Type: DeviceIOSSimulator, IOSSimulator: &IOSSimulatorConfig{}}, "darwin"},
		{"unknown type", "unsupported", DeviceConfig{Type: "other", AndroidPhysical: &AndroidPhysicalConfig{}}, "linux"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDevice(tc.device, "device", tc.goos, map[string]struct{}{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want=%q", err, tc.want)
			}
		})
	}
	_ = base
}

func TestLoadAndWriteRejectUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link.toml")
	if err := os.Symlink(filepath.Join(dir, "missing.toml"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink load error=%v", err)
	}
	if err := WriteFile(link, validConfig()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink write error=%v", err)
	}
	empty := filepath.Join(dir, "empty.toml")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(empty); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty error=%v", err)
	}
}
