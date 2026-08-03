package runtime

import (
	"path/filepath"
	"testing"
)

func obsolete_TestNormalizeLinuxDefault(t *testing.T) {
	values, err := NormalizeValues(Values{}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_BACKEND"] != DefaultContainerBackend || values["CREDIMI_RUNNER_TYPE"] != "android_phone" || values["CREDIMI_RUNNER_DEVICE_MODE"] != "usb" {
		t.Fatalf("normalized = %#v", values)
	}
}

func obsolete_TestNormalizeDarwinDefault(t *testing.T) {
	values, err := NormalizeValues(Values{}, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_BACKEND"] != DefaultHostBackend || values["CREDIMI_RUNNER_TYPE"] != "ios_simulator" {
		t.Fatalf("normalized = %#v", values)
	}
}

func obsolete_TestNormalizeLinuxRejectsIOSSimulator(t *testing.T) {
	_, err := NormalizeValues(Values{"CREDIMI_RUNNER_TYPE": "ios_simulator"}, "linux")
	if err == nil {
		t.Fatal("expected linux to reject ios_simulator")
	}
}

func obsolete_TestNormalizeAndroidWiFi(t *testing.T) {
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_TYPE":        "android_phone",
		"CREDIMI_RUNNER_DEVICE_MODE": "wifi",
		"CREDIMI_RUNNER_WIFI_IP":     "192.168.1.10",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_SERIAL"] != "192.168.1.10:5555" || values["CREDIMI_CONTAINER_MODE"] != "wifi" {
		t.Fatalf("normalized = %#v", values)
	}
}

func obsolete_TestNormalizeAndroidUSBClearsWiFi(t *testing.T) {
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_TYPE":        "android_phone",
		"CREDIMI_RUNNER_DEVICE_MODE": "usb",
		"CREDIMI_RUNNER_WIFI_IP":     "192.168.1.10",
		"CREDIMI_RUNNER_WIFI_PORT":   "1234",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_WIFI_IP"] != "" || values["CREDIMI_RUNNER_WIFI_PORT"] != "" {
		t.Fatalf("normalized = %#v", values)
	}
}

func obsolete_TestNormalizeAndroidEmulator(t *testing.T) {
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_TYPE":        "android_emulator",
		"CREDIMI_RUNNER_DEVICE_MODE": "usb",
		"CREDIMI_RUNNER_SERIAL":      "device-1",
		"CREDIMI_RUNNER_WIFI_IP":     "192.168.1.10",
		"CREDIMI_RUNNER_WIFI_PORT":   "5555",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["RUNNER_IMAGE"] != DefaultEmulatorImage || values["CREDIMI_CONTAINER_MODE"] != "emulator" {
		t.Fatalf("normalized = %#v", values)
	}
	if values["CREDIMI_RUNNER_DEVICE_MODE"] != "" || values["CREDIMI_RUNNER_SERIAL"] != "" || values["CREDIMI_RUNNER_WIFI_IP"] != "" || values["CREDIMI_RUNNER_WIFI_PORT"] != "" {
		t.Fatalf("emulator should not keep device connection fields: %#v", values)
	}
}

func obsolete_TestNormalizeHostAndroidEmulator(t *testing.T) {
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_TYPE":    "android_emulator",
		"CREDIMI_RUNNER_BACKEND": DefaultHostBackend,
	}, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_BACKEND"] != DefaultContainerBackend {
		t.Fatalf("normalized backend = %q", values["CREDIMI_RUNNER_BACKEND"])
	}
	if values["HOST_AVD_HOME_PATH"] == "" || values["HOST_AVD_GOLDEN_PATH"] == "" {
		t.Fatalf("normalized = %#v", values)
	}
}

func obsolete_TestNormalizeBackendForRunnerType(t *testing.T) {
	tests := []struct {
		name        string
		values      Values
		goos        string
		wantBackend string
	}{
		{"darwin emulator forces container", Values{"CREDIMI_RUNNER_TYPE": "android_emulator", "CREDIMI_RUNNER_BACKEND": DefaultHostBackend}, "darwin", DefaultContainerBackend},
		{"darwin phone forces container", Values{"CREDIMI_RUNNER_TYPE": "android_phone", "CREDIMI_RUNNER_BACKEND": DefaultHostBackend}, "darwin", DefaultContainerBackend},
		{"darwin redroid forces container", Values{"CREDIMI_RUNNER_TYPE": "redroid", "CREDIMI_RUNNER_BACKEND": DefaultHostBackend}, "darwin", DefaultContainerBackend},
		{"darwin simulator forces host", Values{"CREDIMI_RUNNER_TYPE": "ios_simulator", "CREDIMI_RUNNER_BACKEND": DefaultContainerBackend}, "darwin", DefaultHostBackend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, err := NormalizeValues(tt.values, tt.goos)
			if err != nil {
				t.Fatal(err)
			}
			if values["CREDIMI_RUNNER_BACKEND"] != tt.wantBackend {
				t.Fatalf("backend = %q, want %q", values["CREDIMI_RUNNER_BACKEND"], tt.wantBackend)
			}
		})
	}
}

