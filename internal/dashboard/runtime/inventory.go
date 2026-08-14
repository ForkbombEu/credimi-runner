package runtime

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DeviceRuntimeConfig is the immutable local configuration for one execution
// target. Its explicit fields are authoritative; Values contains additional
// device-scoped settings, keyed without the CREDIMI_DEVICE_<n>_ prefix.
type DeviceRuntimeConfig struct {
	Index       int
	ID          string
	Name        string
	Description string
	Type        string
	Mode        string
	Enabled     bool
	Serial      string
	WiFiIP      string
	WiFiPort    string
	Values      Values
}

// RunnerRuntimeConfig contains the host configuration and all configured
// execution targets. It is intentionally a value object: callers must pass it
// instead of selecting a target by changing process-wide environment variables.
type RunnerRuntimeConfig struct {
	Host    Values
	Devices []DeviceRuntimeConfig
}

var deviceEnvKey = regexp.MustCompile(`^CREDIMI_DEVICE_([1-9][0-9]*)_([A-Z][A-Z0-9_]*)$`)

// DeviceKeys defines the suffixes allowed in an indexed device block. Local
// target settings are deliberately scoped here rather than being runner keys.
var DeviceKeys = map[string]struct{}{
	"ID": {}, "NAME": {}, "DESCRIPTION": {}, "TYPE": {}, "MODE": {}, "ENABLED": {}, "SERIAL": {}, "WIFI_IP": {}, "WIFI_PORT": {},
	"BASE_NAME": {}, "GOLDEN_PATH": {}, "HOST_AVD_HOME_PATH": {}, "HOST_AVD_GOLDEN_PATH": {},
	"ANDROID_KEYS_DIR": {}, "REDROID_DATA_DIR": {}, "REDROID_DATA_TAR": {},
	"AVD_NAME": {}, "AVDCTL_SSH_TARGET": {}, "AVDCTL_SSH_PASSWORD": {}, "AVDCTL_SSH_KNOWN_HOSTS_PATH": {}, "AVDCTL_SUDO": {}, "AVDCTL_SUDO_PASSWORD": {},
	"WORK_DIR": {}, "PORT": {}, "CONTAINER_NAME": {}, "IOS_UDID": {},
}

// RunnerKeys are the unindexed settings owned by the host process. Legacy
// single-target keys remain in KnownKeys solely so migration can read them;
// they are not emitted by the multi-device writer.
var RunnerKeys = map[string]struct{}{
	"CREDIMI_DEVICE_COUNT": {}, "CREDIMI_RUNNER_ID": {}, "CREDIMI_RUNNER_NAME": {}, "CREDIMI_RUNNER_ORGANIZATION": {}, "CREDIMI_RUNNER_DESCRIPTION": {}, "CREDIMI_RUNNER_PUBLISHED": {},
	"CREDIMI_URL": {}, "CREDIMI_USER_API_KEY": {}, "CREDIMI_INTERNAL_ADMIN_KEY": {}, "CREDIMI_SERVICE_MODE": {}, "CREDIMI_TEMP_DIR": {}, "TEMPORAL_ADDRESS": {},
	"ANDROID_RUNNER_IMAGE": {}, "ANDROID_PULL_POLICY": {}, "ANDROID_NETWORK": {}, "ANDROID_STATE_VOLUME": {}, "ANDROID_TOOL_CACHE_VOLUME": {}, "ANDROID_SDK_VOLUME": {}, "ANDROID_ADB_KEYS_PATH": {},
	"DASHBOARD_HOST": {}, "DASHBOARD_PORT": {}, "DASHBOARD_TOKEN": {}, "RUNNER_HOST": {}, "RUNNER_PORT": {}, "RUNNER_PUBLIC_PORT": {}, "RUNNER_PUBLIC_URL": {}, "RUNNER_DOMAIN": {}, "RUNNER_CADDY_SITE": {},
	"CLOUDFLARE_TUNNEL_TOKEN": {}, "OTEL_ENABLED": {}, "OTEL_EXPORTER_OTLP_ENDPOINT": {}, "OTEL_SERVICE_NAME": {},
}

func devicePrefix(index int) string { return fmt.Sprintf("CREDIMI_DEVICE_%d_", index) }

