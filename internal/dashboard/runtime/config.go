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

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

type Values map[string]string

type Store struct {
	Path         string
	Values       Values
	UnknownLines []string
	exists       bool
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
	store.Values = legacyValuesFromConfig(cfg)

	return store, nil
}

func (s *Store) Save(values Values) error {
	snapshot := cloneValues(values)
	cfg, err := configFromLegacyValues(snapshot)
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

func (s *Store) writeTOML(cfg runnerconfig.Config, values Values) error {
	if err := runnerconfig.WriteFile(s.Path, cfg); err != nil {
		return err
	}
	s.Values, s.exists = cloneValues(values), true
	return nil
}

func legacyValuesFromConfig(cfg runnerconfig.Config) Values {
	values := DefaultValues()
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
	if host, port, err := net.SplitHostPort(cfg.Server.APIListen); err == nil {
		values["RUNNER_HOST"], values["RUNNER_PORT"] = host, port
	}
	if host, port, err := net.SplitHostPort(cfg.Server.DashboardListen); err == nil {
		values["DASHBOARD_HOST"], values["DASHBOARD_PORT"] = host, port
	}
	values["CREDIMI_TEMP_DIR"] = cfg.Storage.TempDir
	values["ANDROID_RUNNER_IMAGE"] = defaultIfEmpty(cfg.Android.RunnerImage, values["ANDROID_RUNNER_IMAGE"])
	values["ANDROID_PULL_POLICY"] = defaultIfEmpty(cfg.Android.PullPolicy, values["ANDROID_PULL_POLICY"])
	values["ANDROID_NETWORK"] = defaultIfEmpty(cfg.Android.Network, values["ANDROID_NETWORK"])
	values["ANDROID_STATE_VOLUME"] = defaultIfEmpty(cfg.Android.StateVolume, values["ANDROID_STATE_VOLUME"])
	values["ANDROID_TOOL_CACHE_VOLUME"] = defaultIfEmpty(cfg.Android.ToolCacheVolume, values["ANDROID_TOOL_CACHE_VOLUME"])
	values["ANDROID_SDK_VOLUME"] = defaultIfEmpty(cfg.Android.SDKVolume, values["ANDROID_SDK_VOLUME"])
	values["ANDROID_ADB_KEYS_PATH"] = cfg.Android.ADBKeysPath
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
			values[prefix+"MODE"], values[prefix+"AVD_NAME"] = "emulator", device.AndroidEmulator.AVDName
			values[prefix+"BASE_NAME"] = device.AndroidEmulator.BaseName
			values[prefix+"GOLDEN_PATH"] = device.AndroidEmulator.GoldenSource
		case runnerconfig.DeviceRedroid:
			values[prefix+"MODE"] = "redroid"
			values[prefix+"WIFI_IP"] = device.Redroid.Host
			values[prefix+"WIFI_PORT"] = strconv.Itoa(device.Redroid.ADBPort)
			values[prefix+"SERIAL"] = AndroidWiFiSerial(device.Redroid.Host, strconv.Itoa(device.Redroid.ADBPort))
		case runnerconfig.DeviceIOSSimulator:
			values[prefix+"MODE"], values[prefix+"IOS_UDID"] = "no_device", device.IOSSimulator.UDID
		}
	}
	return values
}

