package runtime

import (
	"strings"
	"testing"
)

func TestComposeServicesByPlan(t *testing.T) {
	tests := []struct {
		name string
		vals Values
		goos string
		want []string
	}{
		{"container auto", Values{}, "linux", []string{"runner", "caddy", "tunnel"}},
		{"container manual", Values{"CREDIMI_SERVICE_MODE": "manual"}, "linux", []string{"runner"}},
		{"host manual", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_RUNNER_TYPE": "ios_simulator", "CREDIMI_SERVICE_MODE": "manual"}, "darwin", nil},
		{"host auto", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_RUNNER_TYPE": "ios_simulator"}, "darwin", []string{"runner_host", "caddy", "tunnel"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, err := NormalizeValues(tt.vals, tt.goos)
			if err != nil {
				t.Fatal(err)
			}
			plan := BuildRuntimePlan(t.TempDir(), values)
			if strings.Join(plan.ComposeServices, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("services = %v want %v", plan.ComposeServices, tt.want)
			}
		})
	}
}

func TestComposeParityCases(t *testing.T) {
	tests := []struct {
		name     string
		vals     Values
		contains []string
	}{
		{
			name:     "linux USB inventory uses one host-network runner",
			vals:     indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone"}),
			contains: []string{"pull_policy: always", "--inventory", "network_mode: host", `caddy.reverse_proxy: "127.0.0.1:${RUNNER_PORT:-8050}"`, "command: tunnel --no-autoupdate --url ${CREDIMI_TUNNEL_URL:-http://127.0.0.1:80}", `test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:2019/config/ | grep -q reverse_proxy"]`, "condition: service_healthy"},
		},
		{
			name:     "custom shared image",
			vals:     indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone", "RUNNER_IMAGE": "custom:latest", "RUNNER_IMAGE_PULL_POLICY": "never"}),
			contains: []string{"image: custom:latest", "pull_policy: never"},
		},
		{
			name:     "wifi inventory starts through inventory entrypoint",
			vals:     indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_RUNNER_DEVICE_MODE": "wifi", "CREDIMI_DEVICE_1_WIFI_IP": "192.168.1.10"}),
			contains: []string{"--inventory", "ADB_SERVER_SOCKET", `caddy.reverse_proxy: "{{upstreams ${RUNNER_PORT:-8050}}}"`},
		},
		{
			name:     "emulator inventory uses shared emulator image",
			vals:     indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_emulator"}),
			contains: []string{"image: " + DefaultEmulatorImage, "--inventory", "BASE_NAME: ${CREDIMI_DEVICE_1_BASE_NAME}", "GOLDEN_PATH: ${CREDIMI_DEVICE_1_GOLDEN_PATH}", "/dev/kvm:/dev/kvm", "${CREDIMI_DEVICE_1_HOST_AVD_GOLDEN_PATH}:/avd-golden", `caddy.reverse_proxy: "{{upstreams ${RUNNER_PORT:-8050}}}"`, "networks:\n      - ingress"},
		},
		{
			name:     "mixed Android inventory needs one emulator-capable runner",
			vals:     mixedComposeValues(),
			contains: []string{"image: " + DefaultEmulatorImage, "--inventory", "/dev/kvm:/dev/kvm", "network_mode: host"},
		},
		{
			name:     "manual inventory publishes runner API",
			vals:     indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "redroid", "CREDIMI_SERVICE_MODE": "manual"}),
			contains: []string{"--inventory", `- "${RUNNER_PORT:-8050}:${RUNNER_PORT:-8050}"`, "networks:\n      - ingress"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := ComposeYAML(tt.vals, "linux")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.contains {
				if !strings.Contains(content, want) {
					t.Fatalf("compose missing %q:\n%s", want, content)
				}
			}
			if strings.Contains(content, "restart: unless-stopped") || !strings.Contains(content, "restart: \"no\"") {
				t.Fatalf("managed services must not restart after host reboot:\n%s", content)
			}
			if tt.name == "emulator" && strings.Contains(content, "command: tunnel --no-autoupdate --url ${CREDIMI_TUNNEL_URL:-http://127.0.0.1:80}") {
				t.Fatalf("emulator tunnel should use caddy network URL, got:\n%s", content)
			}
		})
	}
}

