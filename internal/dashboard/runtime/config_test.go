package runtime

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func testTOMLConfig(dir string) config.Config {
	return config.Config{
		SchemaVersion: config.SchemaVersion,
		Runner:        config.RunnerConfig{ID: "acme/runner", Name: "Runner", Organization: "acme"},
		Credimi:       config.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"},
		Temporal:      config.TemporalConfig{Address: "temporal.example:7233"},
		Server:        config.ServerConfig{APIListen: "127.0.0.1:8050", DashboardListen: "127.0.0.1:8051", ReadHeaderTimeout: config.Duration(1), ShutdownTimeout: config.Duration(1)},
		Exposure:      config.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example"},
		Storage:       config.StorageConfig{StateDir: filepath.Join(dir, "state"), ArtifactRetention: config.Duration(1)},
		Devices:       []config.DeviceConfig{{ID: "acme/runner/one", Name: "One", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "usb", Serial: "one"}}},
	}
}

func TestLoadStoreMissingTOML(t *testing.T) {
	store, err := LoadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store.Exists() || filepath.Base(store.Path) != "config.toml" {
		t.Fatalf("store=%#v", store)
	}
}

func TestManagedExposureValuesRoundTripThroughTypedStore(t *testing.T) {
	dir := t.TempDir()
	cfg := testTOMLConfig(dir)
	cfg.Exposure = config.ExposureConfig{Mode: "named_tunnel", Domain: "runner.example.com", CloudflareToken: "secret"}
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"CREDIMI_SERVICE_MODE": "cloudflare-managed", "RUNNER_DOMAIN": "runner.example.com", "CLOUDFLARE_TUNNEL_TOKEN": "secret",
	} {
		if store.Values[key] != want {
			t.Fatalf("%s = %q, want %q", key, store.Values[key], want)
		}
	}
	values := store.Snapshot()
	values["RUNNER_DOMAIN"] = "new.example.com"
	values["CLOUDFLARE_TUNNEL_TOKEN"] = "new-secret"
	if err := store.Save(values); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.LoadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Exposure.Domain != "new.example.com" || reloaded.Exposure.CloudflareToken != "new-secret" {
		t.Fatalf("saved managed exposure = %#v", reloaded.Exposure)
	}
}

func TestAndroidScreenRecordSizeRoundTrip(t *testing.T) {
	cfg := testTOMLConfig(t.TempDir())
	cfg.Android.ScreenRecordSize = "1280x720"

	values := ValuesFromTypedConfig(cfg)
	if values["ADB_SCREEN_RECORD_SIZE"] != "1280x720" {
		t.Fatalf("ADB_SCREEN_RECORD_SIZE = %q", values["ADB_SCREEN_RECORD_SIZE"])
	}

	roundTripped, err := TypedConfigFromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	if roundTripped.Android.ScreenRecordSize != "1280x720" {
		t.Fatalf("screen_record_size = %q", roundTripped.Android.ScreenRecordSize)
	}
}

