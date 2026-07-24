package runtime

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DeviceRuntimeConfig is the immutable local configuration for one execution
// target. Values contains only device-scoped values, keyed without the
// CREDIMI_DEVICE_<n>_ prefix.
type DeviceRuntimeConfig struct {
	Index       int
	ID          string
	Name        string
	Description string
	Type        string
	Mode        string
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
	"ID": {}, "NAME": {}, "DESCRIPTION": {}, "TYPE": {}, "MODE": {}, "SERIAL": {}, "WIFI_IP": {}, "WIFI_PORT": {},
	"BASE_NAME": {}, "GOLDEN_PATH": {}, "HOST_AVD_HOME_PATH": {}, "HOST_AVD_GOLDEN_PATH": {},
	"ANDROID_KEYS_DIR": {}, "REDROID_DATA_DIR": {}, "REDROID_DATA_TAR": {},
	"AVD_NAME": {}, "AVDCTL_SSH_TARGET": {}, "AVDCTL_SSH_PASSWORD": {}, "AVDCTL_SSH_KNOWN_HOSTS_PATH": {}, "AVDCTL_SUDO": {}, "AVDCTL_SUDO_PASSWORD": {},
	"RUNNER_IMAGE": {}, "RUNNER_IMAGE_PULL_POLICY": {}, "BACKEND": {}, "CONTAINER_MODE": {},
	"WORK_DIR": {}, "PORT": {}, "CONTAINER_NAME": {}, "IOS_UDID": {},
}

// RunnerKeys are the unindexed settings owned by the host process. Legacy
// single-target keys remain in KnownKeys solely so migration can read them;
// they are not emitted by the multi-device writer.
var RunnerKeys = map[string]struct{}{
	"CREDIMI_DEVICE_COUNT": {}, "CREDIMI_RUNNER_ID": {}, "CREDIMI_RUNNER_NAME": {}, "CREDIMI_RUNNER_ORGANIZATION": {}, "CREDIMI_RUNNER_DESCRIPTION": {}, "CREDIMI_RUNNER_PUBLISHED": {},
	"CREDIMI_URL": {}, "CREDIMI_USER_API_KEY": {}, "CREDIMI_INTERNAL_ADMIN_KEY": {}, "CREDIMI_SERVICE_MODE": {}, "CREDIMI_TEMP_DIR": {}, "TEMPORAL_ADDRESS": {},
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
	seenAVDs, seenPorts, seenContainers, seenPaths := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	for index := 1; index <= count; index++ {
		block := blocks[index]
		if block == nil {
			return RunnerRuntimeConfig{Host: host}, fmt.Errorf("device index %d is missing", index)
		}
		for _, required := range []string{"ID", "NAME", "TYPE", "MODE"} {
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
			"device ID": id, "device name": block["NAME"], "serial": block["SERIAL"], "AVD name": block["AVD_NAME"],
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
			case "serial":
				seen = seenSerials
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
		devices = append(devices, DeviceRuntimeConfig{Index: index, ID: id, Name: block["NAME"], Description: block["DESCRIPTION"], Type: block["TYPE"], Mode: block["MODE"], Serial: block["SERIAL"], WiFiIP: block["WIFI_IP"], WiFiPort: block["WIFI_PORT"], Values: cloneValues(block)})
	}
	return RunnerRuntimeConfig{Host: host, Devices: devices}, nil
}

func canonicalID(value string) string { return strings.TrimPrefix(strings.TrimSpace(value), "/") }

// RuntimeConfig returns the validated immutable inventory persisted in Store.
func (s *Store) RuntimeConfig() (RunnerRuntimeConfig, error) {
	return parseRunnerRuntimeConfig(s.Values)
}

// RuntimeConfigFromEnvironment validates the one root dotenv file after it has
// been loaded into the process environment (or an equivalent deployment
// environment has injected it).
func RuntimeConfigFromEnvironment() (RunnerRuntimeConfig, error) {
	values := Values{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, known := RunnerKeys[key]; known || strings.HasPrefix(key, "CREDIMI_DEVICE_") {
			values[key] = value
		}
	}
	return parseRunnerRuntimeConfig(values)
}

// SaveRuntimeConfig atomically writes host keys followed by deterministic device
// blocks. Unknown runner-level lines are retained; generated device lines are
// always replaced.
func (s *Store) SaveRuntimeConfig(config RunnerRuntimeConfig) error {
	if config.Host == nil {
		config.Host = Values{}
	}
	values := cloneValues(config.Host)
	values["CREDIMI_DEVICE_COUNT"] = strconv.Itoa(len(config.Devices))
	for index, device := range config.Devices {
		for key, value := range device.Values {
			values[devicePrefix(index+1)+key] = value
		}
		values[devicePrefix(index+1)+"ID"] = device.ID
		values[devicePrefix(index+1)+"NAME"] = device.Name
		values[devicePrefix(index+1)+"DESCRIPTION"] = device.Description
		values[devicePrefix(index+1)+"TYPE"] = device.Type
		values[devicePrefix(index+1)+"MODE"] = device.Mode
	}
	config, err := parseRunnerRuntimeConfig(values)
	if err != nil {
		return err
	}
	var lines []string
	lines = append(lines, "# --- Runner host (managed by Credimi Runner) ---")
	for _, key := range SortedRunnerKeys() {
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
	return s.write(strings.Join(lines, "\n")+"\n", values)
}

func SortedRunnerKeys() []string {
	keys := make([]string, 0, len(RunnerKeys))
	for key := range RunnerKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Store) write(content string, values Values) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmpPath := s.Path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return err
	}
	s.Values, s.exists = cloneValues(values), true
	return nil
}

