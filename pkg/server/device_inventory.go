package server

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
)

var safePathComponent = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func canonicalDeviceIdentifier(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "/")
}

func (s *runnerService) configuredDevice(identifier string) (dashboardruntime.DeviceRuntimeConfig, *runner.APIError) {
	identifier = canonicalDeviceIdentifier(identifier)
	if identifier == "" {
		return dashboardruntime.DeviceRuntimeConfig{}, &runner.APIError{Code: 400, Domain: "device", Reason: "missing device_identifier", Message: "device_identifier is required"}
	}
	if s.Deps.RuntimeConfig == nil {
		return dashboardruntime.DeviceRuntimeConfig{}, nil
	}
	config := s.Deps.RuntimeConfig
	runnerID := canonicalDeviceIdentifier(config.Host["CREDIMI_RUNNER_ID"])
	for _, device := range config.Devices {
		if canonicalDeviceIdentifier(device.ID) == identifier {
			if !strings.HasPrefix(identifier, runnerID+"/") {
				break
			}
			if !device.Enabled {
				return dashboardruntime.DeviceRuntimeConfig{}, &runner.APIError{Code: 400, Domain: "device", Reason: "disabled device_identifier", Message: fmt.Sprintf("device %q is disabled", identifier)}
			}
			return device, nil
		}
	}
	return dashboardruntime.DeviceRuntimeConfig{}, &runner.APIError{Code: 400, Domain: "device", Reason: "unknown device_identifier", Message: fmt.Sprintf("device %q is not configured for this runner", identifier)}
}

func deviceArtifactRoot(root, deviceID, runID string) (string, error) {
	deviceID = canonicalDeviceIdentifier(deviceID)
	if deviceID == "" || runID == "" {
		return "", fmt.Errorf("device and run identifiers are required")
	}
	parts := strings.Split(deviceID, "/")
	for _, part := range parts {
		if part == "." || part == ".." || !safePathComponent.MatchString(part) {
			return "", fmt.Errorf("unsafe device identifier")
		}
	}
	runParts := strings.Split(strings.TrimPrefix(strings.TrimSpace(runID), "/"), "/")
	for _, part := range runParts {
		if part == "." || part == ".." || !safePathComponent.MatchString(part) {
			return "", fmt.Errorf("unsafe run identifier")
		}
	}
	return filepath.Join(append([]string{root}, append(parts, runParts...)...)...), nil
}
