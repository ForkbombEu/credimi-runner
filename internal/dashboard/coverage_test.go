package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

func TestWriteIndexedDeviceBlocks(t *testing.T) {
	values := map[string]string{
		"CREDIMI_RUNNER_ID":        "acme/runner",
		"CREDIMI_DEVICE_COUNT":     "1",
		"CREDIMI_DEVICE_1_ID":      "acme/runner/pixel",
		"CREDIMI_DEVICE_1_NAME":    "Pixel USB",
		"CREDIMI_DEVICE_1_TYPE":    "android_phone",
		"CREDIMI_DEVICE_1_MODE":    "usb",
		"CREDIMI_DEVICE_1_ENABLED": "true",
		"CREDIMI_DEVICE_1_SERIAL":  "usb-1",
	}

	var output strings.Builder
	writeIndexedDeviceBlocks(&output, values)
	for _, want := range []string{
		"CREDIMI_DEVICE_COUNT=1",
		"# --- Device 1: Pixel USB ---",
		"CREDIMI_DEVICE_1_ID=acme/runner/pixel",
		"CREDIMI_DEVICE_1_SERIAL=usb-1",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("indexed device output missing %q:\n%s", want, output.String())
		}
	}

	output.Reset()
	writeIndexedDeviceBlocks(&output, map[string]string{"CREDIMI_DEVICE_COUNT": "not-a-count"})
	if output.Len() != 0 {
		t.Fatalf("invalid inventory output = %q, want empty", output.String())
	}
}

func TestWriteComposeFileWritesGeneratedComposeConfiguration(t *testing.T) {
	dir := t.TempDir()
	values := map[string]string{
		"CREDIMI_RUNNER_ID":     "acme/runner",
		"CREDIMI_DEVICE_COUNT":  "1",
		"CREDIMI_DEVICE_1_ID":   "acme/runner/pixel",
		"CREDIMI_DEVICE_1_TYPE": "android_phone",
		"CREDIMI_DEVICE_1_MODE": "usb",
	}
	if err := WriteComposeFile(dir, values); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "docker-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "services:") || !strings.Contains(string(content), "runner:") {
		t.Fatalf("unexpected compose file:\n%s", content)
	}
}

func TestFieldVMDisplayHelpers(t *testing.T) {
	secret := FieldVM{Field: Field{Secret: true}, Value: "test_secret_key_12345"}
	if got := secret.MaskedValue(); got == secret.Value {
		t.Fatalf("secret value was not masked: %q", got)
	}
	plain := FieldVM{Value: "visible"}
	if got := plain.MaskedValue(); got != "visible" {
		t.Fatalf("plain value = %q", got)
	}
	if !plain.Selected("visible") || plain.Selected("other") {
		t.Fatal("unexpected selected state")
	}
}

func TestBootstrapConfiguredRuntimeStartsOrRegistersExistingRuntime(t *testing.T) {
	s := newTestServer(t)
	s.manager = nil
	if s.bootstrapConfiguredRuntime() {
		t.Fatal("bootstrap should be skipped without a runtime manager")
	}

	manager := &fakeManager{status: dashboardruntime.RuntimeStatus{RunnerRunning: true}}
	s.manager = manager
	if !s.bootstrapConfiguredRuntime() {
		t.Fatal("bootstrap should queue registration for a running runtime")
	}
	waitForStartup(t, s)

	s = newTestServer(t)
	s.manager = &fakeManager{}
	s.runnerReady = func(context.Context, map[string]string) error { return nil }
	if !s.bootstrapConfiguredRuntime() {
		t.Fatal("bootstrap should queue startup for a stopped runtime")
	}
	waitForStartup(t, s)
}

func waitForStartup(t *testing.T, s *Server) {
	t.Helper()
	s.mu.RLock()
	done := s.startup.done
	s.mu.RUnlock()
	if done == nil {
		t.Fatal("startup operation did not provide a completion signal")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("startup operation did not finish")
	}
}

func TestDeviceEnableRejectsMalformedForm(t *testing.T) {
	s := newTestServer(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/devices/enable", strings.NewReader("device_id=%"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.deviceEnable(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("deviceEnable status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
