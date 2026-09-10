// Package device owns the canonical runtime inventory and routing primitives.
package device

import (
	"sort"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

// Device is an immutable runtime snapshot of one configured execution target.
// Its configuration is copied when the registry is replaced, so callers cannot
// mutate the registry through a returned device.
type Device struct {
	ID          string
	Name        string
	Description string
	Type        config.DeviceType
	Enabled     bool

	AndroidPhysical *config.AndroidPhysicalConfig
	AndroidEmulator *config.AndroidEmulatorConfig
	Redroid         *config.RedroidConfig
	IOSSimulator    *config.IOSSimulatorConfig
}

func fromConfig(cfg config.DeviceConfig) Device {
	return Device{
		ID:              cfg.ID,
		Name:            cfg.Name,
		Description:     cfg.Description,
		Type:            cfg.Type,
		Enabled:         cfg.Enabled,
		AndroidPhysical: cloneAndroidPhysical(cfg.AndroidPhysical),
		AndroidEmulator: cloneAndroidEmulator(cfg.AndroidEmulator),
		Redroid:         cloneRedroid(cfg.Redroid),
		IOSSimulator:    cloneIOSSimulator(cfg.IOSSimulator),
	}
}

func (d Device) clone() Device {
	d.AndroidPhysical = cloneAndroidPhysical(d.AndroidPhysical)
	d.AndroidEmulator = cloneAndroidEmulator(d.AndroidEmulator)
	d.Redroid = cloneRedroid(d.Redroid)
	d.IOSSimulator = cloneIOSSimulator(d.IOSSimulator)
	return d
}

func cloneAndroidPhysical(value *config.AndroidPhysicalConfig) *config.AndroidPhysicalConfig {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneAndroidEmulator(value *config.AndroidEmulatorConfig) *config.AndroidEmulatorConfig {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneRedroid(value *config.RedroidConfig) *config.RedroidConfig {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneIOSSimulator(value *config.IOSSimulatorConfig) *config.IOSSimulatorConfig {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func devicesFromConfig(devices []config.DeviceConfig) ([]Device, error) {
	result := make([]Device, 0, len(devices))
	seen := make(map[string]struct{}, len(devices))
	for _, cfg := range devices {
		if cfg.ID == "" {
			return nil, ErrInvalidDeviceID
		}
		if _, exists := seen[cfg.ID]; exists {
			return nil, DuplicateDeviceError{DeviceID: cfg.ID}
		}
		seen[cfg.ID] = struct{}{}
		result = append(result, fromConfig(cfg))
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}
