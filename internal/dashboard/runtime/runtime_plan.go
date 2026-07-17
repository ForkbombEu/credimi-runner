package runtime

import (
	goruntime "runtime"
	"strings"
)

type RuntimePlan struct {
	ConfigDir        string
	EnvPath          string
	ComposePath      string
	Backend          string
	RunnerType       string
	ContainerMode    string
	ServiceMode      string
	ComposeServices  []string
	PublicMode       string
	RequiresDocker   bool
	RequiresHostRun  bool
	ExpectedServices []PlannedService
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
	ApplySavedOnly             ApplyClass = "saved_only"
	ApplyRestartRequired       ApplyClass = "restart_required"
	ApplyComposeRecreate       ApplyClass = "compose_recreate_required"
	ApplyCredimiUpdateRequired ApplyClass = "credimi_update_required"
)

type ConfigDiff struct {
	ChangedKeys []string
	Classes     []ApplyClass
}

type FieldImpact struct {
	Restart       bool
	Recreate      bool
	CredimiUpdate bool
	Secret        bool
}

var FieldImpacts = map[string]FieldImpact{
	"CLOUDFLARE_TUNNEL_TOKEN":     {Restart: true, Recreate: true, Secret: true},
	"CREDIMI_INTERNAL_ADMIN_KEY":  {Restart: true, Secret: true},
	"CREDIMI_RUNNER_BACKEND":      {Restart: true, Recreate: true},
	"CREDIMI_RUNNER_DESCRIPTION":  {CredimiUpdate: true},
	"CREDIMI_RUNNER_DEVICE_MODE":  {Restart: true},
	"CREDIMI_RUNNER_ID":           {Restart: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_NAME":         {Restart: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_ORGANIZATION": {Restart: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_PUBLISHED":    {CredimiUpdate: true},
	"CREDIMI_RUNNER_SERIAL":       {Restart: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_TYPE":         {Restart: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_WIFI_IP":      {Restart: true},
	"CREDIMI_RUNNER_WIFI_PORT":    {Restart: true},
	"CREDIMI_SERVICE_MODE":        {Recreate: true, CredimiUpdate: true},
	"CREDIMI_USER_API_KEY":        {Restart: true, Secret: true},
	"OTEL_EXPORTER_OTLP_ENDPOINT": {Restart: true},
	"OTEL_SERVICE_NAME":           {Restart: true},
	"RUNNER_CADDY_SITE":           {Recreate: true},
	"RUNNER_DOMAIN":               {CredimiUpdate: true},
	"RUNNER_HOST":                 {Recreate: true},
	"RUNNER_IMAGE":                {Restart: true, Recreate: true},
	"RUNNER_IMAGE_PULL_POLICY":    {Recreate: true},
	"RUNNER_PORT":                 {Recreate: true, CredimiUpdate: true},
	"RUNNER_PUBLIC_PORT":          {CredimiUpdate: true},
	"RUNNER_PUBLIC_URL":           {CredimiUpdate: true},
	"TEMPORAL_ADDRESS":            {Restart: true},
	"ANDROID_KEYS_DIR":            {Restart: true},
	"BASE_NAME":                   {Restart: true},
	"GOLDEN_PATH":                 {Restart: true},
	"HOST_AVD_GOLDEN_PATH":        {Restart: true, Recreate: true},
	"HOST_AVD_HOME_PATH":          {Restart: true, Recreate: true},
	"REDROID_DATA_DIR":            {Restart: true, Recreate: true},
	"REDROID_DATA_TAR":            {Restart: true, Recreate: true},
}

func BuildRuntimePlan(configDir string, values Values) RuntimePlan {
	serviceMode := normalizeServiceMode(values["CREDIMI_SERVICE_MODE"])
	backend := defaultIfEmpty(values["CREDIMI_RUNNER_BACKEND"], DefaultContainerBackend)
	runnerType := defaultIfEmpty(values["CREDIMI_RUNNER_TYPE"], "android_phone")
	containerMode := strings.TrimSpace(values["CREDIMI_CONTAINER_MODE"])

	plan := RuntimePlan{
		ConfigDir:       configDir,
		EnvPath:         filepathJoin(configDir, ".env"),
		ComposePath:     filepathJoin(configDir, "docker-compose.yaml"),
		Backend:         backend,
		RunnerType:      runnerType,
		ContainerMode:   containerMode,
		ServiceMode:     serviceMode,
		PublicMode:      serviceMode,
		RequiresDocker:  backend == DefaultContainerBackend || serviceMode != "manual",
		RequiresHostRun: backend == DefaultHostBackend,
	}

	switch {
	case backend == DefaultHostBackend && serviceMode == "manual":
		plan.ComposeServices = nil
	case backend == DefaultHostBackend && serviceMode == "cloudflare-managed":
		plan.ComposeServices = []string{"runner_host", "caddy", "tunnel_named"}
	case backend == DefaultHostBackend:
		plan.ComposeServices = []string{"runner_host", "caddy", "tunnel"}
	case serviceMode == "manual":
		plan.ComposeServices = []string{"runner"}
	case serviceMode == "cloudflare-managed":
		plan.ComposeServices = []string{"runner", "caddy", "tunnel_named"}
	default:
		plan.ComposeServices = []string{"runner", "caddy", "tunnel"}
	}

	plan.ExpectedServices = expectedServices(plan)

	return plan
}

func expectedServices(plan RuntimePlan) []PlannedService {
	services := make([]PlannedService, 0, len(plan.ComposeServices)+2)
	for _, service := range plan.ComposeServices {
		switch service {
		case "runner":
			services = append(services, PlannedService{ID: "runner", Name: "runner", Role: "credimi-runner serve", Critical: true, Kind: "compose"})
		case "runner_host":
			services = append(services, PlannedService{ID: "runner_host", Name: "runner_host", Role: "host runner adapter", Critical: true, Kind: "compose"})
		case "caddy":
			services = append(services, PlannedService{ID: "caddy", Name: "caddy", Role: "reverse proxy", Critical: plan.ServiceMode != "manual", Kind: "compose"})
		case "tunnel":
			services = append(services, PlannedService{ID: "tunnel", Name: "tunnel", Role: "quick tunnel", Critical: true, Kind: "compose"})
		case "tunnel_named":
			services = append(services, PlannedService{ID: "tunnel_named", Name: "tunnel_named", Role: "managed tunnel", Critical: true, Kind: "compose"})
		}
	}
	if plan.RequiresHostRun {
		services = append(services, PlannedService{ID: "runner_host_process", Name: "runner host", Role: "local runner process", Critical: true, Kind: "process"})
	}
	if strings.TrimSpace(plan.ServiceMode) != "" {
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
	plan := BuildRuntimePlan("", normalized)
	if plan.Backend == DefaultHostBackend {
		return true
	}
	if plan.Backend != DefaultContainerBackend || goos != "linux" {
		return false
	}
	return plan.ServiceMode == "manual" ||
		(plan.ServiceMode == "auto" && (plan.ContainerMode == "usb" || plan.ContainerMode == "wifi"))
}

func RunnerReadinessRequiredBeforeRegistration(values Values, goos string) bool {
	if strings.TrimSpace(goos) == "" {
		goos = goruntime.GOOS
	}
	normalized, err := NormalizeValues(values, goos)
	if err != nil {
		return false
	}
	plan := BuildRuntimePlan("", normalized)
	if plan.Backend == DefaultHostBackend && plan.ServiceMode == "manual" {
		return true
	}
	// Linux phone containers use host networking so the controller can verify
	// the runner API on the host. Registration must wait for that API instead
	// of treating `docker compose up -d` as readiness.
	return RunnerAPIReachableFromHost(normalized, goos)
}

func DiffValues(oldValues, newValues Values) ConfigDiff {
	var diff ConfigDiff
	classSet := map[ApplyClass]struct{}{}
	for _, key := range SortedKnownKeys() {
		if oldValues[key] == newValues[key] {
			continue
		}
		diff.ChangedKeys = append(diff.ChangedKeys, key)
		impact := FieldImpacts[key]
		switch {
		case impact.Recreate:
			classSet[ApplyComposeRecreate] = struct{}{}
		case impact.Restart:
			classSet[ApplyRestartRequired] = struct{}{}
		}
		if impact.CredimiUpdate {
			classSet[ApplyCredimiUpdateRequired] = struct{}{}
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
	for _, class := range []ApplyClass{ApplyRestartRequired, ApplyComposeRecreate, ApplyCredimiUpdateRequired} {
		if _, ok := classSet[class]; ok {
			diff.Classes = append(diff.Classes, class)
		}
	}
	return diff
}
