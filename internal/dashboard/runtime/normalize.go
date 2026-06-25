package runtime

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	DefaultCredimiURL       = "https://credimi.io"
	DefaultTempDir          = "/tmp/credimi-runner-tmp"
	DefaultTemporalAddress  = "temporal.credimi.io:7233"
	DefaultOTLPEndpoint     = "https://otel-collector.credimi.io"
	DefaultOTELServiceName  = "credimi-runner"
	DefaultRunnerHost       = "127.0.0.1"
	DefaultRunnerPort       = "8050"
	DefaultDashboardHost    = "127.0.0.1"
	DefaultDashboardPort    = "8051"
	DefaultRunnerCaddySite  = ":80"
	DefaultContainerMode    = "usb"
	DefaultPhoneImage       = "ghcr.io/forkbombeu/credimi-runner-phone:latest"
	DefaultEmulatorImage    = "ghcr.io/forkbombeu/credimi-runner-emulator:latest"
	DefaultBaseName         = "credimi"
	DefaultGoldenPath       = "/avd-golden/credimi-golden"
	DefaultWiFiPort         = "5555"
	DefaultRedroidDataDir   = "/home/credimi/redroid-data"
	DefaultRedroidDataTar   = "/home/credimi/redroid-data.tar"
	DefaultHostAVDHome      = ".android/avd"
	DefaultHostAVDGolden    = "avd-golden"
	DefaultContainerBackend = "container"
	DefaultHostBackend      = "host"
)

var KnownKeys = map[string]struct{}{
	"ANDROID_KEYS_DIR":            {},
	"AVDCTL_SSH_KNOWN_HOSTS_PATH": {},
	"AVDCTL_SSH_PASSWORD":         {},
	"AVDCTL_SSH_TARGET":           {},
	"AVDCTL_SUDO":                 {},
	"AVDCTL_SUDO_PASSWORD":        {},
	"BASE_NAME":                   {},
	"CLOUDFLARE_TUNNEL_TOKEN":     {},
	"CREDIMI_CONTAINER_MODE":      {},
	"CREDIMI_INTERNAL_ADMIN_KEY":  {},
	"CREDIMI_RUNNER_BACKEND":      {},
	"CREDIMI_RUNNER_DESCRIPTION":  {},
	"CREDIMI_RUNNER_DEVICE_MODE":  {},
	"CREDIMI_RUNNER_ID":           {},
	"CREDIMI_RUNNER_NAME":         {},
	"CREDIMI_RUNNER_ORGANIZATION": {},
	"CREDIMI_RUNNER_SERIAL":       {},
	"CREDIMI_RUNNER_TYPE":         {},
	"CREDIMI_RUNNER_WIFI_IP":      {},
	"CREDIMI_RUNNER_WIFI_PORT":    {},
	"CREDIMI_SERVICE_MODE":        {},
	"CREDIMI_TEMP_DIR":            {},
	"CREDIMI_URL":                 {},
	"CREDIMI_USER_API_KEY":        {},
	"DASHBOARD_HOST":              {},
	"DASHBOARD_PORT":              {},
	"DASHBOARD_TOKEN":             {},
	"GOLDEN_PATH":                 {},
	"HOST_AVD_GOLDEN_PATH":        {},
	"HOST_AVD_HOME_PATH":          {},
	"OTEL_ENABLED":                {},
	"OTEL_EXPORTER_OTLP_ENDPOINT": {},
	"OTEL_SERVICE_NAME":           {},
	"REDROID_DATA_DIR":            {},
	"REDROID_DATA_TAR":            {},
	"RUNNER_CADDY_SITE":           {},
	"RUNNER_DOMAIN":               {},
	"RUNNER_HOST":                 {},
	"RUNNER_IMAGE":                {},
	"RUNNER_PORT":                 {},
	"RUNNER_PUBLIC_PORT":          {},
	"RUNNER_PUBLIC_URL":           {},
}

