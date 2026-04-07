package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	genhealth "github.com/forkbombeu/credimi-runner/pkg/gen/health"
)

const defaultAppleDeviceCacheTTL = 5 * time.Second

type HealthService struct {
	adbPath           string
	xcrunPath         string
	goos              string
	appleCacheTTL     time.Duration
	appleCacheAt      time.Time
	appleRefreshInFly bool
	now               func() time.Time
	runADB            func(cmd string, args ...string) ([]byte, error)
	runXCRun          func(cmd string, args ...string) ([]byte, error)

	appleCacheMu      sync.RWMutex
	cachedAppleDevice []*genhealth.DeviceInfo
}

func NewHealthService() *HealthService {
	svc := &HealthService{
		adbPath:       "adb",
		xcrunPath:     "xcrun",
		goos:          runtime.GOOS,
		appleCacheTTL: defaultAppleDeviceCacheTTL,
		now:           time.Now,
		runADB: func(cmd string, args ...string) ([]byte, error) {
			return exec.Command(cmd, args...).Output()
		},
		runXCRun: func(cmd string, args ...string) ([]byte, error) {
			return exec.Command(cmd, args...).Output()
		},
	}

	if svc.goos == "darwin" {
		svc.scheduleAppleDeviceRefresh()
	}

	return svc
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

	devices = append(devices, s.getCachedAppleDevices()...)
	s.scheduleAppleDeviceRefresh()

	return devices, nil
}

func (s *HealthService) getCachedAppleDevices() []*genhealth.DeviceInfo {
	s.appleCacheMu.RLock()
	defer s.appleCacheMu.RUnlock()

	if len(s.cachedAppleDevice) == 0 {
		return nil
	}

	cached := make([]*genhealth.DeviceInfo, len(s.cachedAppleDevice))
	copy(cached, s.cachedAppleDevice)

	return cached
}

func (s *HealthService) scheduleAppleDeviceRefresh() {
	s.appleCacheMu.Lock()
	if s.appleRefreshInFly || !s.appleCacheExpiredLocked() {
		s.appleCacheMu.Unlock()
		return
	}
	s.appleRefreshInFly = true
	s.appleCacheMu.Unlock()

	go s.refreshAppleDeviceCache()
}

func (s *HealthService) appleCacheExpiredLocked() bool {
	if s.appleCacheAt.IsZero() {
		return true
	}

	return s.nowTime().Sub(s.appleCacheAt) >= s.appleCacheWindow()
}

func (s *HealthService) refreshAppleDeviceCache() {
	devices := s.getAppleDevicesWithDetails()

	s.appleCacheMu.Lock()
	s.appleRefreshInFly = false
	s.appleCacheAt = s.nowTime()
	if devices != nil {
		s.cachedAppleDevice = devices
	}
	s.appleCacheMu.Unlock()
}

func (s *HealthService) getAppleDevicesWithDetails() []*genhealth.DeviceInfo {
	type probeResult struct {
		devices []*genhealth.DeviceInfo
	}

	results := make(chan probeResult, 2)

	go func() {
		devices, err := s.getConnectedIOSDevices()
		if err != nil {
			results <- probeResult{}
			return
		}
		results <- probeResult{devices: devices}
	}()

	go func() {
		devices, err := s.getBootedIOSSimulators()
		if err != nil {
			results <- probeResult{}
			return
		}
		results <- probeResult{devices: devices}
	}()

	var devices []*genhealth.DeviceInfo
	for i := 0; i < 2; i++ {
		result := <-results
		devices = append(devices, result.devices...)
	}

	return devices
}

func (s *HealthService) appleCacheWindow() time.Duration {
	if s.appleCacheTTL > 0 {
		return s.appleCacheTTL
	}

	return defaultAppleDeviceCacheTTL
}

func (s *HealthService) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}

	return time.Now()
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