// MigrateLegacySingleTarget converts the previous single-target root .env to
// one indexed device block. It never marks the target registered: callers must
// preview/register the returned device before enabling it. The original is
// retained beside the new configuration for operator recovery.
func (s *Store) MigrateLegacySingleTarget() (DeviceRuntimeConfig, bool, error) {
	if _, configured := s.Values["CREDIMI_DEVICE_COUNT"]; configured {
		return DeviceRuntimeConfig{}, false, nil
	}
	legacyType := strings.TrimSpace(s.Values["CREDIMI_RUNNER_TYPE"])
	if legacyType == "" {
		return DeviceRuntimeConfig{}, false, nil
	}
	if !s.exists {
		return DeviceRuntimeConfig{}, false, nil
	}
	runnerID := canonicalID(s.Values["CREDIMI_RUNNER_ID"])
	if runnerID == "" {
		return DeviceRuntimeConfig{}, false, fmt.Errorf("cannot migrate a target without CREDIMI_RUNNER_ID")
	}
	content, err := os.ReadFile(s.Path)
	if err != nil {
		return DeviceRuntimeConfig{}, false, err
	}
	backup := s.Path + ".before-multi-device-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.WriteFile(backup, content, 0o600); err != nil {
		return DeviceRuntimeConfig{}, false, fmt.Errorf("write migration backup: %w", err)
	}
	host := cloneValues(s.Values)
	deviceValues := Values{}
	legacyToDevice := map[string]string{
		"CREDIMI_RUNNER_SERIAL": "SERIAL", "CREDIMI_RUNNER_WIFI_IP": "WIFI_IP", "CREDIMI_RUNNER_WIFI_PORT": "WIFI_PORT",
		"BASE_NAME": "BASE_NAME", "GOLDEN_PATH": "GOLDEN_PATH", "HOST_AVD_HOME_PATH": "HOST_AVD_HOME_PATH", "HOST_AVD_GOLDEN_PATH": "HOST_AVD_GOLDEN_PATH",
		"ANDROID_KEYS_DIR": "ANDROID_KEYS_DIR", "REDROID_DATA_DIR": "REDROID_DATA_DIR", "REDROID_DATA_TAR": "REDROID_DATA_TAR",
		"AVDCTL_SSH_TARGET": "AVDCTL_SSH_TARGET", "AVDCTL_SSH_PASSWORD": "AVDCTL_SSH_PASSWORD", "AVDCTL_SSH_KNOWN_HOSTS_PATH": "AVDCTL_SSH_KNOWN_HOSTS_PATH", "AVDCTL_SUDO": "AVDCTL_SUDO", "AVDCTL_SUDO_PASSWORD": "AVDCTL_SUDO_PASSWORD",
		"RUNNER_IMAGE": "RUNNER_IMAGE", "RUNNER_IMAGE_PULL_POLICY": "RUNNER_IMAGE_PULL_POLICY", "CREDIMI_RUNNER_BACKEND": "BACKEND", "CREDIMI_CONTAINER_MODE": "CONTAINER_MODE",
	}
	for oldKey, newKey := range legacyToDevice {
		deviceValues[newKey] = host[oldKey]
		delete(host, oldKey)
	}
	mode := strings.TrimSpace(s.Values["CREDIMI_RUNNER_DEVICE_MODE"])
	if mode == "" {
		mode = "no_device"
	}
	for _, oldKey := range []string{"CREDIMI_RUNNER_TYPE", "CREDIMI_RUNNER_DEVICE_MODE"} {
		delete(host, oldKey)
	}
	name := strings.TrimSpace(s.Values["CREDIMI_RUNNER_NAME"])
	if name == "" {
		name = runnerNameFromID(runnerID)
	}
	device := DeviceRuntimeConfig{ID: runnerID + "/" + canonifyPlain(name), Name: name, Type: legacyType, Mode: mode, Values: deviceValues}
	if err := s.SaveRuntimeConfig(RunnerRuntimeConfig{Host: host, Devices: []DeviceRuntimeConfig{device}}); err != nil {
		return DeviceRuntimeConfig{}, false, err
	}
	return s.RuntimeConfigDevice(1), true, nil
}

