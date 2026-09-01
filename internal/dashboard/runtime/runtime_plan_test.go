package runtime

import (
	"os"
	"strings"
	"testing"

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
	for _, class := range []ApplyClass{ApplyRuntimeReconcile, ApplyServiceRestartRequired, ApplyCredimiUpdateRequired} {
		if !hasClass(deviceDiff, class) {
			t.Fatalf("device diff missing %q: %#v", class, deviceDiff)
		}
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
	if len(added.ChangedKeys) != 1 || !hasClass(added, ApplyServiceRestartRequired) {
		t.Fatalf("added device diff = %+v", added)
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
