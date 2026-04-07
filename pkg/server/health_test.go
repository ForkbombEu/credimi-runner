package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	genhealth "github.com/forkbombeu/credimi-runner/pkg/gen/health"
)

func newTestHealthService(output string, err error) *HealthService {
	svc := &HealthService{
		goos: "linux",
		runADB: func(cmd string, args ...string) ([]byte, error) {
			return []byte(output), err
		},
	}
	return svc
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

	if len(res.Emulators) != 0 {
		t.Errorf("expected 0 emulators, got %d", len(res.Emulators))
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

	if len(res.Emulators) != 1 {
		t.Fatalf("expected 1 emulator, got %d", len(res.Emulators))
	}

	device := res.Emulators[0]
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

func TestCheck_WithConnectedIPhoneOnMacOS(t *testing.T) {
	svc := &HealthService{
		goos:          "darwin",
		appleCacheTTL: time.Hour,
		now:           time.Now,
		runADB: func(cmd string, args ...string) ([]byte, error) {
			return []byte("List of devices attached\n"), nil
		},
		runXCRun: func(cmd string, args ...string) ([]byte, error) {
			switch args[0] {
			case "xcdevice":
				return []byte(`[
  {
    "available": true,
    "identifier": "ios-device-1",
    "interface": "usb",
    "modelName": "iPhone 15 Pro",
    "name": "Filippo's iPhone",
    "platform": "com.apple.platform.iphoneos",
    "simulator": false
  },
  {
    "available": true,
    "identifier": "ipad-device-1",
    "modelName": "iPad Pro",
    "name": "Filippo's iPad",
    "platform": "com.apple.platform.iphoneos",
    "simulator": false
  }
]`), nil
			case "simctl":
				return []byte(`{
  "devices": {
    "com.apple.CoreSimulator.SimRuntime.iOS-18-0": [
      {
        "isAvailable": true,
        "name": "iPhone 15",
        "state": "Booted",
        "udid": "sim-booted-1"
      },
      {
        "isAvailable": true,
        "name": "iPhone 14",
        "state": "Shutdown",
        "udid": "sim-shutdown-1"
      },
      {
        "isAvailable": true,
        "name": "iPad Pro (13-inch)",
        "state": "Booted",
        "udid": "sim-ipad-1"
      }
    ]
  }
}`), nil
			default:
				t.Fatalf("unexpected xcrun args: %v", args)
				return nil, nil
			}
		},
	}

	svc.refreshAppleDeviceCache()

	res, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Emulators) != 4 {
		t.Fatalf("expected 4 devices, got %d", len(res.Emulators))
	}

	devicesBySerial := make(map[string]*genhealth.DeviceInfo, len(res.Emulators))
	for _, device := range res.Emulators {
		devicesBySerial[*device.Serial] = device
	}

	device, ok := devicesBySerial["ios-device-1"]
	if !ok {
		t.Fatal("missing ios-device-1")
	}
	if *device.Serial != "ios-device-1" {
		t.Errorf("unexpected serial: %s", *device.Serial)
	}
	if *device.Product != "ios-device" {
		t.Errorf("unexpected product: %s", *device.Product)
	}
	if *device.Model != "iPhone 15 Pro" {
		t.Errorf("unexpected model: %s", *device.Model)
	}
	if *device.Device != "Filippo's iPhone" {
		t.Errorf("unexpected device name: %s", *device.Device)
	}
	if *device.TransportID != "usb" {
		t.Errorf("unexpected transport: %s", *device.TransportID)
	}

	attachedTablet, ok := devicesBySerial["ipad-device-1"]
	if !ok {
		t.Fatal("missing ipad-device-1")
	}
	if *attachedTablet.Serial != "ipad-device-1" {
		t.Errorf("unexpected tablet serial: %s", *attachedTablet.Serial)
	}
	if *attachedTablet.Product != "ios-device" {
		t.Errorf("unexpected tablet product: %s", *attachedTablet.Product)
	}
	if *attachedTablet.Model != "iPad Pro" {
		t.Errorf("unexpected tablet model: %s", *attachedTablet.Model)
	}

	simulator, ok := devicesBySerial["sim-booted-1"]
	if !ok {
		t.Fatal("missing sim-booted-1")
	}
	if *simulator.Serial != "sim-booted-1" {
		t.Errorf("unexpected simulator serial: %s", *simulator.Serial)
	}
	if *simulator.State != "booted" {
		t.Errorf("unexpected simulator state: %s", *simulator.State)
	}
	if *simulator.Product != "ios-simulator" {
		t.Errorf("unexpected simulator product: %s", *simulator.Product)
	}
	if *simulator.Model != "iPhone 15" {
		t.Errorf("unexpected simulator model: %s", *simulator.Model)
	}
	if *simulator.Device != "simulator" {
		t.Errorf("unexpected simulator device: %s", *simulator.Device)
	}

	tabletSimulator, ok := devicesBySerial["sim-ipad-1"]
	if !ok {
		t.Fatal("missing sim-ipad-1")
	}
	if *tabletSimulator.Serial != "sim-ipad-1" {
		t.Errorf("unexpected tablet simulator serial: %s", *tabletSimulator.Serial)
	}
	if *tabletSimulator.Product != "ios-simulator" {
		t.Errorf("unexpected tablet simulator product: %s", *tabletSimulator.Product)
	}
	if *tabletSimulator.Model != "iPad Pro (13-inch)" {
		t.Errorf("unexpected tablet simulator model: %s", *tabletSimulator.Model)
	}
}

func TestCheck_IgnoresIPhoneProbeErrorOnMacOS(t *testing.T) {
	svc := &HealthService{
		goos:          "darwin",
		appleCacheTTL: time.Hour,
		now:           time.Now,
		runADB: func(cmd string, args ...string) ([]byte, error) {
			return []byte(`List of devices attached
emulator-5554 device product:sdk_google_phone_x86 model:Android_SDK built-in device:generic transport_id:1
`), nil
		},
		runXCRun: func(cmd string, args ...string) ([]byte, error) {
			return nil, errors.New("xcrun failed")
		},
	}

	res, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Emulators) != 1 {
		t.Fatalf("expected 1 device, got %d", len(res.Emulators))
	}
	if *res.Emulators[0].Serial != "emulator-5554" {
		t.Errorf("unexpected serial: %s", *res.Emulators[0].Serial)
	}
}

func TestCheck_DoesNotBlockOnAppleProbeOnMacOS(t *testing.T) {
	blockProbe := make(chan struct{})

	svc := &HealthService{
		goos:          "darwin",
		appleCacheTTL: time.Hour,
		now:           time.Now,
		runADB: func(cmd string, args ...string) ([]byte, error) {
			return []byte(`List of devices attached
emulator-5554 device product:sdk_google_phone_x86 model:Android_SDK built-in device:generic transport_id:1
`), nil
		},
		runXCRun: func(cmd string, args ...string) ([]byte, error) {
			<-blockProbe
			return nil, errors.New("probe cancelled")
		},
	}

	resultCh := make(chan *genhealth.CheckResult, 1)
	errCh := make(chan error, 1)

	go func() {
		res, err := svc.Check(context.Background())
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- res
	}()

	select {
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case res := <-resultCh:
		if len(res.Emulators) != 1 {
			t.Fatalf("expected 1 device, got %d", len(res.Emulators))
		}
		if *res.Emulators[0].Serial != "emulator-5554" {
			t.Errorf("unexpected serial: %s", *res.Emulators[0].Serial)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("health check blocked on Apple probe")
	}

	close(blockProbe)
}