func (s *Store) RuntimeConfigDevice(index int) DeviceRuntimeConfig {
	config, err := s.RuntimeConfig()
	if err != nil || index < 1 || index > len(config.Devices) {
		return DeviceRuntimeConfig{}
	}
	return config.Devices[index-1]
}

// MigrateLegacyDeviceFiles imports the pre-inventory devices/*.env layout. The
// caller must pass removeLegacy=true only after an operator has confirmed that
// the generated root inventory is correct. A failed or unconfirmed migration
// never deletes the old directory.
func (s *Store) MigrateLegacyDeviceFiles(removeLegacy bool) ([]DeviceRuntimeConfig, bool, error) {
	if _, configured := s.Values["CREDIMI_DEVICE_COUNT"]; configured {
		return nil, false, nil
	}
	legacyDir := filepath.Join(filepath.Dir(s.Path), "devices")
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	runnerID := canonicalID(s.Values["CREDIMI_RUNNER_ID"])
	if runnerID == "" {
		return nil, false, fmt.Errorf("cannot import legacy devices without CREDIMI_RUNNER_ID")
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".env") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, false, nil
	}
	devices := make([]DeviceRuntimeConfig, 0, len(files))
	for _, name := range files {
		values, err := readDotEnvValues(filepath.Join(legacyDir, name))
		if err != nil {
			return nil, false, fmt.Errorf("read legacy device %q: %w", name, err)
		}
		device, err := legacyDeviceConfig(runnerID, strings.TrimSuffix(name, ".env"), values)
		if err != nil {
			return nil, false, fmt.Errorf("import legacy device %q: %w", name, err)
		}
		devices = append(devices, device)
	}
	if s.exists {
		content, err := os.ReadFile(s.Path)
		if err != nil {
			return nil, false, err
		}
		backup := s.Path + ".before-multi-device-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.WriteFile(backup, content, 0o600); err != nil {
			return nil, false, fmt.Errorf("write migration backup: %w", err)
		}
	}
	if err := s.SaveRuntimeConfig(RunnerRuntimeConfig{Host: cloneValues(s.Values), Devices: devices}); err != nil {
		return nil, false, err
	}
	if removeLegacy {
		if err := os.RemoveAll(legacyDir); err != nil {
			return nil, false, fmt.Errorf("remove confirmed legacy devices directory: %w", err)
		}
	}
	return devices, true, nil
}

