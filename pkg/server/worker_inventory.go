package server

import (
	"github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/pkg/workermanager"
)

func workerInventory(config runtime.RunnerRuntimeConfig) workermanager.RunnerRuntimeConfig {
	devices := make([]workermanager.DeviceRuntimeConfig, 0, len(config.Devices))
	for _, device := range config.Devices {
		values := make(map[string]string, len(device.Values))
		for key, value := range device.Values {
			values[key] = value
		}
		devices = append(devices, workermanager.DeviceRuntimeConfig{ID: device.ID, Type: device.Type, Serial: device.Serial, Enabled: device.Enabled, Values: values})
	}
	return workermanager.RunnerRuntimeConfig{RunnerID: config.Host["CREDIMI_RUNNER_ID"], Devices: devices}
}