func DefaultValues() Values {
	homeDir, _ := homeDir()
	values := Values{
		"CREDIMI_RUNNER_WIFI_PORT":    DefaultWiFiPort,
		"CREDIMI_SERVICE_MODE":        "auto",
		"CREDIMI_TEMP_DIR":            DefaultTempDir,
		"CREDIMI_URL":                 DefaultCredimiURL,
		"DASHBOARD_HOST":              DefaultDashboardHost,
		"DASHBOARD_PORT":              DefaultDashboardPort,
		"OTEL_ENABLED":                "true",
		"OTEL_EXPORTER_OTLP_ENDPOINT": DefaultOTLPEndpoint,
		"OTEL_SERVICE_NAME":           DefaultOTELServiceName,
		"REDROID_DATA_DIR":            DefaultRedroidDataDir,
		"REDROID_DATA_TAR":            DefaultRedroidDataTar,
		"RUNNER_CADDY_SITE":           DefaultRunnerCaddySite,
		"RUNNER_HOST":                 DefaultRunnerHost,
		"RUNNER_IMAGE":                DefaultPhoneImage,
		"RUNNER_PORT":                 DefaultRunnerPort,
		"TEMPORAL_ADDRESS":            DefaultTemporalAddress,
	}
	if homeDir != "" {
		values["ANDROID_KEYS_DIR"] = filepath.Join(homeDir, ".android")
		values["HOST_AVD_HOME_PATH"] = filepath.Join(homeDir, DefaultHostAVDHome)
		values["HOST_AVD_GOLDEN_PATH"] = filepath.Join(homeDir, DefaultHostAVDGolden)
	}
	return values
}

func NormalizeValues(values Values, goos string) (Values, error) {
	if strings.TrimSpace(goos) == "" {
		goos = runtime.GOOS
	}

	normalized := DefaultValues()
	for key, value := range values {
		normalized[key] = strings.TrimSpace(value)
	}

	normalized["CREDIMI_RUNNER_BACKEND"] = defaultIfEmpty(normalized["CREDIMI_RUNNER_BACKEND"], defaultServiceBackend(goos))
	normalized["CREDIMI_SERVICE_MODE"] = normalizeServiceMode(normalized["CREDIMI_SERVICE_MODE"])
	normalized["CREDIMI_RUNNER_TYPE"] = defaultRunnerType(normalized, goos)
	normalizeRunnerIdentity(normalized)

	if err := validateRunnerTypeSupported(goos, normalized["CREDIMI_RUNNER_TYPE"]); err != nil {
		return nil, err
	}

	switch normalized["CREDIMI_RUNNER_TYPE"] {
	case "android_emulator":
		normalizeAndroidEmulator(normalized)
	case "ios_simulator":
		normalizeIOSSimulator(normalized)
	case "redroid":
		normalizeRedroid(normalized)
	default:
		if err := normalizeAndroidPhone(normalized); err != nil {
			return nil, err
		}
	}

	if !defaultYesNoChoice(normalized["OTEL_ENABLED"], true) {
		normalized["OTEL_EXPORTER_OTLP_ENDPOINT"] = ""
	}

	return normalized, nil
}

func normalizeRunnerIdentity(values Values) {
	runnerID := strings.TrimSpace(values["CREDIMI_RUNNER_ID"])
	if runnerID == "" {
		return
	}
	if strings.TrimSpace(values["CREDIMI_RUNNER_NAME"]) == "" {
		values["CREDIMI_RUNNER_NAME"] = runnerNameFromID(runnerID)
	}
	if strings.TrimSpace(values["CREDIMI_RUNNER_ORGANIZATION"]) == "" {
		values["CREDIMI_RUNNER_ORGANIZATION"] = runnerOrgFromID(runnerID)
	}
}

func defaultServiceBackend(goos string) string {
	switch goos {
	case "darwin":
		return DefaultHostBackend
	case "linux":
		return DefaultContainerBackend
	default:
		return ""
	}
}

func defaultRunnerType(values Values, goos string) string {
	if runnerType := strings.TrimSpace(values["CREDIMI_RUNNER_TYPE"]); runnerType != "" {
		return runnerType
	}

	switch strings.TrimSpace(values["CREDIMI_CONTAINER_MODE"]) {
	case "emulator":
		return "android_emulator"
	case "usb", "wifi":
		return "android_phone"
	}

	if defaultServiceBackend(goos) == DefaultHostBackend {
		return "ios_simulator"
	}

	return "android_phone"
}

func runnerTypeChoices(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"android_emulator", "ios_simulator", "redroid", "android_phone"}
	case "linux":
		return []string{"android_emulator", "redroid", "android_phone"}
	default:
		return nil
	}
}

func validateRunnerTypeSupported(goos, runnerType string) error {
	for _, candidate := range runnerTypeChoices(goos) {
		if runnerType == candidate {
			return nil
		}
	}
	return fmt.Errorf("runner type %q is not supported on %s", runnerType, goos)
}

