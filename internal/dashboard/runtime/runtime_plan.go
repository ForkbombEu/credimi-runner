package runtime

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"

	runnerplacement "github.com/forkbombeu/credimi-runner/internal/runtime"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
)

type RuntimePlan struct {
	ConfigDir         string
	EnvPath           string
	ComposePath       string
	ComposeProject    string
	ConfigFingerprint string
	Backend           string
	ServiceMode       string
	ComposeServices   []string
	PublicMode        string
	RequiresDocker    bool
	ExpectedServices  []PlannedService
}

type PlannedService struct {
	ID       string
	Name     string
	Role     string
	Critical bool
	Kind     string
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
	Service        bool
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
	"ANDROID_RUNNER_IMAGE":        {Service: true},
	"ANDROID_PULL_POLICY":         {Service: true},
	"ANDROID_NETWORK":             {Service: true},
	"ANDROID_STATE_VOLUME":        {Service: true},
	"ANDROID_TOOL_CACHE_VOLUME":   {Service: true},
	"ANDROID_SDK_VOLUME":          {Service: true},
	"ANDROID_ADB_KEYS_PATH":       {Service: true},
	"OTEL_EXPORTER_OTLP_ENDPOINT": {Runtime: true},
	"OTEL_ENABLED":                {Runtime: true},
	"OTEL_SERVICE_NAME":           {Runtime: true},
	"RUNNER_DOMAIN":               {CredimiUpdate: true},
	"RUNNER_HOST":                 {Service: true},
	"RUNNER_PORT":                 {Service: true, CredimiUpdate: true},
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

	canonicalDir, err := filepath.Abs(configDir)
	if err != nil {
		canonicalDir = configDir
	}
	fingerprint := configFingerprint(canonicalDir, values)
	plan := RuntimePlan{
		ConfigDir:         configDir,
		EnvPath:           filepathJoin(configDir, "config.toml"),
		ComposePath:       filepathJoin(configDir, "service-compose.yaml"),
		ComposeProject:    composeProjectName(canonicalDir),
		ConfigFingerprint: fingerprint,
		Backend:           backend,
		ServiceMode:       serviceMode,
		PublicMode:        serviceMode,
		RequiresDocker:    backend == DefaultContainerBackend || serviceMode != "manual",
	}
	if backend == DefaultContainerBackend && strings.EqualFold(values[BootstrapPhaseEnv], "true") {
		// Before config.toml exists the runner container only hosts the setup
		// wizard and discovery endpoints. Exposure services must not be started
		// from their form defaults before the user commits a service mode.
		plan.ServiceMode = "bootstrap"
		plan.PublicMode = "bootstrap"
		plan.ComposeServices = []string{"runner"}
		plan.ExpectedServices = expectedServices(plan)
		return plan
	}

	switch {
	case backend == DefaultNativeBackend && serviceMode == "manual":
		plan.ComposeServices = nil
	case backend == DefaultNativeBackend:
		plan.ComposeServices = nil
	case serviceMode == "manual":
		plan.ComposeServices = []string{"runner"}
	default:
		plan.ComposeServices = []string{"runner"}
	}

	plan.ExpectedServices = expectedServices(plan)

	return plan
}

func composeProjectName(configDir string) string {
	uid := os.Getuid()
	if configured, err := strconv.Atoi(strings.TrimSpace(os.Getenv(ConfigOwnerUIDEnv))); err == nil && configured >= 0 {
		uid = configured
	}
	return servicemanager.ProjectName(configDir, uid)
}

func configFingerprint(configDir string, values Values) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(configDir))
	for _, key := range []string{"CREDIMI_RUNNER_ID", "CREDIMI_SERVICE_MODE", "RUNNER_HOST", "RUNNER_PORT"} {
		_, _ = hash.Write([]byte(key + "=" + values[key] + "\n"))
	}
	return fmt.Sprintf("%016x", hash.Sum64())
}

