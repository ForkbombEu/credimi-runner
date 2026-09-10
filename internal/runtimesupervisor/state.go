package runtimesupervisor

import "time"

type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

type ActualState string

const (
	ActualStarting ActualState = "starting"
	ActualRunning  ActualState = "running"
	ActualStopping ActualState = "stopping"
	ActualStopped  ActualState = "stopped"
	ActualFailed   ActualState = "failed"
)

type PersistentState struct {
	Desired   DesiredState `json:"desired"`
	Actual    ActualState  `json:"actual"`
	LastError string       `json:"last_error,omitempty"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type Status struct {
	Desired          DesiredState `json:"desired"`
	Actual           ActualState  `json:"actual"`
	PublicURL        string       `json:"public_url,omitempty"`
	LastError        string       `json:"last_error,omitempty"`
	APIListening     bool         `json:"api_listening"`
	WorkersRunning   bool         `json:"workers_running"`
	EdgeRunning      bool         `json:"edge_running"`
	HeartbeatRunning bool         `json:"heartbeat_running"`
}