func defaultAndroidDeviceMode(values Values, runnerType string) string {
	switch runnerType {
	case "redroid":
		return "no_device"
	case "android_phone":
		switch strings.TrimSpace(values["CREDIMI_RUNNER_DEVICE_MODE"]) {
		case "usb", "wifi":
			return strings.TrimSpace(values["CREDIMI_RUNNER_DEVICE_MODE"])
		}
		if strings.TrimSpace(values["CREDIMI_CONTAINER_MODE"]) == "wifi" {
			return "wifi"
		}
		return "usb"
	default:
		return "no_device"
	}
}

func defaultYesNoChoice(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func normalizeServiceMode(value string) string {
	switch strings.TrimSpace(value) {
	case "quick":
		return "auto"
	case "direct":
		return "manual"
	case "named":
		return "cloudflare-managed"
	case "auto", "cloudflare-managed", "manual":
		return strings.TrimSpace(value)
	default:
		return "auto"
	}
}

func resolvedRunnerPublicURL(values Values, quickTunnelURL string) string {
	switch normalizeServiceMode(values["CREDIMI_SERVICE_MODE"]) {
	case "manual":
		return strings.TrimSpace(values["RUNNER_PUBLIC_URL"])
	case "cloudflare-managed":
		domain := strings.TrimSpace(values["RUNNER_DOMAIN"])
		if domain == "" {
			return ""
		}
		if strings.Contains(domain, "://") {
			return domain
		}
		return "https://" + domain
	default:
		return strings.TrimSpace(quickTunnelURL)
	}
}

func clearAVDCTLSSHConfig(values Values) {
	values["AVDCTL_SSH_TARGET"] = ""
	values["AVDCTL_SSH_PASSWORD"] = ""
	values["AVDCTL_SSH_KNOWN_HOSTS_PATH"] = ""
	values["AVDCTL_SUDO"] = ""
	values["AVDCTL_SUDO_PASSWORD"] = ""
}

func runnerNameFromID(value string) string {
	value = runnerIDWithoutLeadingSlash(value)
	index := strings.LastIndex(value, "/")
	if index < 0 {
		return value
	}
	return value[index+1:]
}

func runnerOrgFromID(value string) string {
	value = runnerIDWithoutLeadingSlash(value)
	org, _, ok := strings.Cut(value, "/")
	if !ok {
		return ""
	}
	return org
}

func runnerIDWithoutLeadingSlash(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "/")
}

func canonifyPlain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		alphaNum := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
		if alphaNum {
			builder.WriteRune(char)
			lastDash = false
			continue
		}
		if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(builder.String(), "-")
	if out == "" {
		return "runner"
	}
	return out
}

func normalizeAndroidEmulator(values Values) {
	backend := values["CREDIMI_RUNNER_BACKEND"]
	values["CREDIMI_RUNNER_SERIAL"] = ""
	values["CREDIMI_RUNNER_DEVICE_MODE"] = ""
	values["CREDIMI_RUNNER_WIFI_IP"] = ""
	values["CREDIMI_RUNNER_WIFI_PORT"] = ""
	if strings.TrimSpace(values["RUNNER_IMAGE"]) == "" || values["RUNNER_IMAGE"] == DefaultPhoneImage {
		values["RUNNER_IMAGE"] = DefaultEmulatorImage
	}
	values["BASE_NAME"] = defaultIfEmpty(values["BASE_NAME"], DefaultBaseName)
	if backend == DefaultContainerBackend {
		values["CREDIMI_CONTAINER_MODE"] = "emulator"
		values["ANDROID_KEYS_DIR"] = defaultIfEmpty(values["ANDROID_KEYS_DIR"], DefaultValues()["ANDROID_KEYS_DIR"])
		values["HOST_AVD_HOME_PATH"] = defaultIfEmpty(values["HOST_AVD_HOME_PATH"], DefaultValues()["HOST_AVD_HOME_PATH"])
		values["HOST_AVD_GOLDEN_PATH"] = defaultIfEmpty(values["HOST_AVD_GOLDEN_PATH"], DefaultValues()["HOST_AVD_GOLDEN_PATH"])
		values["GOLDEN_PATH"] = defaultIfEmpty(values["GOLDEN_PATH"], DefaultGoldenPath)
		return
	}
	values["CREDIMI_CONTAINER_MODE"] = ""
	values["ANDROID_KEYS_DIR"] = ""
	values["HOST_AVD_HOME_PATH"] = ""
	values["HOST_AVD_GOLDEN_PATH"] = ""
	values["GOLDEN_PATH"] = defaultIfEmpty(values["GOLDEN_PATH"], DefaultValues()["HOST_AVD_GOLDEN_PATH"])
	clearAVDCTLSSHConfig(values)
	values["REDROID_DATA_DIR"] = ""
	values["REDROID_DATA_TAR"] = ""
}