func configFromLegacyValues(values Values) (runnerconfig.Config, error) {
	cfg := runnerconfig.Bootstrap()
	cfg.Runner.ID, cfg.Runner.Name, cfg.Runner.Organization = values["CREDIMI_RUNNER_ID"], values["CREDIMI_RUNNER_NAME"], values["CREDIMI_RUNNER_ORGANIZATION"]
	cfg.Runner.Description, cfg.Runner.Published = values["CREDIMI_RUNNER_DESCRIPTION"], strings.EqualFold(values["CREDIMI_RUNNER_PUBLISHED"], "true")
	cfg.Credimi.URL, cfg.Credimi.UserAPIKey, cfg.Credimi.InternalAdminKey = values["CREDIMI_URL"], values["CREDIMI_USER_API_KEY"], values["CREDIMI_INTERNAL_ADMIN_KEY"]
	if cfg.Credimi.InternalAdminKey != "" {
		cfg.Credimi.AuthMode = "internal_admin"
	}
	cfg.Temporal.Address = values["TEMPORAL_ADDRESS"]
	cfg.Server.APIListen, cfg.Server.DashboardListen = net.JoinHostPort(values["RUNNER_HOST"], values["RUNNER_PORT"]), net.JoinHostPort(values["DASHBOARD_HOST"], values["DASHBOARD_PORT"])
	cfg.Server.DashboardToken, cfg.Storage.TempDir = values["DASHBOARD_TOKEN"], values["CREDIMI_TEMP_DIR"]
	cfg.Android.RunnerImage = values["ANDROID_RUNNER_IMAGE"]
	cfg.Android.PullPolicy = values["ANDROID_PULL_POLICY"]
	cfg.Android.Network = values["ANDROID_NETWORK"]
	cfg.Android.StateVolume = values["ANDROID_STATE_VOLUME"]
	cfg.Android.ToolCacheVolume = values["ANDROID_TOOL_CACHE_VOLUME"]
	cfg.Android.SDKVolume = values["ANDROID_SDK_VOLUME"]
	cfg.Android.ADBKeysPath = values["ANDROID_ADB_KEYS_PATH"]
	switch values["CREDIMI_SERVICE_MODE"] {
	case "cloudflare-managed":
		cfg.Exposure.Mode = "named_tunnel"
	case "manual":
		cfg.Exposure.Mode = "manual"
	default:
		cfg.Exposure.Mode = "quick_tunnel"
	}
	cfg.Exposure.PublicURL, cfg.Exposure.PublicPort, cfg.Exposure.CloudflareToken = values["RUNNER_PUBLIC_URL"], values["RUNNER_PUBLIC_PORT"], values["CLOUDFLARE_TUNNEL_TOKEN"]
	runtimeCfg, err := parseRunnerRuntimeConfig(values)
	if err != nil && strings.TrimSpace(values["CREDIMI_DEVICE_COUNT"]) != "" {
		return cfg, err
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
			abi := DefaultEmulatorABI(stdruntime.GOOS, stdruntime.GOARCH)
			entry.Type, entry.AndroidEmulator = runnerconfig.DeviceAndroidEmulator, &runnerconfig.AndroidEmulatorConfig{AVDName: device.Values["AVD_NAME"], ABI: abi, SystemImage: "system-images;android-35;google_apis;" + abi, BaseName: "credimi", GoldenSource: "/avd-golden/credimi-golden", APILevel: 35, MemoryMB: 2048, Cores: 2}
		case "redroid":
			host, port := device.WiFiIP, device.WiFiPort
			if host == "" {
				host = device.Values["WIFI_IP"]
			}
			if port == "" {
				port = device.Values["WIFI_PORT"]
			}
			adbPort, _ := strconv.Atoi(port)
			if adbPort == 0 {
				adbPort = 5555
			}
			entry.Type, entry.Redroid = runnerconfig.DeviceRedroid, &runnerconfig.RedroidConfig{Host: host, Image: "redroid:latest", DataDir: defaultIfEmpty(device.Values["REDROID_DATA_DIR"], "/var/lib/credimi-runner/redroid"), DataArchive: defaultIfEmpty(device.Values["REDROID_DATA_TAR"], "/var/lib/credimi-runner/redroid.tar"), ADBPort: adbPort}
		case "ios_simulator":
			entry.Type, entry.IOSSimulator = runnerconfig.DeviceIOSSimulator, &runnerconfig.IOSSimulatorConfig{UDID: device.Values["IOS_UDID"]}
		default:
			return cfg, fmt.Errorf("unsupported device type %q", device.Type)
		}
		cfg.Devices = append(cfg.Devices, entry)
	}
	return cfg, nil
}

func AndroidWiFiSerial(ip, port string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	port = strings.TrimSpace(port)
	if port == "" {
		port = DefaultWiFiPort
	}
	return net.JoinHostPort(ip, port)
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

func quote(value string) string {
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " \t#\"'") {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return value
}
