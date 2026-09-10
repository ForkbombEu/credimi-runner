package server

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	genhealth "github.com/forkbombeu/credimi-runner/pkg/gen/health"
)

type HealthService struct {
	adbPath       string
	runADB        func(context.Context, string, ...string) ([]byte, error)
	RuntimeConfig func() (dashboardruntime.RunnerRuntimeConfig, error)
	resolveADB    func() (string, error)
}

func NewHealthService() *HealthService {
	return &HealthService{
		adbPath:    "adb", // retained for direct standalone health instances.
		resolveADB: androidtools.ResolveADB,
		runADB: func(ctx context.Context, cmd string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, cmd, args...).Output()
		},
	}
}

func (s *HealthService) Check(ctx context.Context) (*genhealth.CheckResult, error) {
	if !s.requiresADB() {
		return &genhealth.CheckResult{Status: "connected", Devices: []*genhealth.DeviceInfo{}}, nil
	}
	adbPath := s.adbPath
	if s.resolveADB != nil {
		path, err := s.resolveADB()
		if err != nil {
			return nil, adbHealthError(err)
		}
		adbPath = path
	}
	adbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	devices, err := s.getDevicesWithDetailsAt(adbCtx, adbPath)
	if err != nil {
		return nil, adbHealthError(err)
	}

	return &genhealth.CheckResult{
		Status:  "connected",
		Devices: devices,
	}, nil
}

func adbHealthError(err error) *genhealth.APIError {
	return &genhealth.APIError{Name: "service_unavailable", Code: http.StatusServiceUnavailable, Domain: "health", Reason: "adb unavailable", Message: err.Error()}
}

func (s *HealthService) requiresADB() bool {
	if s.RuntimeConfig == nil {
		return true
	}
	inventory, err := s.RuntimeConfig()
	if err != nil {
		return true
	}
	for _, device := range inventory.Devices {
		if !device.Enabled {
			continue
		}
		switch device.Type {
		case "android_phone", "android_emulator", "redroid":
			return true
		}
	}
	return false
}

func (s *HealthService) getDevicesWithDetailsAt(ctx context.Context, adbPath string) ([]*genhealth.DeviceInfo, error) {
	output, err := s.runADB(ctx, adbPath, "devices", "-l")
	if err != nil {
		return nil, err
	}

	var devices []*genhealth.DeviceInfo
	lines := strings.Split(string(output), "\n")

	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		serial := parts[0]
		state := parts[1]

		device := &genhealth.DeviceInfo{
			Serial: &serial,
			State:  &state,
		}

		for _, detail := range parts[2:] {
			switch {
			case strings.HasPrefix(detail, "product:"):
				value := strings.TrimPrefix(detail, "product:")
				device.Product = &value
			case strings.HasPrefix(detail, "model:"):
				value := strings.TrimPrefix(detail, "model:")
				device.Model = &value
			case strings.HasPrefix(detail, "device:"):
				value := strings.TrimPrefix(detail, "device:")
				device.Device = &value
			case strings.HasPrefix(detail, "transport_id:"):
				value := strings.TrimPrefix(detail, "transport_id:")
				device.TransportID = &value
			}
		}

		devices = append(devices, device)
	}

	return devices, nil
}
