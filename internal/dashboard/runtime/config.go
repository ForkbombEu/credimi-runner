package runtime

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

type Values map[string]string

type Store struct {
	Path   string
	Values Values
	exists bool
}

func DefaultConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_CONFIG_DIR")); dir != "" {
		return dir
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, ".config", "credimi", "runner")
	}
	return filepath.Join(configDir, "credimi", "runner")
}

func LoadStore(configDir string) (*Store, error) {
	if strings.TrimSpace(configDir) == "" {
		configDir = DefaultConfigDir()
	}

	store := &Store{
		Path:   filepath.Join(configDir, "config.toml"),
		Values: DefaultValues(),
	}

	cfg, err := runnerconfig.LoadFile(store.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, err
	}
	store.exists = true
	store.Values = ValuesFromTypedConfig(cfg)

	return store, nil
}

func (s *Store) Save(values Values) error {
	snapshot := cloneValues(values)
	cfg, err := TypedConfigFromValues(snapshot)
	if err != nil {
		return err
	}
	if err := runnerconfig.WriteFile(s.Path, cfg); err != nil {
		return err
	}
	s.Values = snapshot
	s.exists = true
	return nil
}

func ValuesFromTypedConfig(cfg runnerconfig.Config) Values {
	values := DefaultValues()
	values["CONFIG_SCHEMA_VERSION"] = strconv.Itoa(cfg.SchemaVersion)
	values["CREDIMI_URL"] = cfg.Credimi.URL
	values["CREDIMI_AUTH_MODE"] = cfg.Credimi.AuthMode
	values["CREDIMI_RUNNER_ID"] = cfg.Runner.ID
	values["CREDIMI_RUNNER_NAME"] = cfg.Runner.Name
	values["CREDIMI_RUNNER_ORGANIZATION"] = cfg.Runner.Organization
	values["CREDIMI_RUNNER_DESCRIPTION"] = cfg.Runner.Description
	values["CREDIMI_RUNNER_PUBLISHED"] = strconv.FormatBool(cfg.Runner.Published)
	values["CREDIMI_USER_API_KEY"] = cfg.Credimi.UserAPIKey
	values["CREDIMI_INTERNAL_ADMIN_KEY"] = cfg.Credimi.InternalAdminKey
	values["TEMPORAL_ADDRESS"] = cfg.Temporal.Address
	values["DASHBOARD_TOKEN"] = cfg.Server.DashboardToken
	values["SERVER_OPEN_BROWSER"] = strconv.FormatBool(cfg.Server.OpenBrowser)
	values["SERVER_READ_HEADER_TIMEOUT"] = cfg.Server.ReadHeaderTimeout.Duration().String()
	values["SERVER_SHUTDOWN_TIMEOUT"] = cfg.Server.ShutdownTimeout.Duration().String()
	if host, port, err := net.SplitHostPort(cfg.Server.APIListen); err == nil {
		values["RUNNER_HOST"], values["RUNNER_PORT"] = host, port
	}
	if host, port, err := net.SplitHostPort(cfg.Server.DashboardListen); err == nil {
		values["DASHBOARD_HOST"], values["DASHBOARD_PORT"] = host, port
	}
	values["CREDIMI_TEMP_DIR"] = cfg.Storage.TempDir
	values["STORAGE_STATE_DIR"] = cfg.Storage.StateDir
	values["STORAGE_ARTIFACT_RETENTION"] = cfg.Storage.ArtifactRetention.Duration().String()
	values["OTEL_ENABLED"] = strconv.FormatBool(cfg.Observability.Enabled)
	values["OTEL_EXPORTER_OTLP_ENDPOINT"] = cfg.Observability.OTLPEndpoint
	values["OTEL_SERVICE_NAME"] = cfg.Observability.ServiceName
	values["ANDROID_RUNNER_IMAGE"] = cfg.Android.RunnerImage
	values["ANDROID_PULL_POLICY"] = cfg.Android.PullPolicy
	values["ANDROID_NETWORK"] = cfg.Android.Network
	values["ANDROID_STATE_VOLUME"] = cfg.Android.StateVolume
	values["ANDROID_TOOL_CACHE_VOLUME"] = cfg.Android.ToolCacheVolume
	values["ANDROID_SDK_VOLUME"] = cfg.Android.SDKVolume
	values["ANDROID_ADB_KEYS_PATH"] = cfg.Android.ADBKeysPath
	values["ADB_SCREEN_RECORD_SIZE"] = cfg.Android.ScreenRecordSize
	switch cfg.Exposure.Mode {
	case "named_tunnel":
		values["CREDIMI_SERVICE_MODE"] = "cloudflare-managed"
	case "manual":
		values["CREDIMI_SERVICE_MODE"] = "manual"
	default:
		values["CREDIMI_SERVICE_MODE"] = "auto"
	}
	values["RUNNER_PUBLIC_URL"] = cfg.Exposure.PublicURL
	values["RUNNER_PUBLIC_PORT"] = cfg.Exposure.PublicPort
	values["RUNNER_DOMAIN"] = cfg.Exposure.Domain
	values["CLOUDFLARE_TUNNEL_TOKEN"] = cfg.Exposure.CloudflareToken
	values["CREDIMI_DEVICE_COUNT"] = strconv.Itoa(len(cfg.Devices))
	for i, device := range cfg.Devices {
		prefix := devicePrefix(i + 1)
		values[prefix+"ID"], values[prefix+"NAME"], values[prefix+"DESCRIPTION"] = device.ID, device.Name, device.Description
		legacyType := string(device.Type)
		if device.Type == runnerconfig.DeviceAndroidPhysical {
			legacyType = "android_phone"
		}
		values[prefix+"TYPE"], values[prefix+"ENABLED"] = legacyType, strconv.FormatBool(device.Enabled)
		switch device.Type {
		case runnerconfig.DeviceAndroidPhysical:
			values[prefix+"MODE"] = device.AndroidPhysical.Transport
			switch device.AndroidPhysical.Transport {
			case "wifi":
				values[prefix+"WIFI_IP"] = device.AndroidPhysical.WiFiIP
				values[prefix+"WIFI_PORT"] = device.AndroidPhysical.WiFiPort
				values[prefix+"SERIAL"] = AndroidWiFiSerial(device.AndroidPhysical.WiFiIP, device.AndroidPhysical.WiFiPort)
			case "usb":
				values[prefix+"SERIAL"] = device.AndroidPhysical.Serial
			}
		case runnerconfig.DeviceAndroidEmulator:
			values[prefix+"MODE"], values[prefix+"AVD_NAME"] = "emulator", device.AndroidEmulator.BaseName
			values[prefix+"BASE_NAME"] = device.AndroidEmulator.BaseName
			values[prefix+"GOLDEN_PATH"] = device.AndroidEmulator.GoldenSource
			values[prefix+"API_LEVEL"] = strconv.Itoa(device.AndroidEmulator.APILevel)
			values[prefix+"ABI"] = device.AndroidEmulator.ABI
			values[prefix+"SYSTEM_IMAGE"] = device.AndroidEmulator.SystemImage
			values[prefix+"HEADLESS"] = strconv.FormatBool(device.AndroidEmulator.Headless)
			values[prefix+"MEMORY_MB"] = strconv.Itoa(device.AndroidEmulator.MemoryMB)
			values[prefix+"CORES"] = strconv.Itoa(device.AndroidEmulator.Cores)
		case runnerconfig.DeviceRedroid:
			values[prefix+"MODE"] = "redroid"
			values[prefix+"WIFI_IP"] = device.Redroid.Host
			values[prefix+"WIFI_PORT"] = strconv.Itoa(device.Redroid.ADBPort)
			values[prefix+"SERIAL"] = AndroidWiFiSerial(device.Redroid.Host, strconv.Itoa(device.Redroid.ADBPort))
			values[prefix+"REDROID_IMAGE"] = device.Redroid.Image
			values[prefix+"REDROID_DATA_DIR"] = device.Redroid.DataDir
			values[prefix+"REDROID_DATA_TAR"] = device.Redroid.DataArchive
			values[prefix+"AVDCTL_SSH_TARGET"] = device.Redroid.AVDCTLSSHTarget
			values[prefix+"AVDCTL_SSH_PASSWORD"] = device.Redroid.AVDCTLSSHPassword
			knownHostsPath := EffectiveSSHKnownHostsPath(device.Redroid.AVDCTLSSHTarget, device.Redroid.AVDCTLSSHKnownHostsPath)
			values[prefix+"AVDCTL_SSH_KNOWN_HOSTS_PATH"] = knownHostsPath
			values[prefix+"AVDCTL_SSH_ARGS"] = AVDCTLSSHArgs(knownHostsPath)
			values[prefix+"AVDCTL_SUDO"] = strconv.FormatBool(device.Redroid.AVDCTLSudo)
			values[prefix+"AVDCTL_SUDO_PASSWORD"] = device.Redroid.AVDCTLSudoPassword
		case runnerconfig.DeviceIOSSimulator:
			values[prefix+"MODE"], values[prefix+"IOS_UDID"] = "no_device", device.IOSSimulator.UDID
		}
	}
	return values
}

