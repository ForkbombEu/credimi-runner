package runtime

import (
	"fmt"
	"hash/fnv"
	"os"
	goruntime "runtime"
	"sort"
	"strings"

	runnerplacement "github.com/forkbombeu/credimi-runner/internal/runtime"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
)

type RuntimePlan struct {
	ConfigFingerprint string
	Backend           string
	ServiceMode       string
	PublicMode        string
	RequiresDocker    bool
}

type ApplyClass string

const (
	ApplySavedOnly              ApplyClass = "saved_only"
	ApplyRuntimeReconcile       ApplyClass = "runtime_reconcile"
	ApplyRuntimeRestartRequired ApplyClass = "runtime_restart_required"
	ApplyServiceRestartRequired ApplyClass = "service_restart_required"
	ApplyCredimiUpdateRequired  ApplyClass = "credimi_update_required"
)

type ConfigDiff struct {
	ChangedKeys []string
	Classes     []ApplyClass
}

type FieldImpact struct {
	Runtime        bool
	RuntimeRestart bool
	CredimiUpdate  bool
	Secret         bool
}

var FieldImpacts = map[string]FieldImpact{
	"CLOUDFLARE_TUNNEL_TOKEN":     {Runtime: true, Secret: true},
	"CREDIMI_URL":                 {Runtime: true, CredimiUpdate: true},
	"CREDIMI_INTERNAL_ADMIN_KEY":  {Runtime: true, Secret: true},
	"CREDIMI_RUNNER_DESCRIPTION":  {CredimiUpdate: true},
	"CREDIMI_RUNNER_ID":           {Runtime: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_NAME":         {Runtime: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_ORGANIZATION": {Runtime: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_PUBLISHED":    {Runtime: true, CredimiUpdate: true},
	"CREDIMI_SERVICE_MODE":        {Runtime: true, CredimiUpdate: true},
	"CREDIMI_USER_API_KEY":        {Runtime: true, Secret: true},
	"DASHBOARD_TOKEN":             {Secret: true},
	"ANDROID_RUNNER_IMAGE":        {},
	"ANDROID_PULL_POLICY":         {},
	"ANDROID_NETWORK":             {},
	"ANDROID_STATE_VOLUME":        {},
	"ANDROID_TOOL_CACHE_VOLUME":   {},
	"ANDROID_SDK_VOLUME":          {},
	"ANDROID_ADB_KEYS_PATH":       {},
	"OTEL_EXPORTER_OTLP_ENDPOINT": {Runtime: true},
	"OTEL_ENABLED":                {Runtime: true},
	"OTEL_SERVICE_NAME":           {Runtime: true},
	"RUNNER_DOMAIN":               {CredimiUpdate: true},
	"RUNNER_HOST":                 {},
	"RUNNER_PORT":                 {CredimiUpdate: true},
	"RUNNER_PUBLIC_PORT":          {CredimiUpdate: true},
	"RUNNER_PUBLIC_URL":           {CredimiUpdate: true},
	"TEMPORAL_ADDRESS":            {Runtime: true},
	"CREDIMI_TEMP_DIR":            {Runtime: true},
	"ADB_SCREEN_RECORD_SIZE":      {Runtime: true},
}

func BuildRuntimePlan(configDir string, values Values) RuntimePlan {
	return BuildRuntimePlanForOS(configDir, values, goruntime.GOOS)
}

// BuildRuntimePlanForOS derives application placement from the host platform.
func BuildRuntimePlanForOS(configDir string, values Values, goos string) RuntimePlan {
	serviceMode := normalizeServiceMode(values["CREDIMI_SERVICE_MODE"])
	backend := backendForOS(goos)

	fingerprint := configFingerprint(configDir, values)
	plan := RuntimePlan{
		ConfigFingerprint: fingerprint,
		Backend:           backend,
		ServiceMode:       serviceMode,
		PublicMode:        serviceMode,
		RequiresDocker:    backend == DefaultContainerBackend,
	}
	if backend == DefaultContainerBackend && strings.EqualFold(values[BootstrapPhaseEnv], "true") {
		// Before config.toml exists the runner container only hosts the setup
		// wizard and discovery endpoints. Exposure services must not be started
		// from their form defaults before the user commits a service mode.
		plan.ServiceMode = "bootstrap"
		plan.PublicMode = "bootstrap"
		return plan
	}
	return plan
}

// ServiceRestartRequired compares desired typed configuration with the
// fingerprint exported by the persistent service container. Native execution
// has no applied container fingerprint and therefore never needs this guard.
func ServiceRestartRequired(values Values, configured bool) bool {
	applied := strings.TrimSpace(os.Getenv(servicemanager.AppliedServiceConfigFingerprintEnv))
	if applied == "" {
		return false
	}
	cfg, err := TypedConfigFromValues(values)
	if err != nil {
		return true
	}
	return applied != servicemanager.ServiceConfigFingerprint(cfg, configured)
}

func configFingerprint(configDir string, values Values) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(configDir))
	for _, key := range []string{"CREDIMI_RUNNER_ID", "CREDIMI_SERVICE_MODE", "RUNNER_HOST", "RUNNER_PORT"} {
		_, _ = hash.Write([]byte(key + "=" + values[key] + "\n"))
	}
	return fmt.Sprintf("%016x", hash.Sum64())
}

