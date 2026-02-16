package server

import (
	"context"
	"os/exec"
	"strings"

	genhealth "github.com/forkbombeu/credimi-runner/pkg/gen/health"
)

type HealthService struct {
	adbPath string
	runADB  func(cmd string, args ...string) ([]byte, error)
}

func NewHealthService() *HealthService {
	return &HealthService{
		adbPath: "adb",
		runADB: func(cmd string, args ...string) ([]byte, error) {
			return exec.Command(cmd, args...).Output()
		},
	}
}

func (s *HealthService) Check(ctx context.Context) (*genhealth.CheckResult, error) {
	emulators, err := s.getDevicesWithDetails()
	if err != nil {
		return nil, err
	}

	return &genhealth.CheckResult{
		Status:    "connected",
		Emulators: emulators,
	}, nil
}

func (s *HealthService) getDevicesWithDetails() ([]*genhealth.DeviceInfo, error) {
	output, err := s.runADB(s.adbPath, "devices", "-l")
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