func TypedConfigFromValues(values Values) (runnerconfig.Config, error) {
	cfg := runnerconfig.Bootstrap()
	if raw := strings.TrimSpace(values["CONFIG_SCHEMA_VERSION"]); raw != "" {
		version, err := strconv.Atoi(raw)
		if err != nil {
			return cfg, fmt.Errorf("CONFIG_SCHEMA_VERSION must be an integer")
		}
		cfg.SchemaVersion = version
	}
	cfg.Runner.ID, cfg.Runner.Name, cfg.Runner.Organization = values["CREDIMI_RUNNER_ID"], values["CREDIMI_RUNNER_NAME"], values["CREDIMI_RUNNER_ORGANIZATION"]
	published, err := parseBoolean(values["CREDIMI_RUNNER_PUBLISHED"], false, "CREDIMI_RUNNER_PUBLISHED")
	if err != nil {
		return cfg, err
	}
	cfg.Runner.Description, cfg.Runner.Published = values["CREDIMI_RUNNER_DESCRIPTION"], published
	cfg.Credimi.URL, cfg.Credimi.UserAPIKey, cfg.Credimi.InternalAdminKey = values["CREDIMI_URL"], values["CREDIMI_USER_API_KEY"], values["CREDIMI_INTERNAL_ADMIN_KEY"]
	cfg.Credimi.AuthMode = defaultIfEmpty(values["CREDIMI_AUTH_MODE"], cfg.Credimi.AuthMode)
	if values["CREDIMI_AUTH_MODE"] == "" && cfg.Credimi.InternalAdminKey != "" {
		cfg.Credimi.AuthMode = "internal_admin"
	}
	cfg.Temporal.Address = values["TEMPORAL_ADDRESS"]
	cfg.Server.APIListen, cfg.Server.DashboardListen = net.JoinHostPort(values["RUNNER_HOST"], values["RUNNER_PORT"]), net.JoinHostPort(values["DASHBOARD_HOST"], values["DASHBOARD_PORT"])
	cfg.Server.DashboardToken, cfg.Storage.TempDir = values["DASHBOARD_TOKEN"], values["CREDIMI_TEMP_DIR"]
	cfg.Server.OpenBrowser, err = parseBoolean(values["SERVER_OPEN_BROWSER"], cfg.Server.OpenBrowser, "SERVER_OPEN_BROWSER")
	if err != nil {
		return cfg, err
	}
	cfg.Server.ReadHeaderTimeout, err = parseDuration(values["SERVER_READ_HEADER_TIMEOUT"], cfg.Server.ReadHeaderTimeout, "SERVER_READ_HEADER_TIMEOUT")
	if err != nil {
		return cfg, err
	}
	cfg.Server.ShutdownTimeout, err = parseDuration(values["SERVER_SHUTDOWN_TIMEOUT"], cfg.Server.ShutdownTimeout, "SERVER_SHUTDOWN_TIMEOUT")
	if err != nil {
		return cfg, err
	}
	cfg.Storage.StateDir = values["STORAGE_STATE_DIR"]
	cfg.Storage.ArtifactRetention, err = parseDuration(values["STORAGE_ARTIFACT_RETENTION"], cfg.Storage.ArtifactRetention, "STORAGE_ARTIFACT_RETENTION")
	if err != nil {
		return cfg, err
	}
	cfg.Observability.Enabled, err = parseBoolean(values["OTEL_ENABLED"], false, "OTEL_ENABLED")
	if err != nil {
		return cfg, err
	}
	cfg.Observability.OTLPEndpoint = values["OTEL_EXPORTER_OTLP_ENDPOINT"]
	cfg.Observability.ServiceName = values["OTEL_SERVICE_NAME"]
	cfg.Android.RunnerImage = values["ANDROID_RUNNER_IMAGE"]
	cfg.Android.PullPolicy = values["ANDROID_PULL_POLICY"]
	cfg.Android.Network = values["ANDROID_NETWORK"]
	cfg.Android.StateVolume = values["ANDROID_STATE_VOLUME"]
	cfg.Android.ToolCacheVolume = values["ANDROID_TOOL_CACHE_VOLUME"]
	cfg.Android.SDKVolume = values["ANDROID_SDK_VOLUME"]
	cfg.Android.ADBKeysPath = values["ANDROID_ADB_KEYS_PATH"]
	cfg.Android.ScreenRecordSize = values["ADB_SCREEN_RECORD_SIZE"]
	switch values["CREDIMI_SERVICE_MODE"] {
	case "cloudflare-managed":
		cfg.Exposure.Mode = "named_tunnel"
	case "manual":
		cfg.Exposure.Mode = "manual"
	default:
		cfg.Exposure.Mode = "quick_tunnel"
	}
	cfg.Exposure.PublicURL = values["RUNNER_PUBLIC_URL"]
	cfg.Exposure.PublicPort = values["RUNNER_PUBLIC_PORT"]
	cfg.Exposure.Domain = values["RUNNER_DOMAIN"]
	cfg.Exposure.CloudflareToken = values["CLOUDFLARE_TUNNEL_TOKEN"]
	runtimeCfg := RunnerRuntimeConfig{}
	if count := strings.TrimSpace(values["CREDIMI_DEVICE_COUNT"]); count != "" && count != "0" {
		var err error
		runtimeCfg, err = parseRunnerRuntimeConfig(values)
		if err != nil {
			return cfg, err
		}
	}
	for _, device := range runtimeCfg.Devices {
		entry := runnerconfig.DeviceConfig{ID: device.ID, Name: device.Name, Description: device.Description, Enabled: device.Enabled}
		switch device.Type {
		case "android_phone":
			physical := &runnerconfig.AndroidPhysicalConfig{Transport: device.Mode}
			switch device.Mode {
			case "wifi":
				physical.WiFiIP = device.WiFiIP
				if physical.WiFiIP == "" {
					physical.WiFiIP = device.Values["WIFI_IP"]
				}
				physical.WiFiPort = device.WiFiPort
				if physical.WiFiPort == "" {
					physical.WiFiPort = device.Values["WIFI_PORT"]
				}
				if physical.WiFiPort == "" {
					physical.WiFiPort = DefaultWiFiPort
				}
			case "usb":
				physical.Serial = device.Serial
			}
			entry.Type, entry.AndroidPhysical = runnerconfig.DeviceAndroidPhysical, physical
		case "android_emulator":
			apiLevel := atoiDefault(device.Values["API_LEVEL"], 35)
			abi := defaultIfEmpty(device.Values["ABI"], DefaultEmulatorABI(stdruntime.GOOS, stdruntime.GOARCH))
			systemImage := defaultIfEmpty(device.Values["SYSTEM_IMAGE"], "system-images;android-35;google_apis;"+abi)
			baseName := defaultIfEmpty(device.Values["BASE_NAME"], DefaultBaseName)
			goldenSource := defaultIfEmpty(device.Values["GOLDEN_PATH"], "/avd-golden/"+baseName+"-golden")
			headless, err := parseBoolean(device.Values["HEADLESS"], false, fmt.Sprintf("CREDIMI_DEVICE_%d_HEADLESS", device.Index))
			if err != nil {
				return cfg, err
			}
			entry.Type, entry.AndroidEmulator = runnerconfig.DeviceAndroidEmulator, &runnerconfig.AndroidEmulatorConfig{ABI: abi, SystemImage: systemImage, BaseName: baseName, GoldenSource: goldenSource, APILevel: apiLevel, Headless: headless, MemoryMB: atoiDefault(device.Values["MEMORY_MB"], 2048), Cores: atoiDefault(device.Values["CORES"], 2)}
		case "redroid":
			host, port := device.WiFiIP, device.WiFiPort
			if host == "" {
				host = device.Values["WIFI_IP"]
			}
			if port == "" {
				port = device.Values["WIFI_PORT"]
			}
			adbPort, err := runnerconfig.ParseADBPort(port)
			if err != nil {
				return cfg, fmt.Errorf("device %d: %w", device.Index, err)
			}
			sudo, err := parseBoolean(device.Values["AVDCTL_SUDO"], false, "AVDCTL_SUDO")
			if err != nil {
				return cfg, err
			}
			knownHostsPath := EffectiveSSHKnownHostsPath(device.Values["AVDCTL_SSH_TARGET"], device.Values["AVDCTL_SSH_KNOWN_HOSTS_PATH"])
			if err := validateKnownHostsPathValue(device.ID, knownHostsPath); err != nil {
				return cfg, err
			}
			entry.Type, entry.Redroid = runnerconfig.DeviceRedroid, &runnerconfig.RedroidConfig{
				Host:                    host,
				Image:                   defaultIfEmpty(device.Values["REDROID_IMAGE"], "redroid:latest"),
				DataDir:                 defaultIfEmpty(device.Values["REDROID_DATA_DIR"], DefaultRedroidDataDir),
				DataArchive:             defaultIfEmpty(device.Values["REDROID_DATA_TAR"], DefaultRedroidDataTar),
				ADBPort:                 adbPort,
				AVDCTLSSHTarget:         device.Values["AVDCTL_SSH_TARGET"],
				AVDCTLSSHPassword:       device.Values["AVDCTL_SSH_PASSWORD"],
				AVDCTLSSHKnownHostsPath: knownHostsPath,
				AVDCTLSudo:              sudo,
				AVDCTLSudoPassword:      device.Values["AVDCTL_SUDO_PASSWORD"],
			}
		case "ios_simulator":
			entry.Type, entry.IOSSimulator = runnerconfig.DeviceIOSSimulator, &runnerconfig.IOSSimulatorConfig{UDID: device.Values["IOS_UDID"]}
		default:
			return cfg, fmt.Errorf("unsupported device type %q", device.Type)
		}
		cfg.Devices = append(cfg.Devices, entry)
	}
	if err := runnerconfig.ApplyDefaults(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func parseDuration(raw string, fallback runnerconfig.Duration, key string) (runnerconfig.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration", key)
	}
	return runnerconfig.Duration(duration), nil
}

func atoiDefault(raw string, fallback int) int {
	if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && value > 0 {
		return value
	}
	return fallback
}

func parseBoolean(raw string, fallback bool, key string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be boolean", key)
	}
	return parsed, nil
}

func AndroidWiFiSerial(ip, port string) string {
	return runnerconfig.AndroidWiFiSerial(strings.TrimSpace(ip), strings.TrimSpace(port))
}

func DefaultEmulatorABI(goos, goarch string) string {
	if goos == "darwin" && goarch == "arm64" {
		return "arm64-v8a"
	}
	return "x86_64"
}

func (s *Store) Snapshot() Values {
	return cloneValues(s.Values)
}

func (s *Store) Exists() bool {
	return s.exists
}

func cloneValues(values Values) Values {
	cloned := make(Values, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func SortedKnownKeys() []string {
	keys := make([]string, 0, len(KnownKeys))
	for key := range KnownKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