func RunnerAPIReachableFromHost(values Values, goos string) bool {
	if strings.TrimSpace(goos) == "" {
		goos = goruntime.GOOS
	}
	normalized, err := NormalizeValues(values, goos)
	if err != nil {
		return false
	}
	plan := BuildRuntimePlanForOS("", normalized, goos)
	if plan.Backend == DefaultNativeBackend {
		return true
	}
	return plan.Backend == DefaultContainerBackend
}

func backendForOS(goos string) string {
	backend, err := runnerplacement.Select(goos)
	if err != nil {
		return DefaultContainerBackend
	}
	if backend == runnerplacement.Native {
		return DefaultNativeBackend
	}
	return DefaultContainerBackend
}

func RunnerReadinessRequiredBeforeRegistration(values Values, goos string) bool {
	if strings.TrimSpace(goos) == "" {
		goos = goruntime.GOOS
	}
	normalized, err := NormalizeValues(values, goos)
	if err != nil {
		return false
	}
	// Container runners publish their API on the local host. Registration must
	// wait for that API instead of treating service start as readiness.
	return RunnerAPIReachableFromHost(normalized, goos)
}

// DeviceReadinessRequired reports whether startup may require an ADB device to
// exist now. Managed runtimes such as Redroid create their Android container
// per workflow, after the runner process has started.
func DeviceReadinessRequired(values Values, goos string) bool {
	if strings.TrimSpace(goos) == "" {
		goos = goruntime.GOOS
	}
	normalized, err := NormalizeValues(values, goos)
	if err != nil {
		return false
	}
	if inventory, err := ParseRuntimeConfig(normalized); err == nil {
		for _, device := range inventory.Devices {
			// Emulators, simulators, and Redroid are provisioned per execution,
			// after runner registration. Only a physical Android connection is a
			// prerequisite for the runner's initial readiness.
			if device.Enabled && device.Type == "android_phone" && device.Mode != "no_device" {
				return true
			}
		}
		return false
	}
	return false
}

func DiffValues(oldValues, newValues Values) ConfigDiff {
	return DiffValuesForOS(oldValues, newValues, goruntime.GOOS)
}

