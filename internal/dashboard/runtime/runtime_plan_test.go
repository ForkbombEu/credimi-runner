package runtime

import "testing"

func TestBuildRuntimePlanExpectedServices(t *testing.T) {
	tests := []struct {
		name string
		vals Values
		want []string
	}{
		{"container-auto", Values{}, []string{"runner", "caddy", "tunnel", "temporal"}},
		{"container-managed", Values{"CREDIMI_SERVICE_MODE": "cloudflare-managed"}, []string{"runner", "caddy", "tunnel_named", "temporal"}},
		{"native-manual", Values{"CREDIMI_RUNNER_TYPE": "ios_simulator", "CREDIMI_SERVICE_MODE": "manual"}, []string{"runner_host_process", "temporal"}},
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

func TestDiffValuesCoverageBranches(t *testing.T) {
	if got := DiffValues(Values{"CREDIMI_RUNNER_ID": "acme/runner"}, Values{"CREDIMI_RUNNER_ID": "acme/runner"}); len(got.Classes) != 1 || got.Classes[0] != ApplySavedOnly {
		t.Fatalf("saved only diff = %#v", got)
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

func TestBackendSelectionFollowsInventoryAndHostPlatform(t *testing.T) {
	cases := []struct {
		name, goos, deviceType, want string
	}{
		{"linux android", "linux", "android_phone", DefaultContainerBackend},
		{"mac android", "darwin", "android_phone", DefaultContainerBackend},
		{"mac ios", "darwin", "ios_simulator", DefaultHostBackend},
		{"mac mixed", "darwin", "android_phone", DefaultHostBackend},
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

func TestBackendTransitionIsExplicitlyDetected(t *testing.T) {
	oldValues := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone"})
	newValues := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "ios_simulator"})
	diff := DiffValuesForOS(oldValues, newValues, "darwin")
	if !diff.BackendTransition || !containsApplyClass(diff.Classes, ApplyRestartRequired) {
		t.Fatalf("backend transition diff = %#v", diff)
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
