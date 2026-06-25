package runtime

import (
	"path/filepath"
	"testing"
)

func TestNormalizeLinuxDefault(t *testing.T) {
	values, err := NormalizeValues(Values{}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_BACKEND"] != DefaultContainerBackend || values["CREDIMI_RUNNER_TYPE"] != "android_phone" || values["CREDIMI_RUNNER_DEVICE_MODE"] != "usb" {
		t.Fatalf("normalized = %#v", values)
	}
}

func TestNormalizeDarwinDefault(t *testing.T) {
	values, err := NormalizeValues(Values{}, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_BACKEND"] != DefaultHostBackend || values["CREDIMI_RUNNER_TYPE"] != "ios_simulator" {
		t.Fatalf("normalized = %#v", values)
	}
}

func TestNormalizeLinuxRejectsIOSSimulator(t *testing.T) {
	_, err := NormalizeValues(Values{"CREDIMI_RUNNER_TYPE": "ios_simulator"}, "linux")
	if err == nil {
		t.Fatal("expected linux to reject ios_simulator")
	}
}

func TestNormalizeAndroidWiFi(t *testing.T) {
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

func TestNormalizeAndroidUSBClearsWiFi(t *testing.T) {
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

func TestNormalizeAndroidEmulator(t *testing.T) {
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

func TestNormalizeHostAndroidEmulator(t *testing.T) {
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_TYPE":    "android_emulator",
		"CREDIMI_RUNNER_BACKEND": DefaultHostBackend,
	}, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if values["HOST_AVD_HOME_PATH"] != "" || values["HOST_AVD_GOLDEN_PATH"] != "" {
		t.Fatalf("normalized = %#v", values)
	}
}

func TestNormalizeRedroid(t *testing.T) {
	values, err := NormalizeValues(Values{
		"CREDIMI_RUNNER_TYPE":    "redroid",
		"CREDIMI_RUNNER_WIFI_IP": "10.0.0.1",
		"AVDCTL_SSH_TARGET":      "host",
		"REDROID_DATA_DIR":       "/data",
		"REDROID_DATA_TAR":       "/data.tar",
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDIMI_RUNNER_DEVICE_MODE"] != "no_device" || values["CREDIMI_RUNNER_SERIAL"] != "10.0.0.1:5555" || values["AVDCTL_SSH_TARGET"] != "host" {
		t.Fatalf("normalized = %#v", values)
	}
}

func TestNormalizeSwitchRedroidToAndroidPhoneClearsFields(t *testing.T) {
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

func TestDefaultValuesIncludeHomePaths(t *testing.T) {
	values := DefaultValues()
	if values["ANDROID_KEYS_DIR"] != "" && filepath.Base(values["HOST_AVD_HOME_PATH"]) != "avd" {
		t.Fatalf("defaults = %#v", values)
	}
}
