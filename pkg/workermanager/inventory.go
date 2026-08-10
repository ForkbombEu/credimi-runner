package workermanager

import (
	"fmt"
	"os"
	"strings"
)

// DeviceRuntimeConfig is the immutable execution-target data handed to a
// namespace worker. It intentionally contains no mutable process environment.
type DeviceRuntimeConfig struct {
	ID      string
	Type    string
	Serial  string
	Enabled bool
	Values  map[string]string
}

// RunnerRuntimeConfig is shared by every namespace worker for one local host.
type RunnerRuntimeConfig struct {
	RunnerID string
	Host     map[string]string
	Devices  []DeviceRuntimeConfig
}

// RuntimeConfigProvider resolves the current typed runner configuration.
// Workers call it when a target activity starts, so dashboard inventory edits
// apply without restarting Temporal workers.
type RuntimeConfigProvider func() (RunnerRuntimeConfig, error)

func (c RunnerRuntimeConfig) Validate() error {
	c.RunnerID = strings.TrimPrefix(strings.TrimSpace(c.RunnerID), "/")
	if c.RunnerID == "" {
		return fmt.Errorf("runner ID is required")
	}
	if len(c.Devices) == 0 {
		return fmt.Errorf("at least one device is required")
	}
	seen := make(map[string]struct{}, len(c.Devices))
	for _, device := range c.Devices {
		id := strings.TrimPrefix(strings.TrimSpace(device.ID), "/")
		if !strings.HasPrefix(id, c.RunnerID+"/") {
			return fmt.Errorf("device %q does not belong to runner %q", id, c.RunnerID)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate device %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (c RunnerRuntimeConfig) Device(deviceID string) (DeviceRuntimeConfig, error) {
	deviceID = strings.TrimPrefix(strings.TrimSpace(deviceID), "/")
	for _, device := range c.Devices {
		if strings.TrimPrefix(strings.TrimSpace(device.ID), "/") != deviceID {
			continue
		}
		if !device.Enabled {
			return DeviceRuntimeConfig{}, fmt.Errorf("device %q is disabled", deviceID)
		}
		return device, nil
	}
	return DeviceRuntimeConfig{}, fmt.Errorf("unknown device %q", deviceID)
}

// Environment returns the getter shape expected by Credimi mobile activities.
// It resolves selected-device values first, then stable host values, then the
// process environment and the caller's fallback. No global environment is
// modified. The receiver is a value loaded from the current TOML snapshot.
func (c RunnerRuntimeConfig) Environment(deviceID string) (func(string, ...any) string, error) {
	device, err := c.Device(deviceID)
	if err != nil {
		return nil, err
	}
	host := cloneStringMap(c.Host)
	values := cloneStringMap(device.Values)
	return func(name string, fallback ...any) string {
		if value := strings.TrimSpace(values[name]); value != "" {
			return value
		}
		if value := strings.TrimSpace(host[name]); value != "" {
			return value
		}
		if value, ok := os.LookupEnv(name); ok && value != "" {
			return value
		}
		if len(fallback) > 0 {
			if value, ok := fallback[0].(string); ok {
				return value
			}
		}
		return ""
	}, nil
}
func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