func normalizeIOSSimulator(values Values) {
	values["CREDIMI_RUNNER_SERIAL"] = ""
	values["RUNNER_IMAGE"] = defaultIfEmpty(values["RUNNER_IMAGE"], DefaultPhoneImage)
	values["BASE_NAME"] = defaultIfEmpty(values["BASE_NAME"], DefaultBaseName)
	values["ANDROID_KEYS_DIR"] = ""
	values["HOST_AVD_HOME_PATH"] = ""
	values["HOST_AVD_GOLDEN_PATH"] = ""
	values["GOLDEN_PATH"] = ""
	values["REDROID_DATA_DIR"] = ""
	values["REDROID_DATA_TAR"] = ""
	clearAVDCTLSSHConfig(values)
	if values["CREDIMI_RUNNER_BACKEND"] == DefaultContainerBackend {
		values["CREDIMI_CONTAINER_MODE"] = "no_device"
		return
	}
	values["CREDIMI_CONTAINER_MODE"] = ""
}

func normalizeRedroid(values Values) {
	values["CREDIMI_RUNNER_DEVICE_MODE"] = "no_device"
	values["RUNNER_IMAGE"] = defaultIfEmpty(values["RUNNER_IMAGE"], DefaultPhoneImage)
	values["REDROID_DATA_DIR"] = defaultIfEmpty(values["REDROID_DATA_DIR"], DefaultRedroidDataDir)
	values["REDROID_DATA_TAR"] = defaultIfEmpty(values["REDROID_DATA_TAR"], DefaultRedroidDataTar)
	values["CREDIMI_CONTAINER_MODE"] = "no_device"
	if strings.TrimSpace(values["CREDIMI_RUNNER_WIFI_PORT"]) == "" {
		values["CREDIMI_RUNNER_WIFI_PORT"] = DefaultWiFiPort
	}
	if ip := strings.TrimSpace(values["CREDIMI_RUNNER_WIFI_IP"]); ip != "" {
		values["CREDIMI_RUNNER_SERIAL"] = ip + ":" + values["CREDIMI_RUNNER_WIFI_PORT"]
	}
}

func normalizeAndroidPhone(values Values) error {
	values["RUNNER_IMAGE"] = defaultIfEmpty(values["RUNNER_IMAGE"], DefaultPhoneImage)
	values["CREDIMI_RUNNER_DEVICE_MODE"] = defaultAndroidDeviceMode(values, "android_phone")

	if values["CREDIMI_RUNNER_DEVICE_MODE"] == "wifi" {
		values["CREDIMI_CONTAINER_MODE"] = "wifi"
		values["CREDIMI_RUNNER_WIFI_PORT"] = defaultIfEmpty(values["CREDIMI_RUNNER_WIFI_PORT"], DefaultWiFiPort)
		ip := strings.TrimSpace(values["CREDIMI_RUNNER_WIFI_IP"])
		if ip == "" {
			return fmt.Errorf("CREDIMI_RUNNER_WIFI_IP is required for android phone wi-fi mode")
		}
		values["CREDIMI_RUNNER_SERIAL"] = ip + ":" + values["CREDIMI_RUNNER_WIFI_PORT"]
	} else {
		values["CREDIMI_CONTAINER_MODE"] = "usb"
		values["CREDIMI_RUNNER_WIFI_IP"] = ""
		values["CREDIMI_RUNNER_WIFI_PORT"] = ""
	}

	values["BASE_NAME"] = ""
	values["GOLDEN_PATH"] = ""
	values["HOST_AVD_HOME_PATH"] = ""
	values["HOST_AVD_GOLDEN_PATH"] = ""
	values["REDROID_DATA_DIR"] = ""
	values["REDROID_DATA_TAR"] = ""
	clearAVDCTLSSHConfig(values)
	return nil
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func homeDir() (string, error) {
	return osUserHomeDir()
}
