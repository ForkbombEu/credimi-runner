package runtime

import (
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"
)

func TestComposeUsesOneGlobalRunnerImageAndForegroundRuntime(t *testing.T) {
	values := indexedComposeValues(Values{
		"ANDROID_RUNNER_IMAGE": "credimi-runner:local",
		"ANDROID_PULL_POLICY":  "never",
		"ANDROID_NETWORK":      "credimi-runner",
		"CREDIMI_SERVICE_MODE": "manual",
	})
	content, err := ComposeYAML(values, "linux")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"image: credimi-runner:local", "pull_policy: never", "command:\n      - internal-runtime", "CREDIMI_RUNNER_CONFIG_DIR: /etc/credimi-runner", ".:/etc/credimi-runner", "runner_state:/var/lib/credimi-runner", "runner_state:\n    name: \"credimi-runner-state\"", "runner_tools:\n    name: \"credimi-runner-tools\"", "android_sdk:\n    name: \"credimi-runner-sdk\""} {
		if !strings.Contains(content, want) {
			t.Fatalf("compose missing %q:\n%s", want, content)
		}
	}
	_, source, _, ok := stdruntime.Caller(0)
	if !ok {
		t.Fatal("resolve compose test source path")
	}
	dockerfile, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), `ENTRYPOINT ["/usr/local/bin/credimi-runner"]`) || !strings.Contains(content, "command:\n      - internal-runtime") {
		t.Fatalf("container invocation does not compose the binary entrypoint with internal-runtime:\nDockerfile=%s\nCompose=%s", dockerfile, content)
	}
}

func TestComposeBootstrapsWithoutConfiguredInventory(t *testing.T) {
	dir := t.TempDir()
	values := Values{"ANDROID_RUNNER_IMAGE": "credimi-runner:local", "ANDROID_PULL_POLICY": "never"}
	if err := WriteComposeFileForOS(dir, values, "linux"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "docker-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, "image: credimi-runner:local") || !strings.Contains(content, "pull_policy: never") || !strings.Contains(content, "command:\n      - internal-runtime") || !strings.Contains(content, ":/etc/credimi-runner") {
		t.Fatalf("first-run compose bootstrap is incomplete:\n%s", content)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("Compose generation must not persist bootstrap values: err=%v", err)
	}
}

func TestWriteComposeFileUsesAtomicWritableConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := WriteComposeFile(dir, Values{"ANDROID_RUNNER_IMAGE": "runner:local", "ANDROID_PULL_POLICY": "never"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteComposeFileForOS(dir, Values{"ANDROID_RUNNER_IMAGE": "runner:local", "ANDROID_PULL_POLICY": "never"}, "linux"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "docker-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), dir+":/etc/credimi-runner") || strings.Contains(string(content), dir+":/etc/credimi-runner:ro") {
		t.Fatalf("compose config mount is not writable: %s", content)
	}
}

func TestComposeEmulatorAndPhoneShareTheGlobalImage(t *testing.T) {
	values := indexedComposeValues(Values{"ANDROID_RUNNER_IMAGE": "runner:shared", "ANDROID_PULL_POLICY": "never"})
	values["CREDIMI_DEVICE_COUNT"] = "2"
	values["CREDIMI_DEVICE_2_ID"] = "acme/runner/emulator"
	values["CREDIMI_DEVICE_2_TYPE"] = "android_emulator"
	values["CREDIMI_DEVICE_2_MODE"] = "emulator"
	previous := hostKVMAvailable
	hostKVMAvailable = func(string) bool { return true }
	t.Cleanup(func() { hostKVMAvailable = previous })
	content, err := ComposeYAML(values, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(content, "image: runner:shared") != 1 || !strings.Contains(content, "/dev/kvm:/dev/kvm") {
		t.Fatalf("mixed inventory did not use one stable runner:\n%s", content)
	}
}

func TestPhysicalOnlyRunnerMapsStableKVMCapability(t *testing.T) {
	values := indexedComposeValues(Values{"ANDROID_RUNNER_IMAGE": "runner:shared", "ANDROID_PULL_POLICY": "never"})
	previous := hostKVMAvailable
	hostKVMAvailable = func(string) bool { return true }
	t.Cleanup(func() { hostKVMAvailable = previous })
	content, err := ComposeYAML(values, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "/dev/kvm:/dev/kvm") {
		t.Fatalf("stable KVM capability was not mapped for a physical-only runner:\n%s", content)
	}
}

func TestComposeUsesPersistentAndroidToolingAndConfiguredADBKeys(t *testing.T) {
	values := indexedComposeValues(Values{
		"ANDROID_RUNNER_IMAGE":      "runner:shared",
		"ANDROID_PULL_POLICY":       "never",
		"ANDROID_STATE_VOLUME":      "state-volume",
		"ANDROID_TOOL_CACHE_VOLUME": "tool-volume",
		"ANDROID_SDK_VOLUME":        "sdk-volume",
		"ANDROID_ADB_KEYS_PATH":     "/home/tester/.android",
	})
	content, err := ComposeYAML(values, "linux")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/home/tester/.android:/root/.android:ro",
		"runner_state:/var/lib/credimi-runner",
		"runner_tools:/opt/credimi-runner/tools",
		"android_sdk:/opt/android-sdk",
		"runner_state:\n    name: \"state-volume\"",
		"runner_tools:\n    name: \"tool-volume\"",
		"android_sdk:\n    name: \"sdk-volume\"",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("compose missing persistent tooling mount %q:\n%s", want, content)
		}
	}
}

