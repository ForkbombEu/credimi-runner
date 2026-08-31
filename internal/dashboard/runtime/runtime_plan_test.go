package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRuntimePlanExpectedServices(t *testing.T) {
	tests := []struct {
		name string
		vals Values
		want []string
	}{
		{"container-auto", Values{}, []string{"runner", "caddy", "tunnel", "temporal"}},
		{"container-managed", Values{"CREDIMI_SERVICE_MODE": "cloudflare-managed"}, []string{"runner", "caddy", "tunnel_named", "temporal"}},
		{"native-manual", Values{"CREDIMI_RUNNER_TYPE": "ios_simulator", "CREDIMI_SERVICE_MODE": "manual"}, []string{"temporal"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goos := "linux"
			if tt.name == "native-manual" {
				goos = "darwin"
			}
			normalized, err := NormalizeValues(tt.vals, goos)
			if err != nil {
				t.Fatal(err)
			}
			plan := BuildRuntimePlanForOS("/tmp/credimi", normalized, goos)
			var got []string
			for _, service := range plan.ExpectedServices {
				got = append(got, service.ID)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ExpectedServices = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ExpectedServices = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestBootstrapPlanStartsOnlyRunner(t *testing.T) {
	values := (BootstrapContext{RunnerImage: "credimi-runner:local", PullPolicy: "never", BeforeSetup: true}).Apply(DefaultValues())
	plan := BuildRuntimePlanForOS(t.TempDir(), values, "linux")
	if got := plan.ComposeServices; len(got) != 1 || got[0] != "runner" {
		t.Fatalf("bootstrap services = %v, want runner only", got)
	}
	if plan.ServiceMode != "bootstrap" || plan.PublicMode != "bootstrap" {
		t.Fatalf("bootstrap modes = %q/%q", plan.ServiceMode, plan.PublicMode)
	}
}

func TestDiffValuesCoverageBranches(t *testing.T) {
	if got := DiffValues(Values{"CREDIMI_RUNNER_ID": "acme/runner"}, Values{"CREDIMI_RUNNER_ID": "acme/runner"}); len(got.Classes) != 1 || got.Classes[0] != ApplySavedOnly {
		t.Fatalf("saved only diff = %#v", got)
	}
	if got := DiffValues(Values{"DASHBOARD_TOKEN": "old"}, Values{"DASHBOARD_TOKEN": "new"}); len(got.Classes) != 1 || got.Classes[0] != ApplySavedOnly {
		t.Fatalf("dashboard token diff = %#v", got)
	}
	if got := DiffValues(Values{"CREDIMI_RUNNER_NAME": "a"}, Values{"CREDIMI_RUNNER_NAME": "b"}); len(got.ChangedKeys) == 0 || !containsApplyClass(got.Classes, ApplyRestartRequired) || !containsApplyClass(got.Classes, ApplyCredimiUpdateRequired) {
		t.Fatalf("runner name changed diff = %#v", got)
	}
	if got := DiffValues(Values{"RUNNER_PORT": "8050"}, Values{"RUNNER_PORT": "8051"}); !containsApplyClass(got.Classes, ApplyComposeRecreate) || !containsApplyClass(got.Classes, ApplyCredimiUpdateRequired) {
		t.Fatalf("recreate diff = %#v", got)
	}
	if got := DiffValues(Values{"CREDIMI_DEVICE_1_ID": "acme/runner/a"}, Values{"CREDIMI_DEVICE_1_ID": "acme/runner/b"}); containsApplyClass(got.Classes, ApplyRestartRequired) || containsApplyClass(got.Classes, ApplyComposeRecreate) || !containsApplyClass(got.Classes, ApplyCredimiUpdateRequired) {
		t.Fatalf("device inventory diff = %#v", got)
	}
	if got := DiffValues(Values{"CREDIMI_DEVICE_1_ID": "acme/runner/a"}, Values{"CREDIMI_DEVICE_1_ID": "acme/runner/a", "CREDIMI_DEVICE_2_ID": "acme/runner/b"}); containsApplyClass(got.Classes, ApplyRestartRequired) || containsApplyClass(got.Classes, ApplyComposeRecreate) || !containsApplyClass(got.Classes, ApplyCredimiUpdateRequired) {
		t.Fatalf("added device inventory diff = %#v", got)
	}
}

func TestDashboardTokenIsSavedOnlyAndTopologyRequiresServiceRestart(t *testing.T) {
	token := DiffValuesForOS(Values{"DASHBOARD_TOKEN": "old"}, Values{"DASHBOARD_TOKEN": "new"}, "linux")
	if len(token.Classes) != 1 || token.Classes[0] != ApplySavedOnly {
		t.Fatalf("token diff=%#v", token)
	}
	topology := DiffValuesForOS(Values{"ANDROID_RUNNER_IMAGE": "runner:a"}, Values{"ANDROID_RUNNER_IMAGE": "runner:b"}, "linux")
	if !containsApplyClass(topology.Classes, ApplyServiceRestartRequired) {
		t.Fatalf("service restart class missing: %#v", topology)
	}
}

func TestDiffValuesClassifiesOnlyTopologyChangingDeviceEditsAsRecreate(t *testing.T) {
	base := Values{
		"CREDIMI_RUNNER_ID":        "acme/runner",
		"CREDIMI_DEVICE_COUNT":     "1",
		"CREDIMI_DEVICE_1_ID":      "acme/runner/pixel",
		"CREDIMI_DEVICE_1_TYPE":    "android_emulator",
		"CREDIMI_DEVICE_1_MODE":    "emulator",
		"CREDIMI_DEVICE_1_ENABLED": "true",
	}
	description := cloneValues(base)
	description["CREDIMI_DEVICE_1_DESCRIPTION"] = "lab"
	if got := DiffValuesForOS(base, description, "linux"); containsApplyClass(got.Classes, ApplyComposeRecreate) {
		t.Fatalf("metadata-only device edit requested recreate: %#v", got)
	}
	usb := cloneValues(base)
	usb["CREDIMI_DEVICE_1_TYPE"] = "android_phone"
	usb["CREDIMI_DEVICE_1_MODE"] = "usb"
	usb["CREDIMI_DEVICE_1_SERIAL"] = "usb-1"
	if got := DiffValuesForOS(base, usb, "linux"); !containsApplyClass(got.Classes, ApplyComposeRecreate) {
		t.Fatalf("USB topology change did not request recreate: %#v", got)
	}
}

func TestDiffValuesClassifiesKnownHostsMountChangesAsTopology(t *testing.T) {
	dir := t.TempDir()
	knownA := filepath.Join(dir, "known-a")
	knownB := filepath.Join(dir, "known-b")
	for _, path := range []string{knownA, knownB} {
		if err := os.WriteFile(path, []byte("host ssh-ed25519 key\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := redroidTopologyValues(knownA)

	tests := []struct {
		name string
		new  Values
		want bool
	}{
		{name: "mount added", new: redroidTopologyValues(knownA), want: true},
		{name: "mount replaced", new: redroidTopologyValues(knownB), want: true},
	}
	noMount := cloneValues(base)
	delete(noMount, "CREDIMI_DEVICE_1_AVDCTL_SSH_TARGET")
	delete(noMount, "CREDIMI_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH")
	tests[0].new = base
	tests = append(tests, struct {
		name string
		new  Values
		want bool
	}{name: "mount removed", new: noMount, want: true})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old := base
			if tc.name == "mount added" {
				old = noMount
			}
			if got := containsApplyClass(DiffValuesForOS(old, tc.new, "linux").Classes, ApplyComposeRecreate); got != tc.want {
				t.Fatalf("compose recreate = %v, want %v", got, tc.want)
			}
		})
	}

	unrelated := cloneValues(base)
	unrelated["CREDIMI_DEVICE_1_WIFI_IP"] = "192.0.2.99"
	if got := DiffValuesForOS(base, unrelated, "linux"); containsApplyClass(got.Classes, ApplyComposeRecreate) {
		t.Fatalf("unrelated Redroid edit requested recreate: %#v", got)
	}

	duplicate := cloneValues(base)
	duplicate["CREDIMI_DEVICE_COUNT"] = "2"
	duplicate["CREDIMI_DEVICE_2_ID"] = "acme/runner/redroid-two"
	duplicate["CREDIMI_DEVICE_2_TYPE"] = "redroid"
	duplicate["CREDIMI_DEVICE_2_MODE"] = "redroid"
	duplicate["CREDIMI_DEVICE_2_WIFI_IP"] = "192.0.2.20"
	duplicate["CREDIMI_DEVICE_2_WIFI_PORT"] = "5556"
	duplicate["CREDIMI_DEVICE_2_AVDCTL_SSH_TARGET"] = "bob@two"
	duplicate["CREDIMI_DEVICE_2_AVDCTL_SSH_KNOWN_HOSTS_PATH"] = knownA
	if got := DiffValuesForOS(base, duplicate, "linux"); containsApplyClass(got.Classes, ApplyComposeRecreate) {
		t.Fatalf("duplicate known-host mount requested recreate: %#v", got)
	}
}

func redroidTopologyValues(knownHosts string) Values {
	return Values{
		"CREDIMI_RUNNER_ID":                            "acme/runner",
		"CREDIMI_DEVICE_COUNT":                         "1",
		"CREDIMI_DEVICE_1_ID":                          "acme/runner/redroid",
		"CREDIMI_DEVICE_1_TYPE":                        "redroid",
		"CREDIMI_DEVICE_1_MODE":                        "redroid",
		"CREDIMI_DEVICE_1_WIFI_IP":                     "192.0.2.10",
		"CREDIMI_DEVICE_1_WIFI_PORT":                   "5555",
		"CREDIMI_DEVICE_1_AVDCTL_SSH_TARGET":           "alice@one",
		"CREDIMI_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH": knownHosts,
	}
}

func TestDiffValuesDoesNotRecreateUnifiedRunnerForAddedEmulator(t *testing.T) {
	phone := Values{
		"ANDROID_RUNNER_IMAGE":      "runner:shared",
		"ANDROID_PULL_POLICY":       "never",
		"CREDIMI_RUNNER_ID":         "acme/runner",
		"CREDIMI_DEVICE_COUNT":      "1",
		"CREDIMI_DEVICE_1_ID":       "acme/runner/phone",
		"CREDIMI_DEVICE_1_TYPE":     "android_phone",
		"CREDIMI_DEVICE_1_MODE":     "usb",
		"CREDIMI_DEVICE_1_SERIAL":   "usb-1",
		"CREDIMI_DEVICE_1_ENABLED":  "true",
		"CREDIMI_SERVICE_MODE":      "auto",
		"ANDROID_STATE_VOLUME":      "state",
		"ANDROID_TOOL_CACHE_VOLUME": "tools",
		"ANDROID_SDK_VOLUME":        "sdk",
	}
	withEmulator := cloneValues(phone)
	withEmulator["CREDIMI_DEVICE_COUNT"] = "2"
	withEmulator["CREDIMI_DEVICE_2_ID"] = "acme/runner/emulator"
	withEmulator["CREDIMI_DEVICE_2_TYPE"] = "android_emulator"
	withEmulator["CREDIMI_DEVICE_2_MODE"] = "emulator"
	withEmulator["CREDIMI_DEVICE_2_ENABLED"] = "true"

	diff := DiffValuesForOS(phone, withEmulator, "linux")
	if containsApplyClass(diff.Classes, ApplyComposeRecreate) {
		t.Fatalf("adding an emulator recreated the unified runner: %#v", diff)
	}
	if !containsApplyClass(diff.Classes, ApplyCredimiUpdateRequired) {
		t.Fatalf("adding an emulator did not request registration update: %#v", diff)
	}
}

func TestRuntimePlanReadinessUsesIndexedDevices(t *testing.T) {
	phone := Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner/pixel", "CREDIMI_DEVICE_1_TYPE": "android_phone", "CREDIMI_DEVICE_1_MODE": "usb"}
	if !RunnerAPIReachableFromHost(phone, "linux") || !RunnerReadinessRequiredBeforeRegistration(phone, "linux") || !DeviceReadinessRequired(phone, "linux") {
		t.Fatalf("phone readiness = api:%t runner:%t device:%t", RunnerAPIReachableFromHost(phone, "linux"), RunnerReadinessRequiredBeforeRegistration(phone, "linux"), DeviceReadinessRequired(phone, "linux"))
	}
	managed := cloneValues(phone)
	managed["CREDIMI_DEVICE_1_MODE"] = "no_device"
	if DeviceReadinessRequired(managed, "linux") {
		t.Fatal("managed device should not require an attached ADB target")
	}
	host := Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner/sim", "CREDIMI_DEVICE_1_TYPE": "ios_simulator", "CREDIMI_DEVICE_1_MODE": "no_device"}
	if !RunnerAPIReachableFromHost(host, "darwin") || !RunnerReadinessRequiredBeforeRegistration(host, "darwin") {
		t.Fatal("host runner should be reachable before registration")
	}
}

func TestBootstrapContextLeavingBootstrapRemovesBootstrapMarkers(t *testing.T) {
	values := (BootstrapContext{RunnerImage: "runner:local", PullPolicy: "never", HostNetwork: true, BeforeSetup: true}).Apply(Values{})
	if !strings.EqualFold(values[BootstrapPhaseEnv], "true") || values[BootstrapHostNetworkEnv] != "true" {
		t.Fatalf("bootstrap values = %#v", values)
	}
	values = BootstrapContext{}.Apply(values)
	if _, ok := values[BootstrapPhaseEnv]; ok {
		t.Fatalf("bootstrap phase marker survived final topology: %#v", values)
	}
	if _, ok := values[BootstrapHostNetworkEnv]; ok {
		t.Fatalf("bootstrap host-network marker survived final topology: %#v", values)
	}
}

func TestBackendSelectionFollowsHostPlatform(t *testing.T) {
	cases := []struct {
		name, goos, deviceType, want string
	}{
		{"linux android", "linux", "android_phone", DefaultContainerBackend},
		{"mac android", "darwin", "android_phone", DefaultNativeBackend},
		{"mac ios", "darwin", "ios_simulator", DefaultNativeBackend},
		{"mac mixed", "darwin", "android_phone", DefaultNativeBackend},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": tc.deviceType})
			if tc.name == "mac mixed" {
				values["CREDIMI_DEVICE_COUNT"] = "2"
				values["CREDIMI_DEVICE_2_ID"] = "acme/runner/ios"
				values["CREDIMI_DEVICE_2_TYPE"] = "ios_simulator"
			}
			normalized, err := NormalizeValues(values, tc.goos)
			if err != nil {
				t.Fatal(err)
			}
			if got := BuildRuntimePlanForOS("", normalized, tc.goos).Backend; got != tc.want {
				t.Fatalf("backend=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestBackendRejectsIOSOnNonMacOS(t *testing.T) {
	values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "ios_simulator"})
	if _, err := NormalizeValues(values, "linux"); err == nil {
		t.Fatal("Linux iOS inventory must be rejected")
	}
}

func containsApplyClass(classes []ApplyClass, want ApplyClass) bool {
	for _, class := range classes {
		if class == want {
			return true
		}
	}
	return false
}
