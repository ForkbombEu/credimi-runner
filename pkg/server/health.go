package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	genhealth "github.com/forkbombeu/credimi-runner/pkg/gen/health"
)

type HealthService struct {
	adbPath   string
	xcrunPath string
	goos      string
	runADB    func(cmd string, args ...string) ([]byte, error)
	runXCRun  func(cmd string, args ...string) ([]byte, error)
}

func NewHealthService() *HealthService {
	return &HealthService{
		adbPath:   "adb",
		xcrunPath: "xcrun",
		goos:      runtime.GOOS,
		runADB: func(cmd string, args ...string) ([]byte, error) {
			return exec.Command(cmd, args...).Output()
		},
		runXCRun: func(cmd string, args ...string) ([]byte, error) {
			return exec.Command(cmd, args...).Output()
		},
	}
}

func (s *HealthService) Check(ctx context.Context) (*genhealth.CheckResult, error) {
	emulators, err := s.getDevicesWithDetails()
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
		Status:    "connected",
		Emulators: emulators,
	}, nil
}

func (s *HealthService) getDevicesWithDetails() ([]*genhealth.DeviceInfo, error) {
	devices, err := s.getADBDevicesWithDetails()
	if err != nil {
		return nil, err
	}

	if s.goos != "darwin" {
		return devices, nil
	}

	iosDevices, err := s.getConnectedIOSDevices()
	if err == nil {
		devices = append(devices, iosDevices...)
	}

	simulators, err := s.getBootedIOSSimulators()
	if err == nil {
		devices = append(devices, simulators...)
	}

	return devices, nil
}

func (s *HealthService) getADBDevicesWithDetails() ([]*genhealth.DeviceInfo, error) {
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

type appleDevice struct {
	Available  bool   `json:"available"`
	Identifier string `json:"identifier"`
	Interface  string `json:"interface"`
	ModelName  string `json:"modelName"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	Simulator  bool   `json:"simulator"`
}

func (s *HealthService) getConnectedIOSDevices() ([]*genhealth.DeviceInfo, error) {
	output, err := s.runXCRun(s.xcrunPath, "xcdevice", "list")
	if err != nil {
		return nil, err
	}

	var appleDevices []appleDevice
	if err := json.Unmarshal(output, &appleDevices); err != nil {
		return nil, err
	}

	var devices []*genhealth.DeviceInfo
	for _, appleDevice := range appleDevices {
		if appleDevice.Simulator || !appleDevice.Available {
			continue
		}
		if appleDevice.Platform != "com.apple.platform.iphoneos" {
			continue
		}

		serial := appleDevice.Identifier
		state := "device"
		product := "ios-device"

		device := &genhealth.DeviceInfo{
			Serial:  &serial,
			State:   &state,
			Product: &product,
		}

		if appleDevice.ModelName != "" {
			model := appleDevice.ModelName
			device.Model = &model
		}
		if appleDevice.Name != "" {
			name := appleDevice.Name
			device.Device = &name
		}
		if appleDevice.Interface != "" {
			transport := appleDevice.Interface
			device.TransportID = &transport
		}

		devices = append(devices, device)
	}

	return devices, nil
}

type appleSimulatorList struct {
	Devices map[string][]appleSimulator `json:"devices"`
}

type appleSimulator struct {
	IsAvailable bool   `json:"isAvailable"`
	Name        string `json:"name"`
	State       string `json:"state"`
	UDID        string `json:"udid"`
}

func (s *HealthService) getBootedIOSSimulators() ([]*genhealth.DeviceInfo, error) {
	output, err := s.runXCRun(s.xcrunPath, "simctl", "list", "devices", "booted", "--json")
	if err != nil {
		return nil, err
	}

	var simulatorList appleSimulatorList
	if err := json.Unmarshal(output, &simulatorList); err != nil {
		return nil, err
	}

	var devices []*genhealth.DeviceInfo
	for runtime, runtimeDevices := range simulatorList.Devices {
		if !isIOSSimulatorRuntime(runtime) {
			continue
		}

		for _, simulator := range runtimeDevices {
			if !simulator.IsAvailable || simulator.State != "Booted" {
				continue
			}

			serial := simulator.UDID
			state := "booted"
			product := "ios-simulator"
			model := simulator.Name
			deviceType := "simulator"
			transport := "simulator"

			devices = append(devices, &genhealth.DeviceInfo{
				Serial:      &serial,
				State:       &state,
				Product:     &product,
				Model:       &model,
				Device:      &deviceType,
				TransportID: &transport,
			})
		}
	}

	return devices, nil
}

func isIOSSimulatorRuntime(runtime string) bool {
	return strings.Contains(strings.ToLower(runtime), ".ios-")
}
