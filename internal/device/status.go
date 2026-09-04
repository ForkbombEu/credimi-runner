package device

import "time"

// Status distinguishes host reachability from target readiness.
type Status struct {
	DeviceID   string
	Enabled    bool
	Online     bool
	Ready      bool
	Busy       bool
	Reason     string
	ObservedAt time.Time
}
