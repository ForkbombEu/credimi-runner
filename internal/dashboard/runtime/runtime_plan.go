package runtime

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
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
	RequiresHostRun   bool
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
	"CREDIMI_RUNNER_ID":           {Restart: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_NAME":         {Restart: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_ORGANIZATION": {Restart: true, CredimiUpdate: true},
	"CREDIMI_RUNNER_PUBLISHED":    {CredimiUpdate: true},
	"CREDIMI_SERVICE_MODE":        {Recreate: true, CredimiUpdate: true},
	"CREDIMI_USER_API_KEY":        {Restart: true, Secret: true},
	"OTEL_EXPORTER_OTLP_ENDPOINT": {Restart: true},
	"OTEL_SERVICE_NAME":           {Restart: true},
	"RUNNER_CADDY_SITE":           {Recreate: true},
	"RUNNER_DOMAIN":               {CredimiUpdate: true},
	"RUNNER_HOST":                 {Recreate: true},
	"RUNNER_PORT":                 {Recreate: true, CredimiUpdate: true},
	"RUNNER_PUBLIC_PORT":          {CredimiUpdate: true},
	"RUNNER_PUBLIC_URL":           {CredimiUpdate: true},
	"TEMPORAL_ADDRESS":            {Restart: true},
}

func BuildRuntimePlan(configDir string, values Values) RuntimePlan {
	serviceMode := normalizeServiceMode(values["CREDIMI_SERVICE_MODE"])
	backend := defaultIfEmpty(values["CREDIMI_RUNNER_BACKEND"], DefaultContainerBackend)
	if inventory, err := ParseRuntimeConfig(values); err == nil && len(inventory.Devices) > 0 {
		backend = defaultIfEmpty(inventory.Devices[0].Values["BACKEND"], backend)
	}

	canonicalDir, err := filepath.Abs(configDir)
	if err != nil {
		canonicalDir = configDir
	}
	fingerprint := configFingerprint(canonicalDir, values)
	plan := RuntimePlan{
		ConfigDir:         configDir,
		EnvPath:           filepathJoin(configDir, ".env"),
		ComposePath:       filepathJoin(configDir, "docker-compose.yaml"),
		ComposeProject:    composeProjectName(canonicalDir),
		ConfigFingerprint: fingerprint,
		Backend:           backend,
		ServiceMode:       serviceMode,
		PublicMode:        serviceMode,
		RequiresDocker:    backend == DefaultContainerBackend || serviceMode != "manual",
		RequiresHostRun:   backend == DefaultHostBackend,
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

func composeProjectName(configDir string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(configDir))
	return fmt.Sprintf("credimi-runner-%d-%08x", os.Getuid(), hash.Sum32())
}

func configFingerprint(configDir string, values Values) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(configDir))
	for _, key := range []string{"CREDIMI_RUNNER_ID", "CREDIMI_RUNNER_BACKEND", "CREDIMI_SERVICE_MODE", "RUNNER_HOST", "RUNNER_PORT"} {
		_, _ = hash.Write([]byte(key + "=" + values[key] + "\n"))
	}
	return fmt.Sprintf("%016x", hash.Sum64())
}

func composeArgs(plan RuntimePlan, command ...string) []string {
	args := []string{"compose", "--project-name", plan.ComposeProject, "--env-file", plan.EnvPath, "-f", plan.ComposePath}
	return append(args, command...)
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
	return plan.Backend == DefaultContainerBackend
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
			if device.Enabled && device.Mode != "no_device" {
				return true
			}
		}
		return false
	}
	return false
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
	for key, oldValue := range oldValues {
		if !strings.HasPrefix(key, "CREDIMI_DEVICE_") || strings.HasSuffix(key, "_COUNT") {
			continue
		}
		if oldValue == newValues[key] {
			continue
		}
		diff.ChangedKeys = append(diff.ChangedKeys, key)
		classSet[ApplyRestartRequired] = struct{}{}
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
