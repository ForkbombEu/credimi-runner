package server

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"

	genhealth "github.com/forkbombeu/credimi-runner/pkg/gen/health"
)

type HealthService struct {
	adbPath string
	runADB  func(context.Context, string, ...string) ([]byte, error)
}

func NewHealthService() *HealthService {
	return &HealthService{
		adbPath: "adb",
		runADB: func(ctx context.Context, cmd string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, cmd, args...).Output()
		},
	}
}

func (s *HealthService) Check(ctx context.Context) (*genhealth.CheckResult, error) {
	adbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	devices, err := s.getDevicesWithDetails(adbCtx)
	if err != nil {
		return nil, &genhealth.APIError{
			Name:    "service_unavailable",
			Code:    http.StatusServiceUnavailable,
			Domain:  "health",
			Reason:  "adb unavailable",
			Message: err.Error(),
		}
	}

	return &genhealth.CheckResult{
		Status:  "connected",
		Devices: devices,
	}, nil
}

func (s *HealthService) getDevicesWithDetails(ctx context.Context) ([]*genhealth.DeviceInfo, error) {
	output, err := s.runADB(ctx, s.adbPath, "devices", "-l")
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