func TestComposeDeclaresEveryPersistentRunnerMount(t *testing.T) {
	content, err := ComposeYAML(Values{
		"ANDROID_STATE_VOLUME":      "custom-state",
		"ANDROID_TOOL_CACHE_VOLUME": "custom-tools",
		"ANDROID_SDK_VOLUME":        "custom-sdk",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	for _, volume := range []struct {
		alias, mount, name string
	}{
		{"runner_state", "runner_state:/var/lib/credimi-runner", "custom-state"},
		{"runner_tools", "runner_tools:/opt/credimi-runner/tools", "custom-tools"},
		{"android_sdk", "android_sdk:/opt/android-sdk", "custom-sdk"},
	} {
		if !strings.Contains(content, "- "+volume.mount) {
			t.Fatalf("runner mount %q is missing:\n%s", volume.mount, content)
		}
		declaration := volume.alias + ":\n    name: \"" + volume.name + "\""
		if !strings.Contains(content, declaration) {
			t.Fatalf("volume declaration %q is missing:\n%s", declaration, content)
		}
	}
	for _, alias := range []string{"adbkeys", "caddy_data", "caddy_config"} {
		if !strings.Contains(content, "  "+alias+":\n") {
			t.Fatalf("named volume %q is referenced but not declared:\n%s", alias, content)
		}
	}
}

func TestComposeEnvironmentUsesTypedValues(t *testing.T) {
	t.Setenv("RUNNER_PORT", "shell-runner-port")
	t.Setenv("CREDIMI_COMPOSE_PROJECT", "shell-project")
	values := Values{
		"RUNNER_PORT":             "18050",
		"DASHBOARD_PORT":          "18051",
		"RUNNER_CADDY_SITE":       "runner.example",
		"CLOUDFLARE_TUNNEL_TOKEN": "secret-token",
		"ANDROID_PULL_POLICY":     "never",
		"CREDIMI_SERVICE_MODE":    "cloudflare-managed",
		"CREDIMI_DEVICE_COUNT":    "1",
		"CREDIMI_RUNNER_ID":       "acme/runner",
		"CREDIMI_DEVICE_1_ID":     "acme/runner/device",
		"CREDIMI_DEVICE_1_TYPE":   "android_phone",
		"CREDIMI_DEVICE_1_MODE":   "wifi",
		"CREDIMI_DEVICE_1_SERIAL": "device-1",
	}
	plan := BuildRuntimePlanForOS(t.TempDir(), values, "linux")
	environment := composeEnv(values, plan, "linux")
	got := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			got[key] = value
		}
	}
	for key, want := range map[string]string{
		"RUNNER_PORT":                "18050",
		"DASHBOARD_PORT":             "18051",
		"RUNNER_CADDY_SITE":          "runner.example",
		"CLOUDFLARE_TUNNEL_TOKEN":    "secret-token",
		"CREDIMI_COMPOSE_PROJECT":    plan.ComposeProject,
		"CREDIMI_CONFIG_FINGERPRINT": plan.ConfigFingerprint,
		"CREDIMI_TUNNEL_URL":         "http://caddy:80",
		"CADDY_INGRESS_NETWORKS":     "credimi-runner-ingress",
		"ADB_SERVER_SOCKET":          "tcp:host.docker.internal:5037",
	} {
		if got[key] != want {
			t.Fatalf("compose environment %s=%q, want %q", key, got[key], want)
		}
	}
	if got["CREDIMI_COMPOSE_PROJECT"] == "shell-project" || got["RUNNER_PORT"] == "shell-runner-port" {
		t.Fatalf("shell interpolation values were not replaced: %#v", got)
	}
}

func TestComposeUsesHostNetworkForLinuxUSBADB(t *testing.T) {
	values := indexedComposeValues(Values{"ANDROID_RUNNER_IMAGE": "runner:shared", "ANDROID_PULL_POLICY": "never"})
	content, err := ComposeYAML(values, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "network_mode: host") || !strings.Contains(content, "caddy.reverse_proxy: \"127.0.0.1:") {
		t.Fatalf("USB compose did not use the host network topology:\n%s", content)
	}
}

func TestSharedRunnerImageUsesGlobalConfiguration(t *testing.T) {
	image, policy, err := SharedRunnerImage(indexedComposeValues(Values{"ANDROID_RUNNER_IMAGE": "runner:local", "ANDROID_PULL_POLICY": "never"}), "linux")
	if err != nil || image != "runner:local" || policy != "never" {
		t.Fatalf("shared image=%q policy=%q err=%v", image, policy, err)
	}
}