func mixedComposeValues() Values {
	values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone"})
	values["CREDIMI_DEVICE_COUNT"] = "3"
	values["CREDIMI_DEVICE_2_ID"] = "acme/runner/emulator"
	values["CREDIMI_DEVICE_2_TYPE"] = "android_emulator"
	values["CREDIMI_DEVICE_2_MODE"] = "emulator"
	values["CREDIMI_DEVICE_3_ID"] = "acme/runner/redroid"
	values["CREDIMI_DEVICE_3_TYPE"] = "redroid"
	values["CREDIMI_DEVICE_3_MODE"] = "no_device"
	return values
}

func TestComposeMixedInventoryUsesEmulatorImageOverPhoneOverride(t *testing.T) {
	values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone", "RUNNER_IMAGE": "example.test/phone:latest"})
	values["CREDIMI_DEVICE_COUNT"] = "2"
	values["CREDIMI_DEVICE_2_ID"] = "acme/runner/emulator"
	values["CREDIMI_DEVICE_2_TYPE"] = "android_emulator"
	values["CREDIMI_DEVICE_2_MODE"] = "emulator"
	values["CREDIMI_DEVICE_2_RUNNER_IMAGE"] = "example.test/emulator:latest"
	content, err := ComposeYAML(values, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "image: example.test/emulator:latest") {
		t.Fatalf("mixed inventory must select emulator image:\n%s", content)
	}
}

func TestComposeMixedInventoryUsesLocalEmulatorSibling(t *testing.T) {
	values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone", "RUNNER_IMAGE": "credimi-runner-phone:latest", "RUNNER_IMAGE_PULL_POLICY": "never"})
	values["CREDIMI_DEVICE_COUNT"] = "2"
	values["CREDIMI_DEVICE_2_ID"] = "acme/runner/emulator"
	values["CREDIMI_DEVICE_2_TYPE"] = "android_emulator"
	values["CREDIMI_DEVICE_2_MODE"] = "emulator"
	values["CREDIMI_DEVICE_2_RUNNER_IMAGE"] = DefaultEmulatorImage
	values["CREDIMI_DEVICE_2_RUNNER_IMAGE_PULL_POLICY"] = "never"
	content, err := ComposeYAML(values, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "image: credimi-runner-emulator:latest") || !strings.Contains(content, "pull_policy: never") {
		t.Fatalf("mixed local inventory must select local emulator sibling:\n%s", content)
	}
}

func TestComposeRejectsConflictingEmulatorImageOverrides(t *testing.T) {
	values := indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_emulator", "RUNNER_IMAGE": "example.test/emulator-one:latest"})
	values["CREDIMI_DEVICE_COUNT"] = "2"
	values["CREDIMI_DEVICE_2_ID"] = "acme/runner/emulator-two"
	values["CREDIMI_DEVICE_2_TYPE"] = "android_emulator"
	values["CREDIMI_DEVICE_2_MODE"] = "emulator"
	values["CREDIMI_DEVICE_2_RUNNER_IMAGE"] = "example.test/emulator-two:latest"
	if _, err := ComposeYAML(values, "linux"); err == nil || !strings.Contains(err.Error(), "one emulator runtime image") {
		t.Fatalf("ComposeYAML error = %v", err)
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
		default:
			mode = "usb"
		}
	}
	indexed["CREDIMI_RUNNER_ID"] = "acme/runner"
	indexed["CREDIMI_DEVICE_COUNT"] = "1"
	indexed["CREDIMI_DEVICE_1_ID"] = "acme/runner/device"
	indexed["CREDIMI_DEVICE_1_TYPE"] = deviceType
	indexed["CREDIMI_DEVICE_1_MODE"] = mode
	for oldKey, deviceKey := range map[string]string{
		"RUNNER_IMAGE": "RUNNER_IMAGE", "RUNNER_IMAGE_PULL_POLICY": "RUNNER_IMAGE_PULL_POLICY",
		"AVDCTL_SSH_TARGET": "AVDCTL_SSH_TARGET", "AVDCTL_SSH_KNOWN_HOSTS_PATH": "AVDCTL_SSH_KNOWN_HOSTS_PATH",
	} {
		if value := indexed[oldKey]; value != "" {
			indexed["CREDIMI_DEVICE_1_"+deviceKey] = value
			delete(indexed, oldKey)
		}
	}
	return indexed
}

