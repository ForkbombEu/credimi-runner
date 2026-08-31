package runtime

import "time"

// RuntimeStatus is the dashboard's presentation mapping of supervisor state.
// It contains no lifecycle operations and is never inferred from a service
// process or a transient file protocol.
type RuntimeStatus struct {
	Configured            bool
	Desired               string
	Actual                string
	RunnerRunning         bool
	ObservedAt            time.Time
	Observed              bool
	DeviceReady           bool
	PublicURL             string
	LastStartedAt         time.Time
	LastError             string
	APIListening          bool
	WorkersRunning        bool
	EdgeRunning           bool
	HeartbeatRunning      bool
	PendingRestart        bool
	PendingReconcile      bool
	PendingServiceRestart bool
	PendingCredimiUpdate  bool
}