func parseRunnerRuntimeConfig(values Values) (RunnerRuntimeConfig, error) {
	host := cloneValues(values)
	countText, configured := host["CREDIMI_DEVICE_COUNT"]
	if !configured || strings.TrimSpace(countText) == "" {
		return RunnerRuntimeConfig{Host: host}, fmt.Errorf("CREDIMI_DEVICE_COUNT is required")
	}
	count, err := strconv.Atoi(strings.TrimSpace(countText))
	if err != nil || count < 1 {
		return RunnerRuntimeConfig{Host: host}, fmt.Errorf("CREDIMI_DEVICE_COUNT must be a positive integer")
	}
	runnerID := canonicalID(host["CREDIMI_RUNNER_ID"])
	if runnerID == "" {
		return RunnerRuntimeConfig{Host: host}, fmt.Errorf("CREDIMI_RUNNER_ID is required")
	}
	host["CREDIMI_RUNNER_ID"] = runnerID

	blocks := make(map[int]Values, count)
	for key, value := range values {
		if key == "CREDIMI_DEVICE_COUNT" {
			continue
		}
		if key == "CREDIMI_DEVICE_ID" {
			return RunnerRuntimeConfig{Host: host}, fmt.Errorf("CREDIMI_DEVICE_ID without an index is invalid")
		}
		if !strings.HasPrefix(key, "CREDIMI_DEVICE_") {
			continue
		}
		match := deviceEnvKey.FindStringSubmatch(key)
		if match == nil {
			return RunnerRuntimeConfig{Host: host}, fmt.Errorf("malformed device key %q", key)
		}
		index, _ := strconv.Atoi(match[1])
		if index > count {
			return RunnerRuntimeConfig{Host: host}, fmt.Errorf("device key %q is beyond CREDIMI_DEVICE_COUNT", key)
		}
		if _, ok := DeviceKeys[match[2]]; !ok {
			return RunnerRuntimeConfig{Host: host}, fmt.Errorf("unknown device key %q", key)
		}
		if blocks[index] == nil {
			blocks[index] = Values{}
		}
		blocks[index][match[2]] = strings.TrimSpace(value)
	}

	devices := make([]DeviceRuntimeConfig, 0, count)
	seenIDs, seenNames, seenSerials := map[string]int{}, map[string]int{}, map[string]int{}
	emulatorIndex, simulatorIndex := 0, 0
	seenAVDs, seenPorts, seenContainers, seenPaths := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	for index := 1; index <= count; index++ {
		block := blocks[index]
		if block == nil {
			return RunnerRuntimeConfig{Host: host}, fmt.Errorf("device index %d is missing", index)
		}
		// The runtime is deliberately configured only with its execution
		// inventory. A device ID establishes that inventory; display and setup
		// metadata belongs to the dashboard registration flow and is validated
		// there immediately before it is sent to Credimi.
		for _, required := range []string{"ID"} {
			if strings.TrimSpace(block[required]) == "" {
				return RunnerRuntimeConfig{Host: host}, fmt.Errorf("CREDIMI_DEVICE_%d_%s is required", index, required)
			}
		}
		id := canonicalID(block["ID"])
		if !strings.HasPrefix(id, runnerID+"/") {
			return RunnerRuntimeConfig{Host: host}, fmt.Errorf("device %q must be a child of runner %q", id, runnerID)
		}
		block["ID"] = id
		for key, value := range map[string]string{
			"device ID": id, "device name": block["NAME"], "AVD name": block["AVD_NAME"],
			"port": block["PORT"], "container name": block["CONTAINER_NAME"], "work path": block["WORK_DIR"],
		} {
			if strings.TrimSpace(value) == "" {
				continue
			}
			var seen map[string]int
			switch key {
			case "device ID":
				seen = seenIDs
			case "device name":
				seen = seenNames
			case "AVD name":
				seen = seenAVDs
			case "port":
				seen = seenPorts
			case "container name":
				seen = seenContainers
			default:
				seen = seenPaths
			}
			if other, exists := seen[value]; exists {
				return RunnerRuntimeConfig{Host: host}, fmt.Errorf("duplicate %s %q in devices %d and %d", key, value, other, index)
			}
			seen[value] = index
		}
		switch block["TYPE"] {
		case "android_emulator":
			if emulatorIndex != 0 {
				return RunnerRuntimeConfig{Host: host}, fmt.Errorf("an Android emulator is already registered as device %q; only one is allowed", devices[emulatorIndex-1].Name)
			}
			emulatorIndex = index
		case "ios_simulator":
			if simulatorIndex != 0 {
				return RunnerRuntimeConfig{Host: host}, fmt.Errorf("an iOS simulator is already registered as device %q; only one is allowed", devices[simulatorIndex-1].Name)
			}
			simulatorIndex = index
		}
		enabled := true
		if raw := strings.TrimSpace(block["ENABLED"]); raw != "" {
			enabled, err = strconv.ParseBool(raw)
			if err != nil {
				return RunnerRuntimeConfig{Host: host}, fmt.Errorf("CREDIMI_DEVICE_%d_ENABLED must be boolean", index)
			}
		}
		serial := strings.TrimSpace(block["SERIAL"])
		wifiIP, wifiPort := strings.TrimSpace(block["WIFI_IP"]), strings.TrimSpace(block["WIFI_PORT"])
		if block["TYPE"] == "android_phone" && block["MODE"] == "wifi" {
			if wifiPort == "" {
				wifiPort = DefaultWiFiPort
			}
			serial = AndroidWiFiSerial(wifiIP, wifiPort)
			block["WIFI_PORT"], block["SERIAL"] = wifiPort, serial
		} else if block["TYPE"] == "android_phone" && block["MODE"] == "no_device" {
			serial, wifiIP, wifiPort = "", "", ""
			delete(block, "SERIAL")
			delete(block, "WIFI_IP")
			delete(block, "WIFI_PORT")
		} else if block["TYPE"] == "redroid" {
			if wifiPort == "" {
				wifiPort = DefaultWiFiPort
			}
			serial = AndroidWiFiSerial(wifiIP, wifiPort)
			block["WIFI_PORT"], block["SERIAL"] = wifiPort, serial
		}
		if serial != "" && (block["TYPE"] == "android_phone" || block["TYPE"] == "redroid") {
			if other, exists := seenSerials[serial]; exists {
				return RunnerRuntimeConfig{Host: host}, fmt.Errorf("serial %q is already registered for device %q; choose a different serial for device %q", serial, devices[other-1].Name, block["NAME"])
			}
			seenSerials[serial] = index
		}
		devices = append(devices, DeviceRuntimeConfig{Index: index, ID: id, Name: block["NAME"], Description: block["DESCRIPTION"], Type: block["TYPE"], Mode: block["MODE"], Enabled: enabled, Serial: serial, WiFiIP: wifiIP, WiFiPort: wifiPort, Values: cloneValues(block)})
	}
	return RunnerRuntimeConfig{Host: host, Devices: devices}, nil
}

