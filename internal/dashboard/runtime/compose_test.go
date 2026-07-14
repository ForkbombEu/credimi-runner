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
			name:     "usb container",
			vals:     Values{"CREDIMI_RUNNER_TYPE": "android_phone"},
			contains: []string{"pull_policy: always", "--host-adb", "--usb", `ADB_SERVER_SOCKET: "${ADB_SERVER_SOCKET:-tcp:127.0.0.1:5037}"`, "network_mode: host", `command: tunnel --no-autoupdate --url ${CREDIMI_TUNNEL_URL:-http://127.0.0.1:80}`},
		},
		{
			name:     "local runner image",
			vals:     Values{"CREDIMI_RUNNER_TYPE": "android_phone", "RUNNER_IMAGE": "credimi-runner-phone:latest", "RUNNER_IMAGE_PULL_POLICY": "never"},
			contains: []string{"image: credimi-runner-phone:latest", "pull_policy: never"},
		},
		{
			name:     "wifi container",
			vals:     Values{"CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_RUNNER_DEVICE_MODE": "wifi", "CREDIMI_RUNNER_WIFI_IP": "192.168.1.10"},
			contains: []string{`"${CREDIMI_RUNNER_WIFI_IP}:${CREDIMI_RUNNER_WIFI_PORT:-5555}"`, "network_mode: host"},
		},
		{
			name:     "emulator",
			vals:     Values{"CREDIMI_RUNNER_TYPE": "android_emulator"},
			contains: []string{"--emulator", "/dev/kvm:/dev/kvm", "${HOST_AVD_GOLDEN_PATH}:/avd-golden", `caddy.reverse_proxy: "{{upstreams ${RUNNER_PORT:-8050}}}"`, "networks:\n      - ingress"},
		},
		{
			name:     "redroid known hosts",
			vals:     Values{"CREDIMI_RUNNER_TYPE": "redroid", "AVDCTL_SSH_TARGET": "box", "AVDCTL_SSH_KNOWN_HOSTS_PATH": "/tmp/known_hosts"},
			contains: []string{"--no-device", "${AVDCTL_SSH_KNOWN_HOSTS_PATH}:/root/.ssh/known_hosts:ro", `caddy.reverse_proxy: "{{upstreams ${RUNNER_PORT:-8050}}}"`},
		},
		{
			name:     "emulator custom runner port",
			vals:     Values{"CREDIMI_RUNNER_TYPE": "android_emulator", "RUNNER_PORT": "8052"},
			contains: []string{`PORT: "${RUNNER_PORT:-8050}"`, `- "${RUNNER_PORT:-8050}"`, `caddy.reverse_proxy: "{{upstreams ${RUNNER_PORT:-8050}}}"`},
		},
		{
			name:     "host edge service target",
			vals:     Values{"CREDIMI_RUNNER_BACKEND": "host"},
			contains: []string{`caddy.reverse_proxy: "host.docker.internal:${RUNNER_PORT:-8050}"`},
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
			if tt.name == "emulator" && strings.Contains(content, "command: tunnel --no-autoupdate --url ${CREDIMI_TUNNEL_URL:-http://127.0.0.1:80}") {
				t.Fatalf("emulator tunnel should use caddy network URL, got:\n%s", content)
			}
		})
	}
}

func TestRunnerAPIReachableFromHost(t *testing.T) {
	tests := []struct {
		name string
		vals Values
		goos string
		want bool
	}{
		{"host backend", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_RUNNER_TYPE": "ios_simulator"}, "darwin", true},
		{"linux phone auto host network", Values{"CREDIMI_RUNNER_TYPE": "android_phone"}, "linux", true},
		{"linux emulator bridge", Values{"CREDIMI_RUNNER_TYPE": "android_emulator"}, "linux", false},
		{"linux manual container host network", Values{"CREDIMI_RUNNER_TYPE": "android_emulator", "CREDIMI_SERVICE_MODE": "manual"}, "linux", true},
		{"darwin container not host reachable", Values{"CREDIMI_RUNNER_BACKEND": "container", "CREDIMI_RUNNER_TYPE": "android_phone"}, "darwin", false},
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
		{"host backend auto", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_SERVICE_MODE": "auto", "CREDIMI_RUNNER_TYPE": "ios_simulator"}, "darwin", false},
		{"host backend managed", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_SERVICE_MODE": "cloudflare-managed", "CREDIMI_RUNNER_TYPE": "ios_simulator"}, "darwin", false},
		{"host backend manual", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_SERVICE_MODE": "manual", "CREDIMI_RUNNER_TYPE": "ios_simulator"}, "darwin", true},
		{"linux phone container auto", Values{"CREDIMI_RUNNER_TYPE": "android_phone"}, "linux", false},
		{"linux phone container manual", Values{"CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_SERVICE_MODE": "manual"}, "linux", false},
		{"linux emulator container auto", Values{"CREDIMI_RUNNER_TYPE": "android_emulator"}, "linux", false},
		{"darwin default host auto", Values{}, "darwin", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RunnerReadinessRequiredBeforeRegistration(tt.vals, tt.goos); got != tt.want {
				t.Fatalf("RunnerReadinessRequiredBeforeRegistration = %v, want %v", got, tt.want)
			}
		})
	}
}
