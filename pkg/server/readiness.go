package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
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
	Environment func(string) string
	DeviceState func(string) string
}

func NewReadinessService() *ReadinessService {
	return &ReadinessService{Environment: os.Getenv, DeviceState: adbDeviceState}
}

func (s *ReadinessService) Check() Readiness {
	env := s.environment
	devices := make(map[string]DeviceReady)
	count, _ := strconv.Atoi(strings.TrimSpace(env("CREDIMI_DEVICE_COUNT")))
	for index := 1; index <= count; index++ {
		prefix := fmt.Sprintf("CREDIMI_DEVICE_%d_", index)
		id := strings.TrimPrefix(strings.TrimSpace(env(prefix+"ID")), "/")
		if id == "" {
			continue
		}
		serial := strings.TrimSpace(env(prefix + "SERIAL"))
		required := deviceReadinessRequired(env(prefix+"TYPE"), env(prefix+"MODE"), env(prefix+"ENABLED"))
		state := ""
		if required && serial != "" && s.DeviceState != nil {
			state = s.DeviceState(serial)
		}
		if required && serial == "" {
			state = "missing"
		}
		devices[id] = DeviceReady{Serial: serial, State: state, Ready: !required || state == "device"}
	}
	return Readiness{Service: "credimi-runner", RunnerID: strings.TrimSpace(env("CREDIMI_RUNNER_ID")), BootID: strings.TrimSpace(env("CREDIMI_RUNNER_BOOT_ID")), Version: defaultReadinessVersion(env("CREDIMI_RUNNER_VERSION")), Devices: devices}
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
	mode = strings.TrimSpace(mode)
	return mode != "" && mode != "no_device" && strings.TrimSpace(deviceType) != "redroid"
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
