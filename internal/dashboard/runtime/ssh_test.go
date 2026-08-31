package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

func TestDefaultSSHKnownHostsPathUsesHostHome(t *testing.T) {
	t.Setenv(HostHomeEnv, "/home/filippo")
	if got, want := DefaultSSHKnownHostsPath(), "/home/filippo/.ssh/known_hosts"; got != want {
		t.Fatalf("DefaultSSHKnownHostsPath() = %q, want %q", got, want)
	}
}

func TestDefaultSSHKnownHostsPathFallsBackToProcessHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(HostHomeEnv, "")
	t.Setenv("HOME", home)
	if got, want := DefaultSSHKnownHostsPath(), filepath.Join(home, ".ssh", "known_hosts"); got != want {
		t.Fatalf("DefaultSSHKnownHostsPath() = %q, want %q", got, want)
	}
}

func TestEffectiveSSHKnownHostsPathUsesHostDefaultOnlyForSSH(t *testing.T) {
	t.Setenv(HostHomeEnv, "/home/alice")
	if got := EffectiveSSHKnownHostsPath("alice@redroid", ""); got != "/home/alice/.ssh/known_hosts" {
		t.Fatalf("missing SSH known-hosts path = %q", got)
	}
	if got := EffectiveSSHKnownHostsPath("alice@redroid", "/srv/ssh/known_hosts"); got != "/srv/ssh/known_hosts" {
		t.Fatalf("explicit SSH known-hosts path = %q", got)
	}
	if got := EffectiveSSHKnownHostsPath("", ""); got != "" {
		t.Fatalf("local Redroid known-hosts path = %q, want empty", got)
	}
}

func TestTypedRedroidConfigUsesHostDefaultForMissingKnownHosts(t *testing.T) {
	t.Setenv(HostHomeEnv, "/home/alice")
	cfg := runnerconfig.Config{Devices: []runnerconfig.DeviceConfig{{
		Type:    runnerconfig.DeviceRedroid,
		Redroid: &runnerconfig.RedroidConfig{Host: "192.0.2.10", ADBPort: 5555, AVDCTLSSHTarget: "alice@redroid"},
	}}}
	values := ValuesFromTypedConfig(cfg)
	if got := values["CREDIMI_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH"]; got != "/home/alice/.ssh/known_hosts" {
		t.Fatalf("typed config missing known-hosts path = %q", got)
	}
	if got := values["CREDIMI_DEVICE_1_AVDCTL_SSH_ARGS"]; got != "-o UserKnownHostsFile=/home/alice/.ssh/known_hosts" {
		t.Fatalf("derived SSH args = %q", got)
	}
}

func TestAVDCTLSSHArgsUsesCanonicalKnownHostsPath(t *testing.T) {
	if got, want := AVDCTLSSHArgs("/home/filippo/.ssh/known_hosts"), "-o UserKnownHostsFile=/home/filippo/.ssh/known_hosts"; got != want {
		t.Fatalf("AVDCTLSSHArgs() = %q, want %q", got, want)
	}
	if got := AVDCTLSSHArgs(""); got != "" {
		t.Fatalf("AVDCTLSSHArgs(empty) = %q", got)
	}
}

func TestKnownHostsPathRejectsWhitespaceForAVDCTLArgs(t *testing.T) {
	if err := validateKnownHostsPathValue("acme/runner/redroid", "/home/John Doe/.ssh/known_hosts"); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("whitespace path error = %v", err)
	}
	if err := validateKnownHostsPathValue("acme/runner/redroid", "/home/john/.ssh/known_hosts"); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
}

func TestParseRuntimeConfigRejectsWhitespaceKnownHostsPath(t *testing.T) {
	_, err := ParseRuntimeConfig(Values{
		"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID": "acme/runner/redroid", "CREDIMI_DEVICE_1_TYPE": "redroid",
		"CREDIMI_DEVICE_1_MODE": "redroid", "CREDIMI_DEVICE_1_WIFI_IP": "192.0.2.10",
		"CREDIMI_DEVICE_1_WIFI_PORT": "5555", "CREDIMI_DEVICE_1_AVDCTL_SSH_TARGET": "alice@redroid",
		"CREDIMI_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH": "/home/John Doe/.ssh/known_hosts",
	})
	if err == nil || !strings.Contains(err.Error(), "AVDCTL_SSH_KNOWN_HOSTS_PATH") {
		t.Fatalf("ParseRuntimeConfig whitespace error = %v", err)
	}
}
