package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

var (
	ErrListenerConflict       = errors.New("runner listener conflict")
	ErrRunnerIdentityMismatch = errors.New("runner identity mismatch")
	ErrRunnerNotReady         = errors.New("runner not ready")
	ErrDeviceMissing          = errors.New("configured device missing")
	ErrDeviceOffline          = errors.New("configured device offline")
	ErrDeviceUnauthorized     = errors.New("configured device unauthorized")
	ErrDeviceMismatch         = errors.New("configured device mismatch")
)

type RunnerReadiness struct {
	Service  string                     `json:"service"`
	RunnerID string                     `json:"runner_id"`
	BootID   string                     `json:"boot_id"`
	Version  string                     `json:"version"`
	Devices  map[string]DeviceReadiness `json:"devices"`
}

type DeviceReadiness struct {
	Serial string `json:"serial"`
	State  string `json:"state"`
	Ready  bool   `json:"ready"`
}

func ValidateReadiness(ctx context.Context, client *http.Client, endpoint string, values dashboardruntime.Values) (RunnerReadiness, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/readyz", nil)
	if err != nil {
		return RunnerReadiness{}, fmt.Errorf("create runner readiness request: %w", err)
	}
	response, err := client.Do(req)
	if err != nil {
		return RunnerReadiness{}, fmt.Errorf("%w: %v", ErrListenerConflict, err)
	}
	defer response.Body.Close()
	deviceRequired := dashboardruntime.DeviceReadinessRequired(values, "")
	var ready RunnerReadiness
	if err := json.NewDecoder(response.Body).Decode(&ready); err != nil {
		return RunnerReadiness{}, fmt.Errorf("%w: response is not runner readiness JSON", ErrListenerConflict)
	}
	if ready.Service != "credimi-runner" || strings.TrimSpace(ready.BootID) == "" {
		return ready, ErrRunnerIdentityMismatch
	}
	if want := strings.TrimSpace(values["CREDIMI_RUNNER_ID"]); want != "" && ready.RunnerID != want {
		return ready, ErrRunnerIdentityMismatch
	}
	if deviceRequired {
		if inventory, inventoryErr := dashboardruntime.ParseRuntimeConfig(values); inventoryErr == nil {
			for _, device := range inventory.Devices {
				if !device.Enabled {
					continue
				}
				state, ok := ready.Devices[device.ID]
				if !ok {
					return ready, ErrDeviceMissing
				}
				if device.Serial != "" && state.Serial != "" && device.Serial != state.Serial {
					return ready, ErrDeviceMismatch
				}
				if err := readinessStateError(state.State); err != nil {
					return ready, err
				}
			}
			if response.StatusCode != http.StatusOK {
				return ready, ErrRunnerNotReady
			}
			return ready, nil
		}
		return ready, ErrDeviceMissing
	}
	return ready, nil
}

func readinessStateError(state string) error {
	switch strings.TrimSpace(state) {
	case "", "device":
		return nil
	case "offline":
		return ErrDeviceOffline
	case "unauthorized":
		return ErrDeviceUnauthorized
	case "missing":
		return ErrDeviceMissing
	default:
		return fmt.Errorf("%w: %s", ErrRunnerNotReady, state)
	}
}