// RemoveLegacyDeviceFiles performs the destructive half of an already
// successful import. Keeping it separate makes the dashboard confirmation
// explicit and prevents an ordinary migration retry from deleting data.
func (s *Store) RemoveLegacyDeviceFiles() error {
	if _, err := s.RuntimeConfig(); err != nil {
		return fmt.Errorf("cannot remove legacy devices before a valid inventory is saved: %w", err)
	}
	legacyDir := filepath.Join(filepath.Dir(s.Path), "devices")
	if err := os.RemoveAll(legacyDir); err != nil {
		return fmt.Errorf("remove confirmed legacy devices directory: %w", err)
	}
	return nil
}

func readDotEnvValues(path string) (Values, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := Values{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = unquote(strings.TrimSpace(raw))
	}
	return values, scanner.Err()
}

func legacyDeviceConfig(runnerID, fallbackName string, legacy Values) (DeviceRuntimeConfig, error) {
	deviceValues := Values{}
	legacyToDevice := map[string]string{
		"CREDIMI_RUNNER_SERIAL": "SERIAL", "CREDIMI_RUNNER_WIFI_IP": "WIFI_IP", "CREDIMI_RUNNER_WIFI_PORT": "WIFI_PORT",
		"BASE_NAME": "BASE_NAME", "GOLDEN_PATH": "GOLDEN_PATH", "HOST_AVD_HOME_PATH": "HOST_AVD_HOME_PATH", "HOST_AVD_GOLDEN_PATH": "HOST_AVD_GOLDEN_PATH",
		"ANDROID_KEYS_DIR": "ANDROID_KEYS_DIR", "REDROID_DATA_DIR": "REDROID_DATA_DIR", "REDROID_DATA_TAR": "REDROID_DATA_TAR",
		"AVDCTL_SSH_TARGET": "AVDCTL_SSH_TARGET", "AVDCTL_SSH_PASSWORD": "AVDCTL_SSH_PASSWORD", "AVDCTL_SSH_KNOWN_HOSTS_PATH": "AVDCTL_SSH_KNOWN_HOSTS_PATH", "AVDCTL_SUDO": "AVDCTL_SUDO", "AVDCTL_SUDO_PASSWORD": "AVDCTL_SUDO_PASSWORD",
		"RUNNER_IMAGE": "RUNNER_IMAGE", "RUNNER_IMAGE_PULL_POLICY": "RUNNER_IMAGE_PULL_POLICY", "CREDIMI_RUNNER_BACKEND": "BACKEND", "CREDIMI_CONTAINER_MODE": "CONTAINER_MODE",
	}
	for oldKey, newKey := range legacyToDevice {
		deviceValues[newKey] = legacy[oldKey]
	}
	name := strings.TrimSpace(legacy["CREDIMI_RUNNER_NAME"])
	if name == "" {
		name = fallbackName
	}
	deviceType := strings.TrimSpace(legacy["CREDIMI_RUNNER_TYPE"])
	if deviceType == "" {
		return DeviceRuntimeConfig{}, fmt.Errorf("CREDIMI_RUNNER_TYPE is required")
	}
	mode := strings.TrimSpace(legacy["CREDIMI_RUNNER_DEVICE_MODE"])
	if mode == "" {
		mode = "no_device"
	}
	return DeviceRuntimeConfig{ID: runnerID + "/" + canonifyPlain(name), Name: name, Type: deviceType, Mode: mode, Values: deviceValues}, nil
}
