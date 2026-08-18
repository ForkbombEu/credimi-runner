package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := validateKnownHostsPathValue("acme/runner/redroid", "/home/John\tDoe/.ssh/known_hosts"); err == nil {
		t.Fatal("tabbed known-hosts path was accepted")
	}
	if err := validateKnownHostsPathValue("acme/runner/redroid", "/home/john/.ssh/known_hosts"); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
}

func TestParseRuntimeConfigRejectsWhitespaceKnownHostsPath(t *testing.T) {
	_, err := ParseRuntimeConfig(Values{
		"CREDIMI_RUNNER_ID":                            "acme/runner",
		"CREDIMI_DEVICE_COUNT":                         "1",
		"CREDIMI_DEVICE_1_ID":                          "acme/runner/redroid",
		"CREDIMI_DEVICE_1_TYPE":                        "redroid",
		"CREDIMI_DEVICE_1_MODE":                        "redroid",
		"CREDIMI_DEVICE_1_WIFI_IP":                     "192.0.2.10",
		"CREDIMI_DEVICE_1_WIFI_PORT":                   "5555",
		"CREDIMI_DEVICE_1_AVDCTL_SSH_TARGET":           "alice@redroid",
		"CREDIMI_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH": "/home/John Doe/.ssh/known_hosts",
	})
	if err == nil || !strings.Contains(err.Error(), "AVDCTL_SSH_KNOWN_HOSTS_PATH") {
		t.Fatalf("ParseRuntimeConfig whitespace error = %v", err)
	}
}

func TestComposeMountsRedroidKnownHostsReadOnly(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "known_hosts_a")
	second := filepath.Join(dir, "known_hosts_b")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("host ssh-ed25519 key\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	values := indexedComposeValues(Values{
		"CREDIMI_RUNNER_TYPE":                          "redroid",
		"CREDIMI_RUNNER_DEVICE_MODE":                   "redroid",
		"CREDIMI_DEVICE_1_WIFI_IP":                     "192.0.2.10",
		"CREDIMI_DEVICE_1_WIFI_PORT":                   "5555",
		"CREDIMI_DEVICE_1_AVDCTL_SSH_TARGET":           "alice@one",
		"CREDIMI_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH": first,
		"CREDIMI_DEVICE_2_ID":                          "acme/runner/two",
		"CREDIMI_DEVICE_2_TYPE":                        "redroid",
		"CREDIMI_DEVICE_2_MODE":                        "redroid",
		"CREDIMI_DEVICE_2_WIFI_IP":                     "192.0.2.11",
		"CREDIMI_DEVICE_2_WIFI_PORT":                   "5556",
		"CREDIMI_DEVICE_2_AVDCTL_SSH_TARGET":           "bob@two",
		"CREDIMI_DEVICE_2_AVDCTL_SSH_KNOWN_HOSTS_PATH": second,
	})
	values["CREDIMI_DEVICE_COUNT"] = "2"
	content, err := ComposeYAML(values, "linux")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{first, second} {
		want := "\"" + path + ":" + path + ":ro"
		if !strings.Contains(content, want) {
			t.Fatalf("compose missing known-hosts mount %q:\n%s", want, content)
		}
	}
}

func TestComposeRejectsMissingRedroidKnownHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-known-hosts")
	values := indexedComposeValues(Values{
		"CREDIMI_RUNNER_TYPE":                          "redroid",
		"CREDIMI_RUNNER_DEVICE_MODE":                   "redroid",
		"CREDIMI_DEVICE_1_WIFI_IP":                     "192.0.2.10",
		"CREDIMI_DEVICE_1_WIFI_PORT":                   "5555",
		"CREDIMI_DEVICE_1_AVDCTL_SSH_TARGET":           "alice@one",
		"CREDIMI_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH": path,
	})
	_, err := ComposeYAML(values, "linux")
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "acme/runner/device") {
		t.Fatalf("ComposeYAML error = %v", err)
	}
}
