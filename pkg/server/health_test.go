package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
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

func TestCheckManagedInventoryDoesNotRequireADBForIOSOrDisabledAndroid(t *testing.T) {
	for _, test := range []struct {
		name    string
		devices []dashboardruntime.DeviceRuntimeConfig
	}{
		{
			name: "ios simulator",
			devices: []dashboardruntime.DeviceRuntimeConfig{{
				Type: "ios_simulator", Enabled: true,
			}},
		},
		{
			name: "disabled android",
			devices: []dashboardruntime.DeviceRuntimeConfig{{
				Type: "android_emulator", Enabled: false,
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			svc := newTestHealthService("", errors.New("adb must not be called"))
			svc.RuntimeConfig = func() (dashboardruntime.RunnerRuntimeConfig, error) {
				return dashboardruntime.RunnerRuntimeConfig{Devices: test.devices}, nil
			}
			svc.runADB = func(context.Context, string, ...string) ([]byte, error) {
				calls++
				return nil, errors.New("adb must not be called")
			}

			result, err := svc.Check(context.Background())
			if err != nil {
				t.Fatalf("Check() error = %v", err)
			}
			if result.Status != "connected" || calls != 0 {
				t.Fatalf("result = %#v, adb calls = %d", result, calls)
			}
		})
	}
}

func TestCheckManagedInventoryRequiresADBForEnabledAndroid(t *testing.T) {
	for _, deviceType := range []string{"android_phone", "android_emulator", "redroid"} {
		t.Run(deviceType, func(t *testing.T) {
			calls := 0
			var command string
			svc := newTestHealthService("", errors.New("adb unavailable"))
			svc.RuntimeConfig = func() (dashboardruntime.RunnerRuntimeConfig, error) {
				return dashboardruntime.RunnerRuntimeConfig{Devices: []dashboardruntime.DeviceRuntimeConfig{{
					Type: deviceType, Enabled: true,
				}}}, nil
			}
			svc.resolveADB = func() (string, error) { return "/resolved/adb", nil }
			svc.runADB = func(_ context.Context, cmd string, _ ...string) ([]byte, error) {
				calls++
				command = cmd
				return nil, errors.New("adb unavailable")
			}

			if _, err := svc.Check(context.Background()); err == nil {
				t.Fatal("Check() error = nil, want ADB failure")
			}
			if calls != 1 {
				t.Fatalf("adb calls = %d, want 1", calls)
			}
			if command != "/resolved/adb" {
				t.Fatalf("adb command = %q, want resolved absolute path", command)
			}
		})
	}
}
