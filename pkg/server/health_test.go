package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	genhealth "github.com/forkbombeu/credimi-runner/pkg/gen/health"
)

func newTestHealthService(output string, err error) *HealthService {
	svc := &HealthService{
		runADB: func(_ context.Context, cmd string, args ...string) ([]byte, error) {
			return []byte(output), err
		},
	}
	return svc
}

func TestNewHealthServiceDefaults(t *testing.T) {
	service := NewHealthService()
	if service.adbPath != "adb" || service.runADB == nil {
		t.Fatalf("service = %#v", service)
	}
	if _, err := service.runADB(context.Background(), os.Args[0], "-test.run=^$"); err != nil {
		t.Fatalf("default command runner: %v", err)
	}
}

func TestCheck_NoDevices(t *testing.T) {
	svc := newTestHealthService("List of devices attached\n", nil)

	res, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != "connected" {
		t.Errorf("expected status 'connected', got %q", res.Status)
	}

	if len(res.Devices) != 0 {
		t.Errorf("expected 0 devices, got %d", len(res.Devices))
	}
}

func TestCheck_WithDevices(t *testing.T) {
	output := `List of devices attached
emulator-5554 device product:sdk_google_phone_x86 model:Android_SDK built-in device:generic transport_id:1
`
	svc := newTestHealthService(output, nil)

	res, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(res.Devices))
	}

	device := res.Devices[0]
	if *device.Serial != "emulator-5554" {
		t.Errorf("unexpected serial: %s", *device.Serial)
	}
	if *device.Product != "sdk_google_phone_x86" {
		t.Errorf("unexpected product: %s", *device.Product)
	}
	if *device.Model != "Android_SDK" {
		t.Errorf("unexpected model: %s", *device.Model)
	}
	if *device.Device != "generic" {
		t.Errorf("unexpected device: %s", *device.Device)
	}
	if *device.TransportID != "1" {
		t.Errorf("unexpected transport_id: %s", *device.TransportID)
	}
}

func TestCheck_ADBError(t *testing.T) {
	svc := newTestHealthService("", errors.New("adb failed"))

	_, err := svc.Check(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var apiErr *genhealth.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected health.APIError, got %T", err)
	}
	if apiErr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, apiErr.Code)
	}
	if apiErr.Name != "service_unavailable" {
		t.Errorf("expected name %q, got %q", "service_unavailable", apiErr.Name)
	}
	if !strings.Contains(apiErr.Message, "adb failed") {
		t.Errorf("unexpected message: %q", apiErr.Message)
	}
}
