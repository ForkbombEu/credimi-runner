package device

import (
	"strings"
	"sync/atomic"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

type snapshot struct {
	devices []Device
	byID    map[string]Device
}

// Registry provides lock-free reads from an immutable device inventory.
type Registry struct{ current atomic.Pointer[snapshot] }

func NewRegistry(devices []config.DeviceConfig) (*Registry, error) {
	registry := &Registry{}
	if err := registry.Replace(devices); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *Registry) Replace(devices []config.DeviceConfig) error {
	converted, err := devicesFromConfig(devices)
	if err != nil {
		return err
	}
	byID := make(map[string]Device, len(converted))
	for _, device := range converted {
		byID[device.ID] = device
	}
	r.current.Store(&snapshot{devices: converted, byID: byID})
	return nil
}

func (r *Registry) Resolve(deviceID string) (Device, error) {
	if strings.HasPrefix(deviceID, "/") {
		return Device{}, UnknownDeviceError{DeviceID: deviceID}
	}
	current := r.current.Load()
	if current == nil {
		return Device{}, UnknownDeviceError{DeviceID: deviceID}
	}
	device, exists := current.byID[deviceID]
	if !exists {
		return Device{}, UnknownDeviceError{DeviceID: deviceID}
	}
	return device.clone(), nil
}

func (r *Registry) ResolveEnabled(deviceID string) (Device, error) {
	device, err := r.Resolve(deviceID)
	if err != nil {
		return Device{}, err
	}
	if !device.Enabled {
		return Device{}, DisabledDeviceError{DeviceID: deviceID}
	}
	return device, nil
}

func (r *Registry) List() []Device {
	current := r.current.Load()
	if current == nil {
		return nil
	}
	devices := make([]Device, len(current.devices))
	for index, device := range current.devices {
		devices[index] = device.clone()
	}
	return devices
}

// NormalizeBoundaryID accepts a leading slash only at an external boundary.
// Registry lookups intentionally remain strict so internal callers cannot hide
// malformed identifiers.
func NormalizeBoundaryID(deviceID string) string {
	return strings.TrimPrefix(deviceID, "/")
}
