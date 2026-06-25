package dashboard

import (
	"strings"
	"testing"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

func TestPageDataWorkerAndNetworkViewModels(t *testing.T) {
	cfg := &Config{values: map[string]string{
		"CREDIMI_URL":                 "https://credimi.example",
		"CREDIMI_USER_API_KEY":        "user-key",
		"CREDIMI_SERVICE_MODE":        "cloudflare-managed",
		"RUNNER_DOMAIN":               "runner.example",
		"RUNNER_HOST":                 "0.0.0.0",
		"RUNNER_PORT":                 "8050",
		"CREDIMI_RUNNER_ORGANIZATION": "xy",
	}}
	d := PageData{
		Active: "overview",
		Runner: cfg,
		Snapshot: Snapshot{Services: []Service{
			{ID: "runner", Status: Online},
			{ID: "temporal", Status: Online},
		}},
		Workers: []Worker{
			{ID: "runner-mr", Env: "runner", Status: Online},
		},
	}

	if !d.ServicesAllUp() {
		t.Fatal("expected all services up")
	}
	if got := d.PublicURL(); got != "https://runner.example" {
		t.Fatalf("managed PublicURL = %q", got)
	}

	cfg.values["RUNNER_DOMAIN"] = ""
	if got := d.PublicURL(); got != "https://<runner-domain>" {
		t.Fatalf("managed placeholder PublicURL = %q", got)
	}
	cfg.values["CREDIMI_SERVICE_MODE"] = "manual"
	if got := d.PublicURL(); got != "http://<host-ip>:8050" {
		t.Fatalf("manual PublicURL = %q", got)
	}
	cfg.values["RUNNER_HOST"] = "127.0.0.1"
	if got := d.PublicURL(); got != "http://127.0.0.1:8050" {
		t.Fatalf("manual host PublicURL = %q", got)
	}
	cfg.values["CREDIMI_SERVICE_MODE"] = "auto"
	if got := d.PublicURL(); got != "https://<name>.trycloudflare.com" {
		t.Fatalf("auto PublicURL = %q", got)
	}
	d.Data = map[string]any{"RuntimeStatus": dashboardruntime.RuntimeStatus{PublicURL: "https://runner.example.trycloudflare.com"}}
	if got := d.PublicURL(); got != "https://runner.example.trycloudflare.com" {
		t.Fatalf("runtime PublicURL = %q", got)
	}
	if got := d.RunnerAPIURL(); got != "http://127.0.0.1:8050" {
		t.Fatalf("RunnerAPIURL = %q", got)
	}
	cfg.values["CREDIMI_RUNNER_TYPE"] = "android_phone"
	cfg.values["CREDIMI_RUNNER_DEVICE_MODE"] = "wifi"
	cfg.values["CREDIMI_RUNNER_SERIAL"] = "10.0.0.8:5555"
	if got := d.ConfiguredTargetTitle(); got != "Android phone over Wi-Fi" {
		t.Fatalf("ConfiguredTargetTitle = %q", got)
	}
	if got := d.ConfiguredTargetDetail(); got != "10.0.0.8:5555" {
		t.Fatalf("ConfiguredTargetDetail = %q", got)
	}
}

func TestPageDataFormViewModels(t *testing.T) {
	cfg := &Config{values: map[string]string{
		"CREDIMI_USER_API_KEY":        "test-secret-value-123",
		"CREDIMI_RUNNER_ORGANIZATION": "a",
		"OTEL_ENABLED":                "yes",
	}}
	d := PageData{
		Active: "setup",
		Runner: cfg,
		Data:   map[string]any{"Errors": map[string]string{"CREDIMI_URL": "Required."}},
	}

	if !d.HasErrors() || !d.IsSetup() {
		t.Fatal("expected setup errors")
	}
	field := d.Field("CREDIMI_URL")
	if field.Err != "Required." || field.Key != "CREDIMI_URL" {
		t.Fatalf("Field = %#v", field)
	}
	secret := d.Field("CREDIMI_USER_API_KEY")
	if got := secret.MaskedValue(); strings.Contains(got, "secret123") {
		t.Fatalf("secret was not masked: %q", got)
	}
	if !d.Field("OTEL_ENABLED").Checked() {
		t.Fatal("expected yes bool to be checked")
	}
	selectField := FieldVM{Value: "wifi"}
	if !selectField.Selected("wifi") || selectField.Selected("usb") {
		t.Fatal("Selected returned wrong result")
	}
	if got := string(d.Pretty("<b>prod</b>")); got != "<b>prod</b>" {
		t.Fatalf("Pretty = %q", got)
	}
	if got := d.AvatarInitials(); got != "A" {
		t.Fatalf("single-letter AvatarInitials = %q", got)
	}
	cfg.values["CREDIMI_RUNNER_ORGANIZATION"] = ""
	if got := d.AvatarInitials(); got != "CR" {
		t.Fatalf("default AvatarInitials = %q", got)
	}
	if got := orDash(""); got != "not configured" {
		t.Fatalf("orDash empty = %q", got)
	}
}
