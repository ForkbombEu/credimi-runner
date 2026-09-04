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

// Select applies the complete application placement policy. Device inventory
// is validated separately; it never changes where the runner application
// runs.
func Select(goos string) (Backend, error) {
	if goos == "" {
		goos = runtime.GOOS
	}
	switch goos {
	case "darwin":
		return Native, nil
	case "linux":
		return Container, nil
	default:
		return "", fmt.Errorf("unsupported runner host platform %q", goos)
	}
}

// ValidateDeviceTypes keeps platform capability validation separate from
// placement. Linux cannot execute CoreSimulator; macOS can execute every
// supported configured target natively.
func ValidateDeviceTypes(types []config.DeviceType, goos string) error {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" && goos != "darwin" {
		return fmt.Errorf("unsupported runner host platform %q", goos)
	}
	if goos == "linux" {
		for _, deviceType := range types {
			if deviceType == config.DeviceIOSSimulator {
				return fmt.Errorf("iOS Simulator devices require macOS")
			}
		}
	}
	return nil
}
