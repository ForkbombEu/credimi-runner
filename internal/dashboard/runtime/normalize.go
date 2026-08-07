package runtime

import (
	"fmt"
	"runtime"
	"strings"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	runnerplacement "github.com/forkbombeu/credimi-runner/internal/runtime"
)

const (
	DefaultCredimiURL         = "https://credimi.io"
	DefaultTempDir            = "/tmp/credimi-runner-tmp"
	DefaultTemporalAddress    = "temporal.credimi.io:7233"
	DefaultOTLPEndpoint       = "https://otel-collector.credimi.io"
	DefaultOTELServiceName    = "credimi-runner"
	DefaultRunnerHost         = "127.0.0.1"
	DefaultRunnerPort         = "8050"
	DefaultAndroidRunnerImage = "ghcr.io/forkbombeu/credimi-runner:latest"
	DefaultAndroidPullPolicy  = "if-not-present"
	DefaultDashboardHost      = "0.0.0.0"
	DefaultDashboardPort      = "8051"
	DefaultRunnerCaddySite    = ":80"
	DefaultBaseName           = "credimi"
	DefaultGoldenPath         = "/avd-golden/credimi-golden"
	DefaultWiFiPort           = "5555"
	DefaultRedroidDataDir     = "/home/credimi/redroid-data"
	DefaultRedroidDataTar     = "/home/credimi/redroid-data.tar"
	DefaultHostAVDHome        = ".android/avd"
	DefaultHostAVDGolden      = "avd-golden"
	DefaultContainerBackend   = "container"
	DefaultHostBackend        = "host"
)

var KnownKeys = RunnerKeys

func DefaultValues() Values {
	values := Values{
		"CREDIMI_RUNNER_PUBLISHED":    "false",
		"ANDROID_RUNNER_IMAGE":        DefaultAndroidRunnerImage,
		"ANDROID_PULL_POLICY":         DefaultAndroidPullPolicy,
		"ANDROID_NETWORK":             "credimi-runner",
		"ANDROID_STATE_VOLUME":        "credimi-runner-state",
		"ANDROID_TOOL_CACHE_VOLUME":   "credimi-runner-tools",
		"ANDROID_SDK_VOLUME":          "credimi-runner-sdk",
		"CREDIMI_SERVICE_MODE":        "auto",
		"CREDIMI_TEMP_DIR":            DefaultTempDir,
		"CREDIMI_URL":                 DefaultCredimiURL,
		"DASHBOARD_HOST":              DefaultDashboardHost,
		"DASHBOARD_PORT":              DefaultDashboardPort,
		"OTEL_ENABLED":                "true",
		"OTEL_EXPORTER_OTLP_ENDPOINT": DefaultOTLPEndpoint,
		"OTEL_SERVICE_NAME":           DefaultOTELServiceName,
		"RUNNER_CADDY_SITE":           DefaultRunnerCaddySite,
		"RUNNER_HOST":                 DefaultRunnerHost,
		"RUNNER_PORT":                 DefaultRunnerPort,
		"TEMPORAL_ADDRESS":            DefaultTemporalAddress,
	}
	return values
}

func NormalizeValues(values Values, goos string) (Values, error) {
	if strings.TrimSpace(goos) == "" {
		goos = runtime.GOOS
	}
	if strings.TrimSpace(values["CREDIMI_DEVICE_COUNT"]) != "" {
		return normalizeIndexedValues(values, goos)
	}
	normalized := DefaultValues()
	for key, value := range values {
		normalized[key] = strings.TrimSpace(value)
	}

	backend, err := legacyBackend(normalized, goos)
	if err != nil {
		return nil, err
	}
	normalized["CREDIMI_RUNNER_BACKEND"] = string(backend)
	normalized["CREDIMI_SERVICE_MODE"] = normalizeServiceMode(normalized["CREDIMI_SERVICE_MODE"])
	normalizeRunnerIdentity(normalized)

	if !defaultYesNoChoice(normalized["OTEL_ENABLED"], true) {
		normalized["OTEL_EXPORTER_OTLP_ENDPOINT"] = ""
	}

	return normalized, nil
}

func normalizeIndexedValues(values Values, goos string) (Values, error) {
	normalized := DefaultValues()
	for key, value := range values {
		normalized[key] = strings.TrimSpace(value)
	}
	if _, err := ParseRuntimeConfig(normalized); err != nil {
		return nil, err
	}
	backend, err := legacyBackend(normalized, goos)
	if err != nil {
		return nil, err
	}
	normalized["CREDIMI_RUNNER_BACKEND"] = string(backend)
	return normalized, nil
}

func legacyBackend(values Values, goos string) (runnerplacement.Backend, error) {
	types := []runnerconfig.DeviceType{}
	if count := strings.TrimSpace(values["CREDIMI_DEVICE_COUNT"]); count != "" {
		for index := 1; ; index++ {
			key := fmt.Sprintf("CREDIMI_DEVICE_%d_TYPE", index)
			value := strings.TrimSpace(values[key])
			if value == "" {
				break
			}
			types = append(types, legacyDeviceType(value))
			if index >= atoiOrZero(count) {
				break
			}
		}
	} else if value := strings.TrimSpace(values["CREDIMI_RUNNER_TYPE"]); value != "" {
		types = append(types, legacyDeviceType(value))
	}
	backend, err := runnerplacement.SelectTypes(types, goos)
	if err != nil {
		return "", err
	}
	if backend == runnerplacement.Native {
		return DefaultHostBackend, nil
	}
	return backend, nil
}

func legacyDeviceType(value string) runnerconfig.DeviceType {
	if value == "android_phone" {
		return runnerconfig.DeviceAndroidPhysical
	}
	return runnerconfig.DeviceType(value)
}

func atoiOrZero(value string) int {
	var result int
	_, _ = fmt.Sscanf(value, "%d", &result)
	return result
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

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