func TestComposeRejectsIOSForContainerBackend(t *testing.T) {
	values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "ios_simulator"})
	if _, err := ComposeYAML(values, "linux"); err == nil || !strings.Contains(err.Error(), "require macOS") {
		t.Fatalf("ComposeYAML error = %v", err)
	}
}

func TestComposeServicesFollowBackendSelection(t *testing.T) {
	values, err := NormalizeValues(Values{"CREDIMI_RUNNER_TYPE": "ios_simulator", "CREDIMI_SERVICE_MODE": "manual"}, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildRuntimePlanForOS(t.TempDir(), values, "darwin")
	if plan.Backend != DefaultNativeBackend || len(plan.ComposeServices) != 0 {
		t.Fatalf("native manual plan = %#v", plan)
	}
}

func TestRuntimePlanServiceModesRemainExplicit(t *testing.T) {
	cases := []struct {
		name, mode   string
		want         string
		serviceCount int
	}{
		{"container manual", "manual", "runner", 1},
		{"container quick tunnel", "auto", "runner", 3},
		{"container named tunnel", "cloudflare-managed", "runner", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := Values{"CREDIMI_SERVICE_MODE": tc.mode, "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner/device"}
			plan := BuildRuntimePlan(t.TempDir(), values)
			joined := strings.Join(plan.ComposeServices, ",")
			if !strings.Contains(joined, tc.want) || len(plan.ComposeServices) != tc.serviceCount {
				t.Fatalf("plan services=%v, want %q and %d services", plan.ComposeServices, tc.want, tc.serviceCount)
			}
		})
	}
}

func TestRunnerReadinessDoesNotRequireIdleRedroid(t *testing.T) {
	values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "redroid"})
	if DeviceReadinessRequired(values, "linux") {
		t.Fatal("idle Redroid must not block runner startup")
	}
}

func TestRuntimeReadinessAndReachabilityFollowInventory(t *testing.T) {
	cases := []struct {
		name, goos, deviceType string
		ready, reachable       bool
	}{
		{"android linux", "linux", "android_phone", true, true},
		{"android mac", "darwin", "android_phone", true, true},
		{"redroid idle", "linux", "redroid", false, true},
		{"ios mac", "darwin", "ios_simulator", false, true},
		{"ios linux rejected", "linux", "ios_simulator", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": tc.deviceType})
			if got := DeviceReadinessRequired(values, tc.goos); got != tc.ready {
				t.Fatalf("readiness=%v, want %v", got, tc.ready)
			}
			if got := RunnerAPIReachableFromHost(values, tc.goos); got != tc.reachable {
				t.Fatalf("reachable=%v, want %v", got, tc.reachable)
			}
		})
	}
}

func TestDeviceReadinessSkipsDisabledAndDeferredTargets(t *testing.T) {
	for _, mode := range []string{"no_device", "emulator"} {
		values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_RUNNER_DEVICE_MODE": mode})
		if mode == "no_device" && DeviceReadinessRequired(values, "linux") {
			t.Fatalf("deferred target mode %q required readiness", mode)
		}
	}
	values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone"})
	values["CREDIMI_DEVICE_1_ENABLED"] = "false"
	if DeviceReadinessRequired(values, "linux") {
		t.Fatal("disabled target required readiness")
	}
}

func TestReadinessHelpersFailClosedForInvalidInventory(t *testing.T) {
	values := Values{"CREDIMI_DEVICE_COUNT": "not-a-number"}
	if RunnerAPIReachableFromHost(values, "linux") || RunnerReadinessRequiredBeforeRegistration(values, "linux") || DeviceReadinessRequired(values, "linux") {
		t.Fatal("invalid inventory was reported as reachable or ready")
	}
}

func indexedComposeValues(values Values) Values {
	indexed := cloneValues(values)
	deviceType := indexed["CREDIMI_RUNNER_TYPE"]
	delete(indexed, "CREDIMI_RUNNER_TYPE")
	mode := indexed["CREDIMI_RUNNER_DEVICE_MODE"]
	delete(indexed, "CREDIMI_RUNNER_DEVICE_MODE")
	if mode == "" {
		switch deviceType {
		case "android_emulator":
			mode = "emulator"
		case "redroid":
			mode = "no_device"
		case "ios_simulator":
			mode = "no_device"
		default:
			mode = "usb"
		}
	}
	indexed["CREDIMI_RUNNER_ID"] = "acme/runner"
	indexed["CREDIMI_DEVICE_COUNT"] = "1"
	indexed["CREDIMI_DEVICE_1_ID"] = "acme/runner/device"
	indexed["CREDIMI_DEVICE_1_TYPE"] = deviceType
	indexed["CREDIMI_DEVICE_1_MODE"] = mode
	return indexed
}
