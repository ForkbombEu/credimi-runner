package androidtools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

var adbGetState = func(ctx context.Context, serial string) (string, error) {
	command := exec.CommandContext(ctx, "adb", "-s", serial, "get-state")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	return string(output), err
}

const adbStateTimeout = 10 * time.Second

// ValidateConfiguredUSBDevices is the post-topology-expansion readiness gate
// for physical USB devices. The dashboard may save a serial before the old
// service can discover it, but the replacement service must prove that the
// configured device is usable before activation succeeds.
func ValidateConfiguredUSBDevices(ctx context.Context, cfg runnerconfig.Config) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for _, device := range cfg.Devices {
		if !device.Enabled || device.Type != runnerconfig.DeviceAndroidPhysical || device.AndroidPhysical == nil || device.AndroidPhysical.Transport != "usb" {
			continue
		}
		serial := strings.TrimSpace(device.AndroidPhysical.Serial)
		if serial == "" {
			return fmt.Errorf("USB device %q has no serial", device.ID)
		}
		stateCtx, cancel := context.WithTimeout(ctx, adbStateTimeout)
		state, err := adbGetState(stateCtx, serial)
		cancel()
		if err != nil {
			message := strings.TrimSpace(state)
			if message == "" {
				message = err.Error()
			}
			return fmt.Errorf("validate USB device %q (%s): %s", device.ID, serial, message)
		}
		if strings.TrimSpace(state) != "device" {
			return fmt.Errorf("validate USB device %q (%s): adb state is %q", device.ID, serial, strings.TrimSpace(state))
		}
	}
	return nil
}