// ParseRuntimeConfig validates an in-memory root environment without loading
// or writing files. Dashboard state uses this to render indexed blocks.
func ParseRuntimeConfig(values Values) (RunnerRuntimeConfig, error) {
	return parseRunnerRuntimeConfig(values)
}

// ValidateDeviceRegistration validates the metadata the dashboard must send
// when registering a device with Credimi. It intentionally is not part of the
// direct-serve environment validation.
func ValidateDeviceRegistration(device DeviceRuntimeConfig) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"name", device.Name},
		{"type", device.Type},
		{"mode", device.Mode},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("device %s is required for dashboard registration", field.name)
		}
	}
	return nil
}

// ValidateDeviceConstraints applies runner-wide target limits before a config
// is persisted. The API applies the same rules authoritatively, while this
// keeps setup and dashboard errors immediate and readable.
func ValidateDeviceConstraints(devices []DeviceRuntimeConfig) error {
	seenSerials := map[string]string{}
	emulatorIndex, simulatorIndex := 0, 0
	for index, device := range devices {
		position := index + 1
		switch strings.TrimSpace(device.Type) {
		case "android_emulator":
			if emulatorIndex != 0 {
				return fmt.Errorf("an Android emulator is already registered as device %q; only one is allowed", devices[emulatorIndex-1].Name)
			}
			emulatorIndex = position
		case "ios_simulator":
			if simulatorIndex != 0 {
				return fmt.Errorf("an iOS simulator is already registered as device %q; only one is allowed", devices[simulatorIndex-1].Name)
			}
			simulatorIndex = position
		case "android_phone", "redroid":
			serial := strings.TrimSpace(device.Serial)
			if serial == "" {
				continue
			}
			if other, exists := seenSerials[serial]; exists {
				return fmt.Errorf("serial %q is already registered for device %q; choose a different serial for device %q", serial, other, device.Name)
			}
			seenSerials[serial] = device.Name
		}
	}
	return nil
}

func canonicalID(value string) string { return strings.TrimPrefix(strings.TrimSpace(value), "/") }

// RuntimeConfig returns the validated immutable inventory persisted in Store.
func (s *Store) RuntimeConfig() (RunnerRuntimeConfig, error) {
	return parseRunnerRuntimeConfig(s.Values)
}

// RuntimeConfigFromEnvironment is retained as a source-compatible name for
// the GoA server. The runner configuration is loaded from typed TOML; process
// environment values are not accepted as a configuration source.
func RuntimeConfigFromEnvironment() (RunnerRuntimeConfig, error) {
	store, err := LoadStore(DefaultConfigDir())
	if err != nil {
		return RunnerRuntimeConfig{}, err
	}
	if !store.Exists() {
		return RunnerRuntimeConfig{}, fmt.Errorf("runner config.toml is required")
	}
	return store.RuntimeConfig()
}

