package runtime

import (
	"os"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
)

func TestComposeProjectNameUsesHostIdentityAcrossContainerPaths(t *testing.T) {
	hostPath := "/home/alice/.config/credimi-runner"
	project := servicemanager.ProjectName(hostPath, 1000)
	t.Setenv(servicemanager.ComposeProjectEnv, project)
	t.Setenv(ConfigOwnerUIDEnv, "1000")
	if got := composeProjectName("/etc/credimi-runner"); got != project {
		t.Fatalf("container project = %q, want host project %q", got, project)
	}
	if got := os.Getenv(servicemanager.ComposeProjectEnv); got != project {
		t.Fatalf("project environment = %q, want %q", got, project)
	}
}

func TestRuntimePlanUsesOnlyServiceComposeRunner(t *testing.T) {
	for _, tc := range []struct {
		name string
		goos string
		mode string
		want int
	}{
		{name: "linux", goos: "linux", mode: "auto", want: 1},
		{name: "manual", goos: "linux", mode: "manual", want: 1},
		{name: "darwin", goos: "darwin", mode: "manual", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildRuntimePlanForOS(t.TempDir(), Values{"CREDIMI_SERVICE_MODE": tc.mode}, tc.goos)
			if !strings.HasSuffix(plan.ComposePath, "service-compose.yaml") {
				t.Fatalf("compose path = %q", plan.ComposePath)
			}
			if len(plan.ComposeServices) != tc.want {
				t.Fatalf("compose services = %#v", plan.ComposeServices)
			}
			for _, service := range plan.ExpectedServices {
				if service.ID != "runner" && service.Kind == "compose" {
					t.Fatalf("unexpected compose service = %#v", service)
				}
			}
		})
	}
}

func TestRuntimePlanClassifiesPersistentChanges(t *testing.T) {
	checks := []struct {
		name  string
		key   string
		value string
		class ApplyClass
	}{
		{"saved-only token", "DASHBOARD_TOKEN", "token", ApplySavedOnly},
		{"runtime", "TEMPORAL_ADDRESS", "temporal:7233", ApplyRuntimeReconcile},
		{"service", "ANDROID_RUNNER_IMAGE", "runner:test", ApplyServiceRestartRequired},
		{"Credimi", "RUNNER_PUBLIC_URL", "https://runner.example", ApplyCredimiUpdateRequired},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			oldValues := Values{}
			newValues := Values{check.key: check.value}
			diff := DiffValuesForOS(oldValues, newValues, "linux")
			if len(diff.Classes) != 1 || diff.Classes[0] != check.class {
				t.Fatalf("diff = %#v", diff)
			}
		})
	}
	deviceDiff := DiffValuesForOS(Values{}, Values{"CREDIMI_DEVICE_1_ID": "org/runner/device"}, "linux")
	for _, class := range []ApplyClass{ApplyRuntimeReconcile, ApplyCredimiUpdateRequired} {
		if !hasClass(deviceDiff, class) {
			t.Fatalf("device diff missing %q: %#v", class, deviceDiff)
		}
	}
	if hasClass(deviceDiff, ApplyServiceRestartRequired) {
		t.Fatal("device identity change unexpectedly requires service restart")
	}
}