func expectedServices(plan RuntimePlan) []PlannedService {
	services := make([]PlannedService, 0, len(plan.ComposeServices)+2)
	for _, service := range plan.ComposeServices {
		switch service {
		case "runner":
			services = append(services, PlannedService{ID: "runner", Name: "runner", Role: "credimi-runner serve", Critical: true, Kind: "compose"})
		}
	}
	if strings.TrimSpace(plan.ServiceMode) != "" && plan.ServiceMode != "bootstrap" {
		services = append(services, PlannedService{ID: "temporal", Name: "temporal", Role: "workflow backend", Critical: false, Kind: "external"})
	}
	return services
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
	// wait for that API instead of treating `docker compose up -d` as readiness.
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

// DiffValuesForOS classifies persistent configuration changes. Device edits
// remain registration updates; placement is fixed by the host platform.
func DiffValuesForOS(oldValues, newValues Values, _ string) ConfigDiff {
	var diff ConfigDiff
	classSet := map[ApplyClass]struct{}{}
	for _, key := range SortedKnownKeys() {
		if oldValues[key] == newValues[key] {
			continue
		}
		diff.ChangedKeys = append(diff.ChangedKeys, key)
		impact := FieldImpacts[key]
		switch {
		case impact.Service:
			classSet[ApplyServiceRestartRequired] = struct{}{}
		case impact.RuntimeRestart:
			classSet[ApplyRuntimeRestartRequired] = struct{}{}
		case impact.Runtime:
			classSet[ApplyRuntimeReconcile] = struct{}{}
		}
		if impact.CredimiUpdate {
			classSet[ApplyCredimiUpdateRequired] = struct{}{}
		}
	}
	for key, oldValue := range oldValues {
		if !strings.HasPrefix(key, "CREDIMI_DEVICE_") || strings.HasSuffix(key, "_COUNT") {
			continue
		}
		if oldValue == newValues[key] {
			continue
		}
		diff.ChangedKeys = append(diff.ChangedKeys, key)
		// Device registration is dynamic. The GoA process and its existing
		// workers remain alive; the Credimi registration path updates only the
		// changed inventory.
		classSet[ApplyRuntimeReconcile] = struct{}{}
		classSet[ApplyCredimiUpdateRequired] = struct{}{}
		classSet[ApplyServiceRestartRequired] = struct{}{}
	}
	for key := range newValues {
		if !strings.HasPrefix(key, "CREDIMI_DEVICE_") || strings.HasSuffix(key, "_COUNT") {
			continue
		}
		if _, existed := oldValues[key]; existed {
			continue
		}
		// A newly added device has no old indexed keys to visit in the loop
		// above. It has the same runtime impact as an updated device.
		diff.ChangedKeys = append(diff.ChangedKeys, key)
		classSet[ApplyCredimiUpdateRequired] = struct{}{}
		classSet[ApplyRuntimeReconcile] = struct{}{}
		classSet[ApplyServiceRestartRequired] = struct{}{}
	}
	if len(diff.ChangedKeys) == 0 {
		diff.Classes = []ApplyClass{ApplySavedOnly}
		return diff
	}
	if len(classSet) == 0 {
		diff.Classes = []ApplyClass{ApplySavedOnly}
		return diff
	}
	for _, class := range []ApplyClass{ApplyRuntimeReconcile, ApplyRuntimeRestartRequired, ApplyServiceRestartRequired, ApplyCredimiUpdateRequired} {
		if _, ok := classSet[class]; ok {
			diff.Classes = append(diff.Classes, class)
		}
	}
	return diff
}

// SharedRunnerImage exposes the image used by the service manager for the
// single runner service. Rendering remains exclusively in servicemanager.
func SharedRunnerImage(values Values, _ string) (image, pullPolicy string, err error) {
	return defaultIfEmpty(values["ANDROID_RUNNER_IMAGE"], DefaultAndroidRunnerImage), defaultIfEmpty(values["ANDROID_PULL_POLICY"], DefaultAndroidPullPolicy), nil
}

// ComposeEnvironment supplies interpolation variables to the read-only
// service observation path. It does not render or start a service.
func ComposeEnvironment(values Values, plan RuntimePlan, goos string) []string {
	normalized, err := NormalizeValues(values, goos)
	if err != nil {
		normalized = cloneValues(values)
	}
	overrides := []string{
		"RUNNER_PORT=" + defaultIfEmpty(normalized["RUNNER_PORT"], DefaultRunnerPort),
		"DASHBOARD_PORT=" + defaultIfEmpty(normalized["DASHBOARD_PORT"], DefaultDashboardPort),
		"CREDIMI_COMPOSE_PROJECT=" + defaultIfEmpty(plan.ComposeProject, "credimi-runner"),
		"CREDIMI_CONFIG_FINGERPRINT=" + defaultIfEmpty(plan.ConfigFingerprint, "unknown"),
		"COMPOSE_PROGRESS=plain",
		"DOCKER_CLI_HINTS=false",
	}
	for _, key := range []string{
		BootstrapImageEnv, BootstrapPullPolicyEnv, ConfigOwnerUIDEnv,
		ConfigOwnerGIDEnv, HostHomeEnv, HostAndroidDirEnv, HostGoldenRootEnv,
		ContainerAndroidDirEnv, ContainerAVDHomeEnv, ContainerGoldenRootEnv,
		BootstrapHostNetworkEnv, BootstrapPhaseEnv,
	} {
		overrides = append(overrides, key+"="+normalized[key])
	}
	return replaceEnvironment(os.Environ(), overrides...)
}

func replaceEnvironment(environment []string, overrides ...string) []string {
	managed := make(map[string]struct{}, len(overrides))
	for _, override := range overrides {
		key, _, _ := strings.Cut(override, "=")
		managed[key] = struct{}{}
	}
	result := make([]string, 0, len(environment)+len(overrides))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := managed[key]; !ok {
			result = append(result, entry)
		}
	}
	return append(result, overrides...)
}