// SaveRuntimeConfig atomically writes host keys followed by deterministic device
// blocks. Unknown runner-level lines are retained; generated device lines are
// always replaced.
func (s *Store) SaveRuntimeConfig(config RunnerRuntimeConfig) error {
	values := ValuesWithRuntimeDevices(config.Host, config.Devices)
	config, err := parseRunnerRuntimeConfig(values)
	if err != nil {
		return err
	}
	var lines []string
	lines = append(lines, "# --- Runner host (managed by Credimi Runner) ---")
	for _, key := range SortedRunnerKeys() {
		if key == "CREDIMI_DEVICE_COUNT" {
			continue
		}
		lines = append(lines, key+"="+quote(config.Host[key]))
	}
	lines = append(lines, "", "# --- Device inventory (managed by Credimi Runner; do not edit generated keys) ---", "CREDIMI_DEVICE_COUNT="+strconv.Itoa(len(config.Devices)))
	for _, device := range config.Devices {
		lines = append(lines, "", fmt.Sprintf("# --- Device %d: %s ---", device.Index, device.Name))
		keys := make([]string, 0, len(device.Values))
		for key := range device.Values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			lines = append(lines, devicePrefix(device.Index)+key+"="+quote(device.Values[key]))
		}
	}
	for _, line := range s.UnknownLines {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	typed, err := configFromLegacyValues(values)
	if err != nil {
		return err
	}
	return s.writeTOML(typed, values)
}

// ValuesWithRuntimeDevices returns the compatibility values that represent a
// complete host configuration and inventory. Callers can validate the full
// candidate before handing it to SaveRuntimeConfig, without exposing a
// partially-written config.toml to another process.
func ValuesWithRuntimeDevices(host Values, devices []DeviceRuntimeConfig) Values {
	values := cloneValues(host)
	for key, defaultValue := range DefaultValues() {
		if strings.TrimSpace(values[key]) == "" {
			values[key] = defaultValue
		}
	}
	for key := range values {
		if strings.HasPrefix(key, "CREDIMI_DEVICE_") {
			delete(values, key)
		}
	}
	values["CREDIMI_DEVICE_COUNT"] = strconv.Itoa(len(devices))
	for index, device := range devices {
		prefix := devicePrefix(index + 1)
		for key, value := range device.Values {
			values[prefix+key] = value
		}
		values[prefix+"ID"] = device.ID
		values[prefix+"NAME"] = device.Name
		values[prefix+"DESCRIPTION"] = device.Description
		values[prefix+"TYPE"] = device.Type
		values[prefix+"MODE"] = device.Mode
		values[prefix+"ENABLED"] = strconv.FormatBool(device.Enabled)
		delete(values, prefix+"SERIAL")
		delete(values, prefix+"WIFI_IP")
		delete(values, prefix+"WIFI_PORT")
		switch {
		case device.Type == "android_phone" && device.Mode == "wifi":
			wifiPort := strings.TrimSpace(device.WiFiPort)
			if wifiPort == "" {
				wifiPort = DefaultWiFiPort
			}
			values[prefix+"WIFI_IP"] = strings.TrimSpace(device.WiFiIP)
			values[prefix+"WIFI_PORT"] = wifiPort
			values[prefix+"SERIAL"] = AndroidWiFiSerial(device.WiFiIP, wifiPort)
		case device.Type == "redroid":
			adbPort := strings.TrimSpace(device.WiFiPort)
			if adbPort == "" {
				adbPort = DefaultWiFiPort
			}
			values[prefix+"WIFI_IP"] = strings.TrimSpace(device.WiFiIP)
			values[prefix+"WIFI_PORT"] = adbPort
			values[prefix+"SERIAL"] = AndroidWiFiSerial(device.WiFiIP, adbPort)
		case device.Type == "android_phone" && device.Mode == "usb":
			if serial := strings.TrimSpace(device.Serial); serial != "" {
				values[prefix+"SERIAL"] = serial
			}
		}
	}
	return values
}

func SortedRunnerKeys() []string {
	keys := make([]string, 0, len(RunnerKeys))
	for key := range RunnerKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Store) RuntimeConfigDevice(index int) DeviceRuntimeConfig {
	config, err := s.RuntimeConfig()
	if err != nil || index < 1 || index > len(config.Devices) {
		return DeviceRuntimeConfig{}
	}
	return config.Devices[index-1]
}