func TestDefaultEmulatorABIFollowsNativeHostArchitecture(t *testing.T) {
	for _, test := range []struct {
		goos, goarch, want string
	}{
		{"darwin", "arm64", "arm64-v8a"},
		{"darwin", "amd64", "x86_64"},
		{"linux", "arm64", "x86_64"},
		{"linux", "amd64", "x86_64"},
	} {
		t.Run(test.goos+"/"+test.goarch, func(t *testing.T) {
			if got := DefaultEmulatorABI(test.goos, test.goarch); got != test.want {
				t.Fatalf("DefaultEmulatorABI() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStoreLoadsAndSavesTypedTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteFile(path, testTOMLConfig(dir)); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.Values["CREDIMI_RUNNER_ID"] != "acme/runner" || store.Values["CREDIMI_DEVICE_1_SERIAL"] != "one" {
		t.Fatalf("values=%#v", store.Values)
	}
	inventory, err := store.RuntimeConfig()
	if err != nil || len(inventory.Devices) != 1 {
		t.Fatalf("inventory=%#v err=%v", inventory, err)
	}
	inventory.Devices[0].Serial = "two:5555"
	inventory.Devices[0].Values["SERIAL"] = "two:5555"
	if err := store.SaveRuntimeConfig(inventory); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Values["CREDIMI_DEVICE_1_SERIAL"] != "two:5555" {
		t.Fatalf("reloaded=%#v", reloaded.Values)
	}
	if got := reloaded.RuntimeConfigDevice(1); got.ID != "acme/runner/one" || got.Serial != "two:5555" {
		t.Fatalf("runtime device = %#v", got)
	}
	if empty := reloaded.RuntimeConfigDevice(2); empty.ID != "" {
		t.Fatalf("out-of-range runtime device = %#v", empty)
	}
	if err := reloaded.Save(reloaded.Values); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeConfigFromEnvironmentUsesTypedConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), testTOMLConfig(dir)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", dir)
	runtimeConfig, err := RuntimeConfigFromEnvironment()
	if err != nil || len(runtimeConfig.Devices) != 1 {
		t.Fatalf("runtime config = %#v err=%v", runtimeConfig, err)
	}
}

func TestPhysicalAddressCompatibilityRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := store.Snapshot()
	base["CREDIMI_RUNNER_ID"] = "acme/runner"
	base["CREDIMI_RUNNER_NAME"] = "runner"
	base["CREDIMI_RUNNER_ORGANIZATION"] = "acme"
	base["CREDIMI_URL"] = "https://credimi.example"
	base["CREDIMI_USER_API_KEY"] = "key"
	base["TEMPORAL_ADDRESS"] = "temporal.example:7233"

	cases := []struct {
		name       string
		device     DeviceRuntimeConfig
		wantSerial string
		wantMode   string
		check      func(*testing.T, config.Config)
	}{
		{name: "usb", device: DeviceRuntimeConfig{ID: "acme/runner/usb", Name: "USB", Type: "android_phone", Mode: "usb", Enabled: true, Serial: "SERIAL_ONE", Values: Values{}}, wantSerial: "SERIAL_ONE", wantMode: "usb", check: func(t *testing.T, cfg config.Config) {
			if got := cfg.Devices[0].AndroidPhysical.Serial; got != "SERIAL_ONE" {
				t.Fatalf("typed USB serial = %q", got)
			}
		}},
		{name: "wifi ipv6", device: DeviceRuntimeConfig{ID: "acme/runner/wifi", Name: "Wi-Fi", Type: "android_phone", Mode: "wifi", Enabled: true, WiFiIP: "2001:db8::20", WiFiPort: "5555", Values: Values{}}, wantSerial: "[2001:db8::20]:5555", wantMode: "wifi", check: func(t *testing.T, cfg config.Config) {
			physical := cfg.Devices[0].AndroidPhysical
			if physical.Serial != "" || physical.WiFiIP != "2001:db8::20" || physical.WiFiPort != "5555" {
				t.Fatalf("typed Wi-Fi config = %#v", physical)
			}
		}},
		{name: "no device", device: DeviceRuntimeConfig{ID: "acme/runner/idle", Name: "Idle", Type: "android_phone", Mode: "no_device", Enabled: true, Values: Values{}}, wantMode: "no_device", check: func(t *testing.T, cfg config.Config) {
			physical := cfg.Devices[0].AndroidPhysical
			if physical.Serial != "" || physical.WiFiIP != "" || physical.WiFiPort != "" {
				t.Fatalf("typed no-device config = %#v", physical)
			}
		}},
		{name: "redroid", device: DeviceRuntimeConfig{ID: "acme/runner/redroid", Name: "Redroid", Type: "redroid", Mode: "redroid", Enabled: true, WiFiIP: "192.0.2.20", WiFiPort: "5555", Values: Values{}}, wantSerial: "192.0.2.20:5555", wantMode: "redroid", check: func(t *testing.T, cfg config.Config) {
			redroid := cfg.Devices[0].Redroid
			if redroid.Host != "192.0.2.20" || redroid.ADBPort != 5555 {
				t.Fatalf("typed Redroid config = %#v", redroid)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := ValuesWithRuntimeDevices(base, []DeviceRuntimeConfig{tc.device})
			if values["CREDIMI_DEVICE_1_MODE"] != tc.wantMode {
				t.Fatalf("mode values = %#v", values)
			}
			if values["CREDIMI_DEVICE_1_SERIAL"] != tc.wantSerial {
				t.Fatalf("serial values = %#v, want %q", values, tc.wantSerial)
			}
			if err := store.Save(values); err != nil {
				t.Fatal(err)
			}
			loaded, err := config.LoadFile(filepath.Join(dir, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, loaded)
		})
	}
}

func TestTypedCompatibilityConverterRoundTripsCanonicalCases(t *testing.T) {
	base := testTOMLConfig(t.TempDir())
	cases := []struct {
		name       string
		cfg        func(config.Config) config.Config
		check      func(*testing.T, config.Config)
		compatWant map[string]string
	}{
		{name: "manual exposure", cfg: func(cfg config.Config) config.Config {
			cfg.Devices = nil
			cfg.Exposure = config.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example", PublicPort: "8050"}
			return cfg
		}, check: func(t *testing.T, cfg config.Config) {
			if cfg.Exposure.PublicURL != "https://runner.example" || cfg.Exposure.PublicPort != "8050" {
				t.Fatalf("manual exposure = %#v", cfg.Exposure)
			}
		}, compatWant: map[string]string{"CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_PORT": "8050"}},
		{name: "named exposure", cfg: func(cfg config.Config) config.Config {
			cfg.Devices = nil
			cfg.Exposure = config.ExposureConfig{Mode: "named_tunnel", Domain: "runner.example.com", CloudflareToken: "secret"}
			return cfg
		}, check: func(t *testing.T, cfg config.Config) {
			if cfg.Exposure.Domain != "runner.example.com" || cfg.Exposure.CloudflareToken != "secret" {
				t.Fatalf("named exposure = %#v", cfg.Exposure)
			}
		}, compatWant: map[string]string{"CREDIMI_SERVICE_MODE": "cloudflare-managed", "RUNNER_DOMAIN": "runner.example.com"}},
		{name: "usb", cfg: func(cfg config.Config) config.Config {
			cfg.Devices = []config.DeviceConfig{{ID: "acme/runner/usb", Name: "USB", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "usb", Serial: "SERIAL_ONE"}}}
			return cfg
		}, check: func(t *testing.T, cfg config.Config) {
			if cfg.Devices[0].AndroidPhysical.Serial != "SERIAL_ONE" {
				t.Fatalf("USB = %#v", cfg.Devices[0].AndroidPhysical)
			}
		}, compatWant: map[string]string{"CREDIMI_DEVICE_1_SERIAL": "SERIAL_ONE"}},
		{name: "wifi", cfg: func(cfg config.Config) config.Config {
			cfg.Devices = []config.DeviceConfig{{ID: "acme/runner/wifi", Name: "Wi-Fi", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.0.2.20", WiFiPort: "5555"}}}
			return cfg
		}, check: func(t *testing.T, cfg config.Config) {
			physical := cfg.Devices[0].AndroidPhysical
			if physical.Serial != "" || physical.WiFiIP != "192.0.2.20" || physical.WiFiPort != "5555" {
				t.Fatalf("Wi-Fi = %#v", physical)
			}
		}, compatWant: map[string]string{"CREDIMI_DEVICE_1_SERIAL": "192.0.2.20:5555"}},
		{name: "no device", cfg: func(cfg config.Config) config.Config {
			cfg.Devices = []config.DeviceConfig{{ID: "acme/runner/idle", Name: "Idle", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "no_device"}}}
			return cfg
		}, check: func(t *testing.T, cfg config.Config) {
			if physical := cfg.Devices[0].AndroidPhysical; physical.Serial != "" || physical.WiFiIP != "" || physical.WiFiPort != "" {
				t.Fatalf("no-device = %#v", physical)
			}
		}, compatWant: map[string]string{"CREDIMI_DEVICE_1_MODE": "no_device"}},
		{name: "emulator", cfg: func(cfg config.Config) config.Config {
			cfg.Devices = []config.DeviceConfig{{ID: "acme/runner/emulator", Name: "Emulator", Type: config.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &config.AndroidEmulatorConfig{BaseName: "credimi-base", GoldenSource: "/golden/credimi-base", ABI: "x86_64", SystemImage: "system-images;android-35;google_apis;x86_64", APILevel: 35, MemoryMB: 2048, Cores: 2}}}
			return cfg
		}, check: func(t *testing.T, cfg config.Config) {
			if cfg.Devices[0].AndroidEmulator.BaseName != "credimi-base" {
				t.Fatalf("emulator = %#v", cfg.Devices[0].AndroidEmulator)
			}
		}, compatWant: map[string]string{"CREDIMI_DEVICE_1_BASE_NAME": "credimi-base", "CREDIMI_DEVICE_1_AVD_NAME": "credimi-base"}},
		{name: "redroid", cfg: func(cfg config.Config) config.Config {
			cfg.Devices = []config.DeviceConfig{{ID: "acme/runner/redroid", Name: "Redroid", Type: config.DeviceRedroid, Enabled: true, Redroid: &config.RedroidConfig{Host: "192.0.2.30", Image: "redroid:custom", DataDir: "/data", DataArchive: "/data.tar", ADBPort: 5556, AVDCTLSSHTarget: "root@redroid", AVDCTLSSHPassword: "ssh-secret", AVDCTLSSHKnownHostsPath: "/known_hosts", AVDCTLSudo: true, AVDCTLSudoPassword: "sudo-secret"}}}
			return cfg
		}, check: func(t *testing.T, cfg config.Config) {
			want := &config.RedroidConfig{Host: "192.0.2.30", Image: "redroid:custom", DataDir: "/data", DataArchive: "/data.tar", ADBPort: 5556, AVDCTLSSHTarget: "root@redroid", AVDCTLSSHPassword: "ssh-secret", AVDCTLSSHKnownHostsPath: "/known_hosts", AVDCTLSudo: true, AVDCTLSudoPassword: "sudo-secret"}
			if redroid := cfg.Devices[0].Redroid; !reflect.DeepEqual(redroid, want) {
				t.Fatalf("redroid = %#v", redroid)
			}
		}, compatWant: map[string]string{
			"CREDIMI_DEVICE_1_SERIAL":                      "192.0.2.30:5556",
			"CREDIMI_DEVICE_1_REDROID_IMAGE":               "redroid:custom",
			"CREDIMI_DEVICE_1_AVDCTL_SSH_TARGET":           "root@redroid",
			"CREDIMI_DEVICE_1_AVDCTL_SSH_PASSWORD":         "ssh-secret",
			"CREDIMI_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH": "/known_hosts",
			"CREDIMI_DEVICE_1_AVDCTL_SUDO":                 "true",
			"CREDIMI_DEVICE_1_AVDCTL_SUDO_PASSWORD":        "sudo-secret",
		}},
		{name: "ios simulator", cfg: func(cfg config.Config) config.Config {
			cfg.Devices = []config.DeviceConfig{{ID: "acme/runner/ios", Name: "iOS", Type: config.DeviceIOSSimulator, Enabled: true, IOSSimulator: &config.IOSSimulatorConfig{UDID: "ios-1"}}}
			return cfg
		}, check: func(t *testing.T, cfg config.Config) {
			if cfg.Devices[0].IOSSimulator.UDID != "ios-1" {
				t.Fatalf("iOS = %#v", cfg.Devices[0].IOSSimulator)
			}
		}, compatWant: map[string]string{"CREDIMI_DEVICE_1_IOS_UDID": "ios-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := ValuesFromTypedConfig(tc.cfg(base))
			got, err := TypedConfigFromValues(values)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, got)
			for key, want := range tc.compatWant {
				if got := ValuesFromTypedConfig(got)[key]; got != want {
					t.Fatalf("compatibility %s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestTypedConfigFromValuesRejectsMalformedRedroidPort(t *testing.T) {
	values := Values{
		"CREDIMI_RUNNER_ID":           "acme/runner",
		"CREDIMI_RUNNER_NAME":         "runner",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
		"CREDIMI_URL":                 "https://credimi.example",
		"CREDIMI_USER_API_KEY":        "key",
		"CREDIMI_DEVICE_COUNT":        "1",
		"CREDIMI_DEVICE_1_ID":         "acme/runner/redroid",
		"CREDIMI_DEVICE_1_NAME":       "redroid",
		"CREDIMI_DEVICE_1_TYPE":       "redroid",
		"CREDIMI_DEVICE_1_MODE":       "redroid",
		"CREDIMI_DEVICE_1_WIFI_IP":    "192.0.2.40",
		"CREDIMI_DEVICE_1_WIFI_PORT":  "not-a-port",
	}
	if _, err := TypedConfigFromValues(values); err == nil {
		t.Fatal("malformed Redroid port was accepted")
	}
}

func TestRedroidAVDCTLSettingsSurviveTOMLReload(t *testing.T) {
	dir := t.TempDir()
	cfg := testTOMLConfig(dir)
	cfg.Devices = []config.DeviceConfig{{
		ID: "acme/runner/redroid", Name: "Redroid", Type: config.DeviceRedroid, Enabled: true,
		Redroid: &config.RedroidConfig{
			Host: "192.0.2.40", Image: "redroid:custom", DataDir: "/srv/redroid", DataArchive: "/srv/redroid.tar", ADBPort: 5566,
			AVDCTLSSHTarget: "root@redroid", AVDCTLSSHPassword: "ssh-secret", AVDCTLSSHKnownHostsPath: "/known_hosts", AVDCTLSudo: true, AVDCTLSudoPassword: "sudo-secret",
		},
	}}
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := TypedConfigFromValues(store.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Devices[0].Redroid, cfg.Devices[0].Redroid) {
		t.Fatalf("reloaded Redroid config = %#v, want %#v", got.Devices[0].Redroid, cfg.Devices[0].Redroid)
	}
}

func TestRedroidAVDCTLSudoRejectsMalformedBoolean(t *testing.T) {
	values := Values{
		"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID": "acme/runner/redroid", "CREDIMI_DEVICE_1_TYPE": "redroid", "CREDIMI_DEVICE_1_MODE": "redroid",
		"CREDIMI_DEVICE_1_WIFI_IP": "192.0.2.50", "CREDIMI_DEVICE_1_WIFI_PORT": "5555", "CREDIMI_DEVICE_1_AVDCTL_SUDO": "sometimes",
	}
	if _, err := TypedConfigFromValues(values); err == nil {
		t.Fatal("malformed AVDCTL_SUDO accepted")
	}
}

func TestRedroidDefaultsUseCanonicalDataPaths(t *testing.T) {
	values := Values{
		"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID": "acme/runner/redroid", "CREDIMI_DEVICE_1_TYPE": "redroid", "CREDIMI_DEVICE_1_MODE": "redroid",
		"CREDIMI_DEVICE_1_WIFI_IP": "192.0.2.51", "CREDIMI_DEVICE_1_WIFI_PORT": "5555",
	}
	cfg, err := TypedConfigFromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	redroid := cfg.Devices[0].Redroid
	if redroid.DataDir != DefaultRedroidDataDir || redroid.DataArchive != DefaultRedroidDataTar {
		t.Fatalf("Redroid defaults = %#v", redroid)
	}
}

func TestTypedCompatibilityConverterPreservesAllCanonicalSettings(t *testing.T) {
	cfg := testTOMLConfig(t.TempDir())
	cfg.SchemaVersion = config.SchemaVersion
	cfg.Credimi.AuthMode = "internal_admin"
	cfg.Credimi.UserAPIKey = ""
	cfg.Credimi.InternalAdminKey = "internal-secret"
	cfg.Server.OpenBrowser = false
	cfg.Server.ReadHeaderTimeout = config.Duration(11 * time.Second)
	cfg.Server.ShutdownTimeout = config.Duration(17 * time.Second)
	cfg.Storage.StateDir = "/srv/credimi/state"
	cfg.Storage.TempDir = "/srv/credimi/tmp"
	cfg.Storage.ArtifactRetention = config.Duration(29 * time.Hour)
	cfg.Android = config.AndroidConfig{RunnerImage: "runner:custom", PullPolicy: "never", Network: "custom-net", StateVolume: "state-vol", ToolCacheVolume: "tools-vol", SDKVolume: "sdk-vol", ADBKeysPath: "/srv/adb-keys"}
	cfg.Devices = []config.DeviceConfig{
		{ID: "acme/runner/phone", Name: "Phone", Description: "physical", Type: config.DeviceAndroidPhysical, Enabled: false, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.0.2.60", WiFiPort: "5560"}},
		{ID: "acme/runner/emulator", Name: "Emulator", Description: "custom emulator", Type: config.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &config.AndroidEmulatorConfig{APILevel: 34, ABI: "custom-abi", SystemImage: "custom-image", BaseName: "custom-base", GoldenSource: "/custom-golden", Headless: true, MemoryMB: 4096, Cores: 4}},
		{ID: "acme/runner/redroid", Name: "Redroid", Description: "custom redroid", Type: config.DeviceRedroid, Enabled: true, Redroid: &config.RedroidConfig{Host: "192.0.2.61", Image: "redroid:custom", DataDir: "/srv/redroid", DataArchive: "/srv/redroid.tar", ADBPort: 5561, AVDCTLSSHTarget: "root@redroid", AVDCTLSSHPassword: "ssh-secret", AVDCTLSSHKnownHostsPath: "/known_hosts", AVDCTLSudo: true, AVDCTLSudoPassword: "sudo-secret"}},
	}
	if err := config.ApplyDefaults(&cfg); err != nil {
		t.Fatal(err)
	}
	values := ValuesFromTypedConfig(cfg)
	values["CREDIMI_RUNNER_DESCRIPTION"] = "unrelated edit"
	got, err := TypedConfigFromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	want := cfg
	want.Runner.Description = "unrelated edit"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lossy typed round-trip:\n got: %#v\nwant: %#v", got, want)
	}
	store, err := LoadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(values); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadStore(filepath.Dir(store.Path))
	if err != nil {
		t.Fatal(err)
	}
	reloadedConfig, err := TypedConfigFromValues(reloaded.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloadedConfig, want) {
		t.Fatalf("lossy TOML reload:\n got: %#v\nwant: %#v", reloadedConfig, want)
	}
}

func TestLoadStorePreservesEmulatorActivityEnvironment(t *testing.T) {
	dir := t.TempDir()
	cfg := testTOMLConfig(dir)
	cfg.Devices = []config.DeviceConfig{{
		ID: "acme/runner/emulator", Name: "Emulator", Type: config.DeviceAndroidEmulator, Enabled: true,
		AndroidEmulator: &config.AndroidEmulatorConfig{
			BaseName: "credimi-base", GoldenSource: "/avd-golden/credimi-base",
			ABI: "x86_64", SystemImage: "system-images;android-35;google_apis;x86_64", APILevel: 35, MemoryMB: 2048, Cores: 2,
		},
	}}
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := store.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	device := inventory.Devices[0]
	if device.Values["BASE_NAME"] != "credimi-base" || device.Values["AVD_NAME"] != "credimi-base" || device.Values["GOLDEN_PATH"] != "/avd-golden/credimi-base" {
		t.Fatalf("emulator activity environment = %#v", device.Values)
	}
}

func TestDefaultConfigDirHonorsOverride(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", "/tmp/runner-config")
	if got := DefaultConfigDir(); got != "/tmp/runner-config" {
		t.Fatalf("dir=%q", got)
	}
}

func TestDefaultConfigDirResolvesPlatformDefault(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", "")
	if got := DefaultConfigDir(); filepath.Base(got) != "runner" {
		t.Fatalf("platform default config dir = %q", got)
	}
}

func TestRuntimeConfigRejectsInvalidInventory(t *testing.T) {
	_, err := ParseRuntimeConfig(Values{"CREDIMI_DEVICE_COUNT": "1", "CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_1_UNKNOWN": "bad"})
	if err == nil {
		t.Fatal("invalid inventory accepted")
	}
}

func TestValidateDeviceConstraintsExplainsConflicts(t *testing.T) {
	err := ValidateDeviceConstraints([]DeviceRuntimeConfig{{Name: "One", Type: "android_phone", Serial: "same"}, {Name: "Two", Type: "redroid", Serial: "same"}})
	if err == nil {
		t.Fatal("duplicate serial accepted")
	}
}

func TestRuntimeConfigValidationAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot()["ANDROID_RUNNER_IMAGE"]; got != DefaultAndroidRunnerImage {
		t.Fatalf("default runner image = %q", got)
	}
	if err := ValidateDeviceRegistration(DeviceRuntimeConfig{Name: "Pixel", Type: "android_phone", Mode: "usb"}); err != nil {
		t.Fatal(err)
	}
	for _, device := range []DeviceRuntimeConfig{{Type: "android_phone", Mode: "usb"}, {Name: "Pixel", Mode: "usb"}, {Name: "Pixel", Type: "android_phone"}} {
		if err := ValidateDeviceRegistration(device); err == nil {
			t.Fatalf("invalid registration accepted: %#v", device)
		}
	}
	if err := ValidateDeviceConstraints([]DeviceRuntimeConfig{{Name: "Emulator 1", Type: "android_emulator"}, {Name: "Emulator 2", Type: "android_emulator"}}); err == nil {
		t.Fatal("multiple emulators accepted")
	}
}

func TestParseRuntimeConfigPreservesMultiDeviceInventory(t *testing.T) {
	values := Values{
		"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "3",
		"CREDIMI_DEVICE_1_ID": "acme/runner/phone", "CREDIMI_DEVICE_1_TYPE": "android_phone", "CREDIMI_DEVICE_1_MODE": "wifi", "CREDIMI_DEVICE_1_WIFI_IP": "phone", "CREDIMI_DEVICE_1_WIFI_PORT": "5555", "CREDIMI_DEVICE_1_ENABLED": "true",
		"CREDIMI_DEVICE_2_ID": "acme/runner/emulator", "CREDIMI_DEVICE_2_TYPE": "android_emulator", "CREDIMI_DEVICE_2_MODE": "emulator", "CREDIMI_DEVICE_2_BASE_NAME": "pixel", "CREDIMI_DEVICE_2_PORT": "5556",
		"CREDIMI_DEVICE_3_ID": "acme/runner/redroid", "CREDIMI_DEVICE_3_TYPE": "redroid", "CREDIMI_DEVICE_3_MODE": "redroid", "CREDIMI_DEVICE_3_WIFI_IP": "redroid", "CREDIMI_DEVICE_3_WIFI_PORT": "5555", "CREDIMI_DEVICE_3_ENABLED": "false",
	}
	parsed, err := ParseRuntimeConfig(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Devices) != 3 || parsed.Devices[1].Values["AVD_NAME"] != "pixel" || parsed.Devices[2].Enabled {
		t.Fatalf("parsed inventory = %#v", parsed.Devices)
	}
}

func TestRedroidReadinessSurvivesTypedTOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	values := Values{
		"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_RUNNER_NAME": "runner", "CREDIMI_RUNNER_ORGANIZATION": "acme",
		"CREDIMI_URL": "https://credimi.example", "CREDIMI_USER_API_KEY": "key", "TEMPORAL_ADDRESS": "temporal.example:7233", "RUNNER_HOST": "127.0.0.1", "RUNNER_PORT": "8050", "DASHBOARD_HOST": "127.0.0.1", "DASHBOARD_PORT": "8051", "CREDIMI_TEMP_DIR": t.TempDir(), "CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID": "acme/runner/redroid", "CREDIMI_DEVICE_1_NAME": "Redroid", "CREDIMI_DEVICE_1_TYPE": "redroid", "CREDIMI_DEVICE_1_MODE": "redroid", "CREDIMI_DEVICE_1_WIFI_IP": "redroid", "CREDIMI_DEVICE_1_WIFI_PORT": "5555",
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(values); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Snapshot()["CREDIMI_DEVICE_1_TYPE"] != "redroid" {
		t.Fatalf("loaded type = %q", loaded.Snapshot()["CREDIMI_DEVICE_1_TYPE"])
	}
	if DeviceReadinessRequired(loaded.Snapshot(), "linux") {
		t.Fatal("idle Redroid must not require startup ADB readiness")
	}
}

func TestParseRuntimeConfigRejectsMalformedInventory(t *testing.T) {
	cases := []struct {
		name   string
		values Values
	}{
		{"missing count", Values{}},
		{"bad count", Values{"CREDIMI_DEVICE_COUNT": "zero"}},
		{"missing runner", Values{"CREDIMI_DEVICE_COUNT": "1"}},
		{"unindexed device", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_ID": "bad"}},
		{"malformed key", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_x_ID": "bad"}},
		{"beyond count", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_2_ID": "acme/runner/two"}},
		{"unknown key", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_1_UNKNOWN": "bad"}},
		{"missing block", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1"}},
		{"wrong child", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "other/device"}},
		{"bad enabled", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_1_ENABLED": "sometimes"}},
		{"duplicate ID", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "2", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_2_ID": "acme/runner/one"}},
		{"duplicate name", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "2", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_1_NAME": "same", "CREDIMI_DEVICE_2_ID": "acme/runner/two", "CREDIMI_DEVICE_2_NAME": "same"}},
		{"duplicate emulator", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "2", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_1_TYPE": "android_emulator", "CREDIMI_DEVICE_1_BASE_NAME": "one", "CREDIMI_DEVICE_2_ID": "acme/runner/two", "CREDIMI_DEVICE_2_TYPE": "android_emulator", "CREDIMI_DEVICE_2_BASE_NAME": "two"}},
		{"duplicate serial", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "2", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_1_TYPE": "android_phone", "CREDIMI_DEVICE_1_MODE": "usb", "CREDIMI_DEVICE_1_SERIAL": "same:5555", "CREDIMI_DEVICE_2_ID": "acme/runner/two", "CREDIMI_DEVICE_2_TYPE": "redroid", "CREDIMI_DEVICE_2_WIFI_IP": "same", "CREDIMI_DEVICE_2_WIFI_PORT": "5555"}},
		{"duplicate port", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "2", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_1_PORT": "5555", "CREDIMI_DEVICE_2_ID": "acme/runner/two", "CREDIMI_DEVICE_2_PORT": "5555"}},
		{"duplicate container", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "2", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_1_CONTAINER_NAME": "same", "CREDIMI_DEVICE_2_ID": "acme/runner/two", "CREDIMI_DEVICE_2_CONTAINER_NAME": "same"}},
		{"duplicate work path", Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "2", "CREDIMI_DEVICE_1_ID": "acme/runner/one", "CREDIMI_DEVICE_1_WORK_DIR": "/same", "CREDIMI_DEVICE_2_ID": "acme/runner/two", "CREDIMI_DEVICE_2_WORK_DIR": "/same"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRuntimeConfig(tc.values); err == nil {
				t.Fatal("malformed inventory accepted")
			}
		})
	}
}
