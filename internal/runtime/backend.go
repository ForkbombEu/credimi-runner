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
	types := make([]config.DeviceType, 0, len(cfg.Devices))
	for _, device := range cfg.Devices {
		types = append(types, device.Type)
	}
	return SelectTypes(types, goos)
}

// SelectTypes is the single placement policy used by typed and legacy
// configuration adapters. Device inventory, rather than the host OS alone,
// determines placement.
func SelectTypes(types []config.DeviceType, goos string) (Backend, error) {
	if goos == "" {
		goos = runtime.GOOS
	}
	for _, deviceType := range types {
		if deviceType != config.DeviceIOSSimulator {
			continue
		}
		if goos != "darwin" {
			return "", fmt.Errorf("iOS Simulator devices require macOS")
		}
		return Native, nil
	}
	return Container, nil
}
