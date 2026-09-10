package device

import "fmt"

var ErrInvalidDeviceID = fmt.Errorf("device ID is required")

type UnknownDeviceError struct{ DeviceID string }

func (err UnknownDeviceError) Error() string {
	return fmt.Sprintf("unknown device %q", err.DeviceID)
}

type DisabledDeviceError struct{ DeviceID string }

func (err DisabledDeviceError) Error() string {
	return fmt.Sprintf("device %q is disabled", err.DeviceID)
}

type DuplicateDeviceError struct{ DeviceID string }

func (err DuplicateDeviceError) Error() string {
	return fmt.Sprintf("duplicate device %q", err.DeviceID)
}
