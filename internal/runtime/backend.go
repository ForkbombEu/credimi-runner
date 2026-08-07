// Package runtime contains the small amount of placement policy shared by
// the command, dashboard and container reconciler.
package runtime

import (
	"fmt"
	"runtime"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

type Backend string

const (
	Container Backend = "container"
	Native    Backend = "native"
)

func Select(cfg config.Config, goos string) (Backend, error) {
	if goos == "" {
		goos = runtime.GOOS
	}
	for _, device := range cfg.Devices {
		if device.Type != config.DeviceIOSSimulator {
			continue
		}
		if goos != "darwin" {
			return "", fmt.Errorf("iOS Simulator devices require macOS")
		}
		return Native, nil
	}
	return Container, nil
}
