// Package controller owns the read-only view of a configured runtime.
package controller

import "time"

// ObservationState is deliberately separate from Docker's raw state strings.
// It lets callers distinguish an owned runtime from a port or container that
// happens to look similar.
type ObservationState string

const (
	StateStopped  ObservationState = "stopped"
	StateRunning  ObservationState = "running"
	StateDegraded ObservationState = "degraded"
	StateForeign  ObservationState = "foreign"
	StateUnknown  ObservationState = "unknown"
)

type ObservedService struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Role     string           `json:"role"`
	Image    string           `json:"image,omitempty"`
	State    ObservationState `json:"state"`
	Detail   string           `json:"detail,omitempty"`
	Owned    bool             `json:"owned"`
	Critical bool             `json:"critical"`
}

type ObservedDevice struct {
	Serial string           `json:"serial,omitempty"`
	State  ObservationState `json:"state"`
	Detail string           `json:"detail,omitempty"`
}

type PublicEndpoint struct {
	URL        string    `json:"url,omitempty"`
	Source     string    `json:"source,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

// RegistrationEvidence records only facts observed locally. It is not proof
// of a registration mutation and is safe to return from status APIs.
type RegistrationEvidence struct {
	RunnerID string `json:"runner_id,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Current  bool   `json:"current"`
}

// ObservedRuntime is the controller's source of truth. Observation never
// starts, stops, recreates, or registers anything.
type ObservedRuntime struct {
	State        ObservationState     `json:"state"`
	Services     []ObservedService    `json:"services"`
	Device       ObservedDevice       `json:"device"`
	Endpoint     PublicEndpoint       `json:"endpoint"`
	Registration RegistrationEvidence `json:"registration"`
	ObservedAt   time.Time            `json:"observed_at"`
	Error        string               `json:"error,omitempty"`
}

func (o ObservedRuntime) Stale(now time.Time, maxAge time.Duration) bool {
	return o.ObservedAt.IsZero() || maxAge <= 0 || now.Sub(o.ObservedAt) > maxAge
}
