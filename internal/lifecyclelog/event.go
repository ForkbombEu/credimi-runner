package lifecyclelog

import "time"

// Event is one bounded, machine-readable controller lifecycle record.
// Fields intentionally use an interface map so callers can attach typed
// diagnostic values without serialising command environments or raw output.
type Event struct {
	Schema      int            `json:"schema"`
	Timestamp   time.Time      `json:"timestamp"`
	Level       string         `json:"level"`
	Event       string         `json:"event"`
	Message     string         `json:"message,omitempty"`
	Controller  string         `json:"controller_id,omitempty"`
	OperationID string         `json:"operation_id,omitempty"`
	Trigger     string         `json:"trigger,omitempty"`
	Component   string         `json:"component,omitempty"`
	Phase       string         `json:"phase,omitempty"`
	DurationMS  int64          `json:"duration_ms,omitempty"`
	Fields      map[string]any `json:"fields,omitempty"`
	Error       string         `json:"error,omitempty"`
	Hint        string         `json:"hint,omitempty"`
}

const (
	SchemaVersion = 1
	LevelDebug    = "debug"
	LevelInfo     = "info"
	LevelWarn     = "warn"
	LevelError    = "error"
)
