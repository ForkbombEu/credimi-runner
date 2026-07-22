package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Readiness describes this particular runner process. Unlike /health, this
// endpoint is used by the local lifecycle controller to verify identity.
type Readiness struct {
	Service      string `json:"service"`
	RunnerID     string `json:"runner_id"`
	BootID       string `json:"boot_id"`
	Version      string `json:"version"`
	DeviceSerial string `json:"device_serial,omitempty"`
	DeviceState  string `json:"device_state,omitempty"`
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
	serial := strings.TrimSpace(env("ANDROID_SERIAL"))
	if serial == "" {
		serial = strings.TrimSpace(env("CREDIMI_RUNNER_SERIAL"))
	}
	state := ""
	if s.deviceReadinessRequired() && serial != "" && s.DeviceState != nil {
		state = s.DeviceState(serial)
	}
	return Readiness{Service: "credimi-runner", RunnerID: strings.TrimSpace(env("CREDIMI_RUNNER_ID")), BootID: strings.TrimSpace(env("CREDIMI_RUNNER_BOOT_ID")), Version: defaultReadinessVersion(env("CREDIMI_RUNNER_VERSION")), DeviceSerial: serial, DeviceState: state}
}

func (s *ReadinessService) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	ready := s.Check()
	w.Header().Set("Content-Type", "application/json")
	if ready.RunnerID == "" || ready.BootID == "" || (s.deviceReadinessRequired() && ready.DeviceSerial != "" && ready.DeviceState != "device") {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(ready)
}

func (s *ReadinessService) deviceReadinessRequired() bool {
	return strings.TrimSpace(s.environment("CREDIMI_CONTAINER_MODE")) != "no_device"
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
