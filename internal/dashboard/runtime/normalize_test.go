package runtime

import (
	"path/filepath"
	"testing"
)

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

func TestNormalizeHelperFunctions(t *testing.T) {
	values, err := NormalizeValues(Values{}, "darwin")
	if err != nil {
		t.Fatalf("default backend = %#v, %v", values, err)
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
	for input, want := range map[string]string{"quick": "auto", "direct": "manual", "named": "cloudflare-managed", "auto": "auto", "manual": "manual", "unknown": "auto"} {
		if got := normalizeServiceMode(input); got != want {
			t.Fatalf("normalizeServiceMode(%q) = %q, want %q", input, got, want)
		}
	}
	if got := resolvedRunnerPublicURL(Values{"CREDIMI_SERVICE_MODE": "auto", "RUNNER_PUBLIC_URL": "https://existing.example"}, "https://fallback.example"); got != "https://fallback.example" {
		t.Fatalf("resolvedRunnerPublicURL fallback = %q", got)
	}
}

func TestNormalizeIndexedDarwinGoldenPathUsesHostRoot(t *testing.T) {
	hostRoot := filepath.Join(t.TempDir(), "avd-golden")
	valuesInput := DefaultValues()
	valuesInput["CREDIMI_RUNNER_ID"] = "acme/runner"
	valuesInput["CREDIMI_DEVICE_1_ID"] = "acme/runner/emulator"
	valuesInput["CREDIMI_DEVICE_1_NAME"] = "Emulator"
	valuesInput["CREDIMI_DEVICE_1_MODE"] = "emulator"
	valuesInput["CREDIMI_DEVICE_1_ENABLED"] = "true"
	valuesInput["CREDIMI_DEVICE_COUNT"] = "1"
	valuesInput["CREDIMI_DEVICE_1_TYPE"] = "android_emulator"
	valuesInput["CREDIMI_DEVICE_1_BASE_NAME"] = "credimi"
	valuesInput["CREDIMI_DEVICE_1_GOLDEN_PATH"] = "/avd-golden/credimi-golden"
	valuesInput["CREDIMI_DEVICE_1_HOST_AVD_GOLDEN_PATH"] = hostRoot
	values, err := NormalizeValues(valuesInput, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if got := values["CREDIMI_DEVICE_1_GOLDEN_PATH"]; got != filepath.Join(hostRoot, "credimi-golden") {
		t.Fatalf("normalized Darwin golden path = %q", got)
	}
}

func TestOSUserHomeDir(t *testing.T) {
	if home, err := osUserHomeDir(); err != nil || home == "" {
		t.Fatalf("osUserHomeDir = %q, %v", home, err)
	}
}
