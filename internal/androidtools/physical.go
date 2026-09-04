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

// ValidateConfiguredPhysicalDevices prepares configured Wi-Fi phones and
// proves every enabled physical Android target is usable before activation.
func ValidateConfiguredPhysicalDevices(ctx context.Context, cfg runnerconfig.Config) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ConnectConfiguredWiFiDevices(ctx, cfg); err != nil {
		return err
	}
	for _, device := range cfg.Devices {
		if !device.Enabled || device.Type != runnerconfig.DeviceAndroidPhysical || device.AndroidPhysical == nil {
			continue
		}
		transport := strings.TrimSpace(device.AndroidPhysical.Transport)
		serial := strings.TrimSpace(device.AndroidPhysical.Serial)
		switch transport {
		case "usb":
			if serial == "" {
				return fmt.Errorf("USB device %q has no serial", device.ID)
			}
		case "wifi":
			serial = runnerconfig.AndroidWiFiSerial(device.AndroidPhysical.WiFiIP, device.AndroidPhysical.WiFiPort)
			if serial == "" {
				return fmt.Errorf("Wi-Fi device %q has no endpoint", device.ID)
			}
		default:
			continue
		}
		stateCtx, cancel := context.WithTimeout(ctx, adbStateTimeout)
		state, err := adbGetState(stateCtx, serial)
		cancel()
		if err != nil {
			message := strings.TrimSpace(state)
			if message == "" {
				message = err.Error()
			}
			return fmt.Errorf("validate %s device %q (%s): %s", transport, device.ID, serial, message)
		}
		if strings.TrimSpace(state) != "device" {
			return fmt.Errorf("validate %s device %q (%s): adb state is %q", transport, device.ID, serial, strings.TrimSpace(state))
		}
	}
	return nil
}

// ValidateConfiguredUSBDevices preserves the focused USB readiness API.
func ValidateConfiguredUSBDevices(ctx context.Context, cfg runnerconfig.Config) error {
	usbOnly := cfg
	usbOnly.Devices = nil
	for _, device := range cfg.Devices {
		if device.Enabled && device.Type == runnerconfig.DeviceAndroidPhysical && device.AndroidPhysical != nil && strings.TrimSpace(device.AndroidPhysical.Transport) == "usb" {
			usbOnly.Devices = append(usbOnly.Devices, device)
		}
	}
	return ValidateConfiguredPhysicalDevices(ctx, usbOnly)
}