func TestRuntimePlanEnvironmentAndImageHelpers(t *testing.T) {
	values := Values{"ANDROID_RUNNER_IMAGE": "runner:test", "ANDROID_PULL_POLICY": "never", "RUNNER_PORT": "18050"}
	image, policy, err := SharedRunnerImage(values, "linux")
	if err != nil || image != "runner:test" || policy != "never" {
		t.Fatalf("image helper = %q/%q/%v", image, policy, err)
	}
	plan := BuildRuntimePlanForOS("/tmp/config", values, "linux")
	environment := ComposeEnvironment(values, plan, "linux")
	if !containsEnvironment(environment, "RUNNER_PORT=18050") || !containsEnvironment(environment, "COMPOSE_PROGRESS=plain") {
		t.Fatalf("environment = %#v", environment)
	}
	if !RunnerAPIReachableFromHost(values, "linux") || !RunnerReadinessRequiredBeforeRegistration(values, "linux") {
		t.Fatal("linux runner should be host reachable")
	}
	if !DeviceReadinessRequired(Values{"CREDIMI_RUNNER_ID": "org/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "org/runner/device", "CREDIMI_DEVICE_1_TYPE": "android_phone", "CREDIMI_DEVICE_1_MODE": "usb"}, "linux") {
		t.Fatal("physical Android device should require readiness")
	}
	if BuildRuntimePlan("/tmp/config", values).ComposePath == "" || len(DiffValues(Values{}, Values{"DASHBOARD_TOKEN": "token"}).Classes) != 1 {
		t.Fatal("wrapper helpers returned empty results")
	}
	if image, policy, err := SharedRunnerImage(Values{}, "linux"); err != nil || image == "" || policy == "" {
		t.Fatalf("default image helper = %q/%q/%v", image, policy, err)
	}
	if RunnerAPIReachableFromHost(Values{"CREDIMI_RUNNER_TYPE": "ios_simulator"}, "linux") {
		t.Fatal("unsupported device type reported as reachable")
	}
	if DeviceReadinessRequired(Values{"CREDIMI_DEVICE_COUNT": "bad"}, "linux") {
		t.Fatal("invalid inventory reported as ready")
	}
	if DeviceReadinessRequired(Values{"CREDIMI_RUNNER_ID": "org/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "org/runner/device", "CREDIMI_DEVICE_1_TYPE": "android_phone", "CREDIMI_DEVICE_1_MODE": "no_device", "CREDIMI_DEVICE_1_ENABLED": "false"}, "linux") {
		t.Fatal("disabled device reported as requiring readiness")
	}
}

func TestRuntimePlanBootstrapAndEnvironmentFallbacks(t *testing.T) {
	plan := BuildRuntimePlanForOS(t.TempDir(), Values{BootstrapPhaseEnv: "true"}, "linux")
	if plan.ServiceMode != "bootstrap" || len(plan.ComposeServices) != 1 || len(plan.ExpectedServices) != 1 {
		t.Fatalf("bootstrap plan = %+v", plan)
	}
	if !RunnerAPIReachableFromHost(Values{}, "darwin") || !RunnerReadinessRequiredBeforeRegistration(Values{}, "darwin") {
		t.Fatal("native runner should be reachable")
	}
	invalid := ComposeEnvironment(Values{"RUNNER_PORT": "bad"}, RuntimePlan{}, "linux")
	if !containsEnvironment(invalid, "CREDIMI_COMPOSE_PROJECT=credimi-runner") || !containsEnvironment(invalid, "CREDIMI_CONFIG_FINGERPRINT=unknown") {
		t.Fatalf("fallback environment = %#v", invalid)
	}
	replaced := replaceEnvironment([]string{"RUNNER_PORT=old", "OTHER=value"}, "RUNNER_PORT=new")
	if !containsEnvironment(replaced, "RUNNER_PORT=new") || !containsEnvironment(replaced, "OTHER=value") {
		t.Fatalf("replaced environment = %#v", replaced)
	}
}

func TestRuntimePlanDeviceDiffAndNoop(t *testing.T) {
	if diff := DiffValuesForOS(Values{"DASHBOARD_TOKEN": "same"}, Values{"DASHBOARD_TOKEN": "same"}, "linux"); len(diff.ChangedKeys) != 0 || len(diff.Classes) != 1 || diff.Classes[0] != ApplySavedOnly {
		t.Fatalf("no-op diff = %+v", diff)
	}
	updated := DiffValuesForOS(Values{"CREDIMI_DEVICE_1_ID": "old"}, Values{"CREDIMI_DEVICE_1_ID": "new"}, "linux")
	if len(updated.ChangedKeys) != 1 || !hasClass(updated, ApplyRuntimeReconcile) {
		t.Fatalf("updated device diff = %+v", updated)
	}
	added := DiffValuesForOS(Values{}, Values{"CREDIMI_DEVICE_2_NAME": "new"}, "linux")
	if len(added.ChangedKeys) != 1 || hasClass(added, ApplyServiceRestartRequired) {
		t.Fatalf("added device diff = %+v", added)
	}
}

func TestServiceConfigFingerprintProjectsOnlyServiceTopology(t *testing.T) {
	base := runnerconfig.Bootstrap()
	base.Server.APIListen = "127.0.0.1:8050"
	base.Server.DashboardListen = "127.0.0.1:8051"
	base.Exposure.Mode = "quick_tunnel"
	base.Devices = []runnerconfig.DeviceConfig{{
		ID: "org/runner/phone", Name: "Phone", Description: "old", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "A"},
	}}
	fingerprint := func(cfg runnerconfig.Config, configured bool) string {
		return servicemanager.ServiceConfigFingerprint(cfg, configured)
	}
	unchanged := []struct {
		name   string
		mutate func(*runnerconfig.Config)
	}{
		{"Temporal address", func(cfg *runnerconfig.Config) { cfg.Temporal.Address = "other:7233" }},
		{"runner description", func(cfg *runnerconfig.Config) { cfg.Runner.Description = "new" }},
		{"device description", func(cfg *runnerconfig.Config) { cfg.Devices[0].Description = "new" }},
		{"USB serial", func(cfg *runnerconfig.Config) { cfg.Devices[0].AndroidPhysical.Serial = "B" }},
		{"quick to named", func(cfg *runnerconfig.Config) { cfg.Exposure.Mode = "named_tunnel" }},
	}
	baseFingerprint := fingerprint(base, true)
	for _, tc := range unchanged {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			candidate.Devices = append([]runnerconfig.DeviceConfig(nil), base.Devices...)
			physical := *base.Devices[0].AndroidPhysical
			candidate.Devices[0].AndroidPhysical = &physical
			tc.mutate(&candidate)
			if got := fingerprint(candidate, true); got != baseFingerprint {
				t.Fatalf("fingerprint changed: %q != %q", got, baseFingerprint)
			}
		})
	}
	changed := []struct {
		name   string
		mutate func(*runnerconfig.Config)
	}{
		{"manual exposure", func(cfg *runnerconfig.Config) { cfg.Exposure.Mode = "manual" }},
		{"API port", func(cfg *runnerconfig.Config) { cfg.Server.APIListen = "127.0.0.1:8052" }},
		{"Dashboard port", func(cfg *runnerconfig.Config) { cfg.Server.DashboardListen = "127.0.0.1:8052" }},
		{"runner image", func(cfg *runnerconfig.Config) { cfg.Android.RunnerImage = "runner:other" }},
		{"network", func(cfg *runnerconfig.Config) { cfg.Android.Network = "other-network" }},
		{"Wi-Fi to USB", func(cfg *runnerconfig.Config) { cfg.Devices[0].AndroidPhysical.Transport = "wifi" }},
		{"emulator enabled", func(cfg *runnerconfig.Config) {
			cfg.Devices = append(cfg.Devices, runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidEmulator, Enabled: true})
		}},
	}
	for _, tc := range changed {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			candidate.Devices = append([]runnerconfig.DeviceConfig(nil), base.Devices...)
			physical := *base.Devices[0].AndroidPhysical
			candidate.Devices[0].AndroidPhysical = &physical
			tc.mutate(&candidate)
			if got := fingerprint(candidate, true); got == baseFingerprint {
				t.Fatal("topology change did not change fingerprint")
			}
		})
	}
	redroid := base
	redroid.Devices = []runnerconfig.DeviceConfig{{Type: runnerconfig.DeviceRedroid, Enabled: true, Redroid: &runnerconfig.RedroidConfig{AVDCTLSSHKnownHostsPath: "/home/alice/.ssh/known_hosts"}}}
	otherRedroid := redroid
	otherRedroid.Devices = []runnerconfig.DeviceConfig{{Type: runnerconfig.DeviceRedroid, Enabled: true, Redroid: &runnerconfig.RedroidConfig{AVDCTLSSHKnownHostsPath: "/home/alice/.ssh/other_known_hosts"}}}
	if fingerprint(redroid, true) == fingerprint(otherRedroid, true) {
		t.Fatal("Redroid known-hosts change did not change fingerprint")
	}
	if fingerprint(base, false) == baseFingerprint {
		t.Fatal("bootstrap/configured state did not change fingerprint")
	}
}

func TestServiceRestartRequiredUsesAppliedFingerprint(t *testing.T) {
	cfg := runnerconfig.Bootstrap()
	values := ValuesFromTypedConfig(cfg)
	if ServiceRestartRequired(values, true) {
		t.Fatal("native execution unexpectedly requires service restart")
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(cfg, true))
	if ServiceRestartRequired(values, true) {
		t.Fatal("matching applied service fingerprint reported stale")
	}
	changed := ValuesFromTypedConfig(cfg)
	changed["ANDROID_RUNNER_IMAGE"] = "runner:changed"
	if !ServiceRestartRequired(changed, true) {
		t.Fatal("changed service configuration was not reported stale")
	}
	if !ServiceRestartRequired(values, false) {
		t.Fatal("bootstrap/configured mismatch was not reported stale")
	}
}

func hasClass(diff ConfigDiff, want ApplyClass) bool {
	for _, class := range diff.Classes {
		if class == want {
			return true
		}
	}
	return false
}

func containsEnvironment(environment []string, want string) bool {
	for _, value := range environment {
		if value == want {
			return true
		}
	}
	return false
}
