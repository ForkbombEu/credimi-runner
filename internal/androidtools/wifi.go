package androidtools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

var adbConnect = func(ctx context.Context, endpoint string) (string, error) {
	command := exec.CommandContext(ctx, "adb", "connect", endpoint)
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	return string(output), err
}

// ConnectConfiguredWiFiDevices makes configured physical Wi-Fi devices visible
// to the existing ADB server before runtime readiness checks begin.
func ConnectConfiguredWiFiDevices(ctx context.Context, cfg runnerconfig.Config) error {
	for _, device := range cfg.Devices {
		if !device.Enabled || device.Type != runnerconfig.DeviceAndroidPhysical || device.AndroidPhysical == nil || device.AndroidPhysical.Transport != "wifi" {
			continue
		}
		endpoint := runnerconfig.AndroidWiFiSerial(device.AndroidPhysical.WiFiIP, device.AndroidPhysical.WiFiPort)
		output, err := adbConnect(ctx, endpoint)
		if err != nil {
			message := strings.TrimSpace(output)
			if message == "" {
				message = err.Error()
			}
			return fmt.Errorf("connect configured Wi-Fi device %q at %s: %s", device.ID, endpoint, message)
		}
	}
	return nil
}