func obsolete_TestNormalizeRedroid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_TYPE":    "redroid",
		"CREDIMI_RUNNER_WIFI_IP": "10.0.0.1",
		"AVDCTL_SSH_TARGET":      "credimi@host",
		"AVDCTL_SUDO":            "no",
		"AVDCTL_SUDO_PASSWORD":   "unused",
		"REDROID_DATA_DIR":       "/data",
		"REDROID_DATA_TAR":       "/data.tar",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_DEVICE_MODE"] != "no_device" || values["CREDIMI_RUNNER_SERIAL"] != "10.0.0.1:5555" || values["CREDIMI_RUNNER_WIFI_IP"] != "10.0.0.1" || values["CREDIMI_RUNNER_WIFI_PORT"] != "5555" {
		t.Fatalf("normalized Redroid endpoint = %#v", values)
	}
	if values["AVDCTL_SSH_TARGET"] != "credimi@host" || values["AVDCTL_SSH_KNOWN_HOSTS_PATH"] != filepath.Join(home, ".ssh", "known_hosts") || values["AVDCTL_SUDO"] != "false" || values["AVDCTL_SUDO_PASSWORD"] != "" {
		t.Fatalf("normalized Redroid SSH = %#v", values)
	}
}

func obsolete_TestNormalizeRedroidWithoutSSHClearsRemoteConfig(t *testing.T) {
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_TYPE":         "redroid",
		"CREDIMI_RUNNER_WIFI_IP":      "10.0.0.2",
		"AVDCTL_SSH_PASSWORD":         "unused",
		"AVDCTL_SSH_KNOWN_HOSTS_PATH": "/tmp/known_hosts",
		"AVDCTL_SUDO":                 "true",
		"AVDCTL_SUDO_PASSWORD":        "unused",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["AVDCTL_SSH_TARGET"] != "" || values["AVDCTL_SSH_PASSWORD"] != "" || values["AVDCTL_SSH_KNOWN_HOSTS_PATH"] != "" || values["AVDCTL_SUDO"] != "false" || values["AVDCTL_SUDO_PASSWORD"] != "" {
		t.Fatalf("normalized = %#v", values)
	}
}

func obsolete_TestNormalizeSwitchRedroidToAndroidPhoneClearsFields(t *testing.T) {
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_TYPE":        "android_phone",
		"CREDIMI_RUNNER_DEVICE_MODE": "usb",
		"AVDCTL_SSH_TARGET":          "host",
		"REDROID_DATA_DIR":           "/data",
		"REDROID_DATA_TAR":           "/data.tar",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["AVDCTL_SSH_TARGET"] != "" || values["REDROID_DATA_DIR"] != "" || values["REDROID_DATA_TAR"] != "" {
		t.Fatalf("normalized = %#v", values)
	}
}

func TestNormalizeOTELDisabledClearsEndpoint(t *testing.T) {
	values, err := NormalizeValues(Values{
		"OTEL_ENABLED":                "false",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://example.test",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["OTEL_EXPORTER_OTLP_ENDPOINT"] != "" {
		t.Fatalf("normalized = %#v", values)
	}
}

func TestNormalizeDerivesRunnerIdentityFromExistingID(t *testing.T) {
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_ID": "/acme-labs/lab-phone",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_NAME"] != "lab-phone" || values["CREDIMI_RUNNER_ORGANIZATION"] != "acme-labs" {
		t.Fatalf("normalized = %#v", values)
	}
}

func TestNormalizeRunnerIdentityKeepsExplicitValues(t *testing.T) {
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_ID":           "/acme-labs/lab-phone",
		"CREDIMI_RUNNER_NAME":         "Display Phone",
		"CREDIMI_RUNNER_ORGANIZATION": "other-org",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_NAME"] != "Display Phone" || values["CREDIMI_RUNNER_ORGANIZATION"] != "other-org" {
		t.Fatalf("normalized = %#v", values)
	}
}

func obsolete_TestDefaultValuesIncludeHomePaths(t *testing.T) {
	values := DefaultValues()
	if values["RUNNER_IMAGE_PULL_POLICY"] != "always" {
		t.Fatalf("RUNNER_IMAGE_PULL_POLICY = %q", values["RUNNER_IMAGE_PULL_POLICY"])
	}
	if values["ANDROID_KEYS_DIR"] != "" && filepath.Base(values["HOST_AVD_HOME_PATH"]) != "avd" {
		t.Fatalf("defaults = %#v", values)
	}
}

func obsolete_TestNormalizeRunnerImagePullPolicy(t *testing.T) {
	values, err := NormalizeValues(Values{"RUNNER_IMAGE_PULL_POLICY": "never"}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["RUNNER_IMAGE_PULL_POLICY"] != "never" {
		t.Fatalf("RUNNER_IMAGE_PULL_POLICY = %q", values["RUNNER_IMAGE_PULL_POLICY"])
	}
	if _, err := NormalizeValues(Values{"RUNNER_IMAGE_PULL_POLICY": "sometimes"}, "linux"); err == nil {
		t.Fatal("NormalizeValues should reject an unsupported runner image pull policy")
	}
}

func TestNormalizeHelperFunctions(t *testing.T) {
	if got := defaultServiceBackend("darwin"); got != DefaultHostBackend {
		t.Fatalf("defaultServiceBackend(darwin) = %q", got)
	}
	if !defaultYesNoChoice("yes", false) || defaultYesNoChoice("no", true) {
		t.Fatal("defaultYesNoChoice returned unexpected result")
	}
	if got := normalizeServiceMode("named"); got != "cloudflare-managed" {
		t.Fatalf("normalizeServiceMode = %q", got)
	}
	if got := resolvedRunnerPublicURL(Values{"CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://manual.example"}, ""); got != "https://manual.example" {
		t.Fatalf("resolvedRunnerPublicURL manual = %q", got)
	}
	if got := resolvedRunnerPublicURL(Values{"CREDIMI_SERVICE_MODE": "cloudflare-managed", "RUNNER_DOMAIN": "runner.example"}, ""); got != "https://runner.example" {
		t.Fatalf("resolvedRunnerPublicURL managed = %q", got)
	}
	if got := canonifyPlain(" Test Runner "); got != "test-runner" {
		t.Fatalf("canonifyPlain = %q", got)
	}
}
