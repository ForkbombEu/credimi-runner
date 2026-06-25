package runtime

import (
	"runtime"
	"strings"
	"testing"
)

func TestComposeServicesByPlan(t *testing.T) {
	tests := []struct {
		name string
		vals Values
		want []string
	}{
		{"container auto", Values{}, []string{"runner", "caddy", "tunnel"}},
		{"container manual", Values{"CREDIMI_SERVICE_MODE": "manual"}, []string{"runner"}},
		{"host manual", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_SERVICE_MODE": "manual"}, nil},
		{"host auto", Values{"CREDIMI_RUNNER_BACKEND": "host"}, []string{"runner_host", "caddy", "tunnel"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, err := NormalizeValues(tt.vals, runtime.GOOS)
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
			contains: []string{"--host-adb", "--usb", `ADB_SERVER_SOCKET: "${ADB_SERVER_SOCKET:-tcp:127.0.0.1:5037}"`, "network_mode: host"},
		},
		{
			name:     "wifi container",
			vals:     Values{"CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_RUNNER_DEVICE_MODE": "wifi", "CREDIMI_RUNNER_WIFI_IP": "192.168.1.10"},
			contains: []string{`"${CREDIMI_RUNNER_WIFI_IP}:${CREDIMI_RUNNER_WIFI_PORT:-5555}"`, "network_mode: host"},
		},
		{
			name:     "emulator",
			vals:     Values{"CREDIMI_RUNNER_TYPE": "android_emulator"},
			contains: []string{"--emulator", "/dev/kvm:/dev/kvm", "${HOST_AVD_GOLDEN_PATH}:/avd-golden"},
		},
		{
			name:     "redroid known hosts",
			vals:     Values{"CREDIMI_RUNNER_TYPE": "redroid", "AVDCTL_SSH_TARGET": "box", "AVDCTL_SSH_KNOWN_HOSTS_PATH": "/tmp/known_hosts"},
			contains: []string{"--no-device", "${AVDCTL_SSH_KNOWN_HOSTS_PATH}:/root/.ssh/known_hosts:ro"},
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
		})
	}
}
