package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

// Readiness describes this particular runner process. Unlike /health, this
// endpoint is used by the local lifecycle controller to verify identity.
type Readiness struct {
	Service      string                 `json:"service"`
	RunnerID     string                 `json:"runner_id"`
	BootID       string                 `json:"boot_id"`
	Version      string                 `json:"version"`
	Devices      map[string]DeviceReady `json:"devices,omitempty"`
	DeviceSerial string                 `json:"-"`
	DeviceState  string                 `json:"-"`
}

type DeviceReady struct {
	Serial string `json:"serial,omitempty"`
	State  string `json:"state,omitempty"`
	Ready  bool   `json:"ready"`
}

type ReadinessService struct {
	Environment   func(string) string
	DeviceState   func(string) string
	RuntimeConfig func() (dashboardruntime.RunnerRuntimeConfig, error)
}

func NewReadinessService() *ReadinessService {
	service := &ReadinessService{Environment: os.Getenv, DeviceState: adbDeviceState}
	if strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_CONFIG_DIR")) != "" {
		service.RuntimeConfig = dashboardruntime.RuntimeConfigFromEnvironment
	}
	return service
}

func (s *ReadinessService) Check() Readiness {
	env := s.environment
	devices := s.typedDevices()
	return Readiness{Service: "credimi-runner", RunnerID: strings.TrimSpace(env("CREDIMI_RUNNER_ID")), BootID: strings.TrimSpace(env("CREDIMI_RUNNER_BOOT_ID")), Version: defaultReadinessVersion(env("CREDIMI_RUNNER_VERSION")), Devices: devices}
}

// typedDevices reads the authoritative TOML inventory. Device keys are not
// copied into process-global environment variables, so readiness must not rely
// on the legacy environment representation.
func (s *ReadinessService) typedDevices() map[string]DeviceReady {
	if s.RuntimeConfig == nil {
		return map[string]DeviceReady{}
	}
	inventory, err := s.RuntimeConfig()
	if err != nil {
		return map[string]DeviceReady{}
	}
	devices := make(map[string]DeviceReady, len(inventory.Devices))
	for _, device := range inventory.Devices {
		id := strings.TrimPrefix(strings.TrimSpace(device.ID), "/")
		if id != "" {
			devices[id] = s.deviceReady(device.Type, device.Mode, strconv.FormatBool(device.Enabled), device.Serial)
		}
	}
	return devices
}

func (s *ReadinessService) deviceReady(deviceType, mode, enabled, serial string) DeviceReady {
	serial = strings.TrimSpace(serial)
	required := deviceReadinessRequired(deviceType, mode, enabled)
	state := ""
	if required && serial != "" && s.DeviceState != nil {
		state = s.DeviceState(serial)
	}
	if required && serial == "" {
		state = "missing"
	}
	return DeviceReady{Serial: serial, State: state, Ready: !required || state == "device"}
}

func (s *ReadinessService) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	ready := s.Check()
	w.Header().Set("Content-Type", "application/json")
	if ready.RunnerID == "" || ready.BootID == "" || !allDevicesReady(ready.Devices) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(ready)
}

func allDevicesReady(devices map[string]DeviceReady) bool {
	for _, device := range devices {
		if !device.Ready {
			return false
		}
	}
	return true
}

func deviceReadinessRequired(deviceType, mode, enabled string) bool {
	if value, err := strconv.ParseBool(strings.TrimSpace(enabled)); err == nil && !value {
		return false
	}
	// Emulators and simulators are managed execution targets: their ADB serial is
	// allocated when the target is started, rather than being a host-level
	// prerequisite like a USB or Wi-Fi phone. Requiring one here made every
	// configured emulator report "missing" and consequently offline forever.
	switch strings.TrimSpace(deviceType) {
	case "android_emulator", "ios_simulator", "redroid":
		return false
	}
	mode = strings.TrimSpace(mode)
	return mode != "" && mode != "no_device"
}

func (s *ReadinessService) environment(key string) string {
	if s.Environment != nil {
		return s.Environment(key)
	}
	return os.Getenv(key)
}

func adbDeviceState(serial string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "adb", "-s", serial, "get-state").Output()
	if err != nil {
		return "missing"
	}
	return strings.TrimSpace(string(out))
}

func defaultReadinessVersion(value string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return "unknown"
}
