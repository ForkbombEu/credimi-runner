package workermanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
	Devices  []DeviceRuntimeConfig
}

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

// DeviceGate serializes target-touching work per device without preventing
// different devices from executing concurrently.
type DeviceGate struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

type DeviceDispatcher struct {
	Inventory RunnerRuntimeConfig
	Gate      *DeviceGate
}

func NewDeviceDispatcher(inventory RunnerRuntimeConfig) (*DeviceDispatcher, error) {
	if err := inventory.Validate(); err != nil {
		return nil, err
	}
	return &DeviceDispatcher{Inventory: inventory, Gate: NewDeviceGate()}, nil
}

func (d *DeviceDispatcher) Execute(ctx context.Context, deviceID string, operation func(context.Context, DeviceRuntimeConfig) error) error {
	if d == nil || operation == nil {
		return fmt.Errorf("device dispatcher is not configured")
	}
	device, err := d.Inventory.Device(deviceID)
	if err != nil {
		return err
	}
	unlock, err := d.Gate.Acquire(ctx, device.ID)
	if err != nil {
		return err
	}
	defer unlock()
	return operation(ctx, device)
}

func NewDeviceGate() *DeviceGate { return &DeviceGate{locks: make(map[string]chan struct{})} }

func (g *DeviceGate) Acquire(ctx context.Context, deviceID string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deviceID = strings.TrimPrefix(strings.TrimSpace(deviceID), "/")
	if deviceID == "" {
		return nil, fmt.Errorf("device ID is required")
	}
	g.mu.Lock()
	lock := g.locks[deviceID]
	if lock == nil {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		g.locks[deviceID] = lock
	}
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-lock:
		return func() { lock <- struct{}{} }, nil
	}
}
