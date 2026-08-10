package workermanager

import (
	"context"
	"fmt"
	"os"
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
	Host     map[string]string
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
	Inventory *InventoryStore
	Gate      *DeviceGate
}

// InventoryStore is the concurrency-safe local source of truth for device
// execution settings. Replacing it updates future activity lookups without
// stopping the Temporal worker or mutating process-wide environment state.
type InventoryStore struct {
	mu     sync.RWMutex
	config RunnerRuntimeConfig
}

func NewInventoryStore(config RunnerRuntimeConfig) (*InventoryStore, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &InventoryStore{config: cloneRuntimeConfig(config)}, nil
}

func (s *InventoryStore) Update(config RunnerRuntimeConfig) error {
	if s == nil {
		return fmt.Errorf("inventory store is not configured")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.config = cloneRuntimeConfig(config)
	s.mu.Unlock()
	return nil
}

func (s *InventoryStore) Snapshot() RunnerRuntimeConfig {
	if s == nil {
		return RunnerRuntimeConfig{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneRuntimeConfig(s.config)
}

// Environment returns the getter shape expected by credimi-extra mobile
// activities. It resolves selected-device values first, then stable host
// values, then the caller's fallback. No global environment is modified.
func (s *InventoryStore) Environment(deviceID string) (func(string, ...any) string, error) {
	config := s.Snapshot()
	device, err := config.Device(deviceID)
	if err != nil {
		return nil, err
	}
	return func(name string, fallback ...any) string {
		if value := strings.TrimSpace(device.Values[name]); value != "" {
			return value
		}
		if value := strings.TrimSpace(config.Host[name]); value != "" {
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

func NewDeviceDispatcher(inventory RunnerRuntimeConfig) (*DeviceDispatcher, error) {
	if err := inventory.Validate(); err != nil {
		return nil, err
	}
	store, err := NewInventoryStore(inventory)
	if err != nil {
		return nil, err
	}
	return &DeviceDispatcher{Inventory: store, Gate: NewDeviceGate()}, nil
}

func (d *DeviceDispatcher) Execute(ctx context.Context, deviceID string, operation func(context.Context, DeviceRuntimeConfig) error) error {
	if d == nil || operation == nil {
		return fmt.Errorf("device dispatcher is not configured")
	}
	device, err := d.Inventory.Snapshot().Device(deviceID)
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

func cloneRuntimeConfig(config RunnerRuntimeConfig) RunnerRuntimeConfig {
	cloned := RunnerRuntimeConfig{RunnerID: config.RunnerID, Host: make(map[string]string, len(config.Host)), Devices: make([]DeviceRuntimeConfig, len(config.Devices))}
	for key, value := range config.Host {
		cloned.Host[key] = value
	}
	for index, device := range config.Devices {
		cloned.Devices[index] = device
		cloned.Devices[index].Values = make(map[string]string, len(device.Values))
		for key, value := range device.Values {
			cloned.Devices[index].Values[key] = value
		}
	}
	return cloned
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