func TestRunnerAPIReachableFromHost(t *testing.T) {
	tests := []struct {
		name string
		vals Values
		goos string
		want bool
	}{
		{"host backend", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_RUNNER_TYPE": "ios_simulator"}, "darwin", true},
		{"linux phone host adb", Values{"CREDIMI_RUNNER_TYPE": "android_phone"}, "linux", true},
		{"linux emulator bridge", Values{"CREDIMI_RUNNER_TYPE": "android_emulator"}, "linux", true},
		{"linux manual container", Values{"CREDIMI_RUNNER_TYPE": "android_emulator", "CREDIMI_SERVICE_MODE": "manual"}, "linux", true},
		{"darwin container published locally", Values{"CREDIMI_RUNNER_BACKEND": "container", "CREDIMI_RUNNER_TYPE": "android_phone"}, "darwin", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RunnerAPIReachableFromHost(tt.vals, tt.goos); got != tt.want {
				t.Fatalf("RunnerAPIReachableFromHost = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunnerReadinessRequiredBeforeRegistration(t *testing.T) {
	tests := []struct {
		name string
		vals Values
		goos string
		want bool
	}{
		{"host backend auto", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_SERVICE_MODE": "auto", "CREDIMI_RUNNER_TYPE": "ios_simulator"}, "darwin", true},
		{"host backend managed", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_SERVICE_MODE": "cloudflare-managed", "CREDIMI_RUNNER_TYPE": "ios_simulator"}, "darwin", true},
		{"host backend manual", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_SERVICE_MODE": "manual", "CREDIMI_RUNNER_TYPE": "ios_simulator"}, "darwin", true},
		{"linux phone container auto", Values{"CREDIMI_RUNNER_TYPE": "android_phone"}, "linux", true},
		{"linux phone container manual", Values{"CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_SERVICE_MODE": "manual"}, "linux", true},
		{"linux emulator container auto", Values{"CREDIMI_RUNNER_TYPE": "android_emulator"}, "linux", true},
		{"darwin default host auto", Values{}, "darwin", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RunnerReadinessRequiredBeforeRegistration(tt.vals, tt.goos); got != tt.want {
				t.Fatalf("RunnerReadinessRequiredBeforeRegistration = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeviceReadinessRequired(t *testing.T) {
	tests := []struct {
		name string
		vals Values
		goos string
		want bool
	}{
		{"usb phone", indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone"}), "linux", true},
		{"wifi phone", indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_RUNNER_DEVICE_MODE": "wifi", "CREDIMI_DEVICE_1_WIFI_IP": "192.168.1.10"}), "linux", true},
		{"redroid managed device", indexedComposeValues(Values{"CREDIMI_RUNNER_TYPE": "redroid", "CREDIMI_DEVICE_1_WIFI_IP": "192.168.1.10"}), "linux", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeviceReadinessRequired(tt.vals, tt.goos); got != tt.want {
				t.Fatalf("DeviceReadinessRequired = %v, want %v", got, tt.want)
			}
		})
	}
}
