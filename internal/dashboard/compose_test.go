package dashboard

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func obsolete_TestNormalizeWizardValues(t *testing.T) {
	home := homeDir()
	tests := []struct {
		name string
		in   map[string]string
		want map[string]string
	}{
		{
			name: "android phone wifi derives serial",
			in: map[string]string{
				"CREDIMI_RUNNER_TYPE":        "android_phone",
				"CREDIMI_RUNNER_BACKEND":     "container",
				"CREDIMI_RUNNER_DEVICE_MODE": "wifi",
				"CREDIMI_RUNNER_WIFI_IP":     "192.168.1.20",
			},
			want: map[string]string{
				"CREDIMI_CONTAINER_MODE":   "wifi",
				"CREDIMI_RUNNER_SERIAL":    "192.168.1.20:5555",
				"CREDIMI_RUNNER_WIFI_PORT": "5555",
				"RUNNER_IMAGE":             defaultPhoneImage,
				"RUNNER_PORT":              "8050",
				"RUNNER_HOST":              "127.0.0.1",
			},
		},
		{
			name: "android phone usb clears wifi fields",
			in: map[string]string{
				"CREDIMI_RUNNER_TYPE":        "android_phone",
				"CREDIMI_RUNNER_DEVICE_MODE": "usb",
				"CREDIMI_RUNNER_WIFI_IP":     "192.168.1.20",
				"CREDIMI_RUNNER_WIFI_PORT":   "38349",
			},
			want: map[string]string{
				"CREDIMI_CONTAINER_MODE":   "usb",
				"CREDIMI_RUNNER_WIFI_IP":   "",
				"CREDIMI_RUNNER_WIFI_PORT": "",
				"RUNNER_IMAGE":             defaultPhoneImage,
			},
		},
		{
			name: "android emulator defaults paths",
			in:   map[string]string{"CREDIMI_RUNNER_TYPE": "android_emulator"},
			want: map[string]string{
				"CREDIMI_CONTAINER_MODE": "emulator",
				"CREDIMI_RUNNER_SERIAL":  "",
				"RUNNER_IMAGE":           defaultEmulatorImage,
				"ANDROID_KEYS_DIR":       filepath.Join(home, ".android"),
				"HOST_AVD_HOME_PATH":     filepath.Join(home, ".android", "avd"),
				"HOST_AVD_GOLDEN_PATH":   filepath.Join(home, "avd-golden"),
				"BASE_NAME":              "credimi",
				"GOLDEN_PATH":            "/avd-golden/credimi-golden",
			},
		},
		{
			name: "android emulator forces container backend",
			in: map[string]string{
				"CREDIMI_RUNNER_TYPE":    "android_emulator",
				"CREDIMI_RUNNER_BACKEND": "host",
			},
			want: map[string]string{
				"CREDIMI_RUNNER_BACKEND": "container",
				"CREDIMI_CONTAINER_MODE": "emulator",
			},
		},
		{
			name: "redroid forces no device",
			in: map[string]string{
				"CREDIMI_RUNNER_TYPE":    "redroid",
				"CREDIMI_RUNNER_WIFI_IP": "192.168.1.30",
			},
			want: map[string]string{
				"CREDIMI_RUNNER_DEVICE_MODE": "no_device",
				"CREDIMI_CONTAINER_MODE":     "no_device",
				"CREDIMI_RUNNER_SERIAL":      "192.168.1.30:5555",
				"CREDIMI_RUNNER_WIFI_PORT":   "5555",
				"REDROID_DATA_DIR":           "/home/credimi/redroid-data",
				"REDROID_DATA_TAR":           "/home/credimi/redroid-data.tar",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizeWizardValues(tt.in)
			for key, want := range tt.want {
				if got := tt.in[key]; got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func obsolete_TestWriteComposeFile(t *testing.T) {
	tests := []struct {
		name     string
		vals     map[string]string
		contains []string
	}{
		{
			name: "wifi runner uses bridge network and wifi command",
			vals: map[string]string{
				"CREDIMI_RUNNER_TYPE":        "android_phone",
				"CREDIMI_RUNNER_DEVICE_MODE": "wifi",
				"CREDIMI_RUNNER_WIFI_IP":     "192.168.1.20",
			},
			contains: []string{`"${CREDIMI_DEVICE_1_WIFI_IP}:${CREDIMI_DEVICE_1_WIFI_PORT:-5555}"`, `caddy.reverse_proxy: "{{upstreams ${RUNNER_PORT:-8050}}}"`},
		},
		{
			name:     "emulator runner mounts avd paths",
			vals:     map[string]string{"CREDIMI_RUNNER_TYPE": "android_emulator"},
			contains: []string{"--emulator", "/dev/kvm:/dev/kvm", "${CREDIMI_DEVICE_1_HOST_AVD_HOME_PATH}:/avd-home"},
		},
		{
			name:     "no device runner uses no device flag",
			vals:     map[string]string{"CREDIMI_RUNNER_TYPE": "redroid"},
			contains: []string{"--no-device", "runner_host:", "tunnel_named:"},
		},
		{
			name:     "usb runner uses the host adb namespace",
			vals:     map[string]string{"CREDIMI_RUNNER_TYPE": "android_phone"},
			contains: []string{"--host-adb", "--usb", "network_mode: host"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteComposeFile(dir, tt.vals); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "docker-compose.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			got := string(data)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Fatalf("compose file missing %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestComposeServices(t *testing.T) {
	tests := []struct {
		name string
		vals map[string]string
		want []string
	}{
		{"container auto", map[string]string{}, []string{"runner", "caddy", "tunnel"}},
		{"container managed", map[string]string{"CREDIMI_SERVICE_MODE": "cloudflare-managed"}, []string{"runner", "caddy", "tunnel_named"}},
		{"container manual", map[string]string{"CREDIMI_SERVICE_MODE": "manual"}, []string{"runner"}},
		{"unknown service mode", map[string]string{"CREDIMI_SERVICE_MODE": "custom"}, []string{"runner", "caddy", "tunnel"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComposeServices(tt.vals)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("ComposeServices = %v, want %v", got, tt.want)
			}
		})
	}

	if runtime.GOOS == "darwin" {
		tests := []struct {
			name string
			vals map[string]string
			want []string
		}{
			{"host auto", map[string]string{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_RUNNER_TYPE": "ios_simulator"}, []string{"runner_host", "caddy", "tunnel"}},
			{"host manual", map[string]string{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_RUNNER_TYPE": "ios_simulator", "CREDIMI_SERVICE_MODE": "manual"}, nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := ComposeServices(tt.vals)
				if strings.Join(got, ",") != strings.Join(tt.want, ",") {
					t.Fatalf("ComposeServices = %v, want %v", got, tt.want)
				}
			})
		}
	}
}

func TestComposeNetworkHelpers(t *testing.T) {
	autoUSB := map[string]string{"CREDIMI_CONTAINER_MODE": "usb"}
	if hostNetworkForTunnel(autoUSB) {
		t.Fatal("container tunnel should not use host networking")
	}
	if got := tunnelURL(autoUSB); got != "http://caddy:80" {
		t.Fatalf("tunnelURL = %q", got)
	}
	if !strings.Contains(caddyNetworkBlock(autoUSB), "networks:") {
		t.Fatal("expected caddy bridge network block")
	}

	manual := map[string]string{"CREDIMI_SERVICE_MODE": "manual"}
	block := runnerConnectivityBlock(manual)
	if !strings.Contains(block, "ports:") || strings.Contains(block, "127.0.0.1") {
		t.Fatalf("expected published bridge connectivity block, got %q", block)
	}
	auto := runnerConnectivityBlock(map[string]string{})
	if !strings.Contains(auto, "127.0.0.1") {
		t.Fatalf("expected loopback-only automatic connectivity block, got %q", auto)
	}

	managed := map[string]string{"CREDIMI_SERVICE_MODE": "cloudflare-managed"}
	if hostNetworkForTunnel(managed) {
		t.Fatal("managed tunnels should not use host network")
	}
	if got := tunnelURL(managed); got != "http://caddy:80" {
		t.Fatalf("managed tunnelURL = %q", got)
	}
	if !strings.Contains(tunnelNetworkBlock(managed), "networks:") {
		t.Fatal("expected tunnel network block")
	}
}

func TestComposeTinyHelpers(t *testing.T) {
	vals := map[string]string{"A": " value ", "EMPTY": ""}
	if got := val(vals, "A"); got != "value" {
		t.Fatalf("val trimmed = %q", got)
	}
	if got := valDefault(vals, "EMPTY", "fallback"); got != "fallback" {
		t.Fatalf("valDefault empty = %q", got)
	}
	defaultIfEmpty(vals, "EMPTY", "filled")
	if got := vals["EMPTY"]; got != "filled" {
		t.Fatalf("defaultIfEmpty = %q", got)
	}
	if got := containerMode("host", "usb"); got != "" {
		t.Fatalf("host containerMode = %q", got)
	}
	if got := containerMode("container", "usb"); got != "usb" {
		t.Fatalf("container containerMode = %q", got)
	}
}