// DiffValuesForOS classifies persistent configuration changes. Service
// topology is derived from the typed service projection, not from individual
// compatibility keys or every device field.
func DiffValuesForOS(oldValues, newValues Values, goos string) ConfigDiff {
	if strings.TrimSpace(goos) == "" {
		goos = goruntime.GOOS
	}
	var diff ConfigDiff
	classSet := map[ApplyClass]struct{}{}
	for _, key := range SortedKnownKeys() {
		if oldValues[key] == newValues[key] {
			continue
		}
		diff.ChangedKeys = append(diff.ChangedKeys, key)
		impact := FieldImpacts[key]
		switch {
		case impact.RuntimeRestart:
			classSet[ApplyRuntimeRestartRequired] = struct{}{}
		case impact.Runtime:
			classSet[ApplyRuntimeReconcile] = struct{}{}
		}
		if impact.CredimiUpdate {
			classSet[ApplyCredimiUpdateRequired] = struct{}{}
		}
		if goos == "darwin" {
			switch key {
			case "RUNNER_HOST", "RUNNER_PORT":
				// Native execution owns the runner listener directly; there is no
				// Docker service topology to recreate on Darwin.
				classSet[ApplyRuntimeReconcile] = struct{}{}
			case "DASHBOARD_HOST", "DASHBOARD_PORT":
				// The Dashboard listener belongs to the persistent native service
				// process, not to a runtime generation.
				classSet[ApplyServiceRestartRequired] = struct{}{}
			}
		}
	}
	deviceKeys := make([]string, 0)
	for key := range oldValues {
		if strings.HasPrefix(key, "CREDIMI_DEVICE_") && !strings.HasSuffix(key, "_COUNT") && oldValues[key] != newValues[key] {
			deviceKeys = append(deviceKeys, key)
		}
	}
	for key := range newValues {
		if strings.HasPrefix(key, "CREDIMI_DEVICE_") && !strings.HasSuffix(key, "_COUNT") && oldValues[key] != newValues[key] {
			if !containsStringValue(deviceKeys, key) {
				deviceKeys = append(deviceKeys, key)
			}
		}
	}
	sort.Strings(deviceKeys)
	for _, key := range deviceKeys {
		diff.ChangedKeys = append(diff.ChangedKeys, key)
		classSet[ApplyRuntimeReconcile] = struct{}{}
		classSet[ApplyCredimiUpdateRequired] = struct{}{}
	}
	if goos != "darwin" {
		if oldCfg, oldErr := TypedConfigFromValues(oldValues); oldErr == nil {
			if newCfg, newErr := TypedConfigFromValues(newValues); newErr == nil {
				if servicemanager.ServiceConfigFingerprint(oldCfg, true) != servicemanager.ServiceConfigFingerprint(newCfg, true) {
					classSet[ApplyServiceRestartRequired] = struct{}{}
				}
			} else if serviceFallbackChanged(diff.ChangedKeys) {
				classSet[ApplyServiceRestartRequired] = struct{}{}
			}
		} else if serviceFallbackChanged(diff.ChangedKeys) {
			classSet[ApplyServiceRestartRequired] = struct{}{}
		}
	}
	if len(diff.ChangedKeys) == 0 {
		diff.Classes = []ApplyClass{ApplySavedOnly}
		return diff
	}
	if len(classSet) == 0 {
		diff.Classes = []ApplyClass{ApplySavedOnly}
		return diff
	}
	for _, class := range []ApplyClass{ApplyServiceRestartRequired, ApplyRuntimeRestartRequired, ApplyRuntimeReconcile, ApplyCredimiUpdateRequired} {
		if _, ok := classSet[class]; ok {
			diff.Classes = append(diff.Classes, class)
		}
	}
	return diff
}

func containsStringValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func serviceFallbackChanged(keys []string) bool {
	for _, key := range keys {
		if key == "ANDROID_RUNNER_IMAGE" || key == "ANDROID_PULL_POLICY" || key == "ANDROID_NETWORK" || key == "ANDROID_STATE_VOLUME" || key == "ANDROID_TOOL_CACHE_VOLUME" || key == "ANDROID_SDK_VOLUME" || key == "ANDROID_ADB_KEYS_PATH" || key == "RUNNER_HOST" || key == "RUNNER_PORT" || key == "DASHBOARD_HOST" || key == "DASHBOARD_PORT" || key == "CREDIMI_SERVICE_MODE" || strings.HasPrefix(key, "CREDIMI_DEVICE_") {
			return true
		}
	}
	return false
}
