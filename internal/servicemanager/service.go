package servicemanager

import (
	"context"
	"errors"
)

type LogOptions struct {
	Follow bool
	Lines  int
}
type Status struct {
	Running                bool
	ServiceRestartRequired bool
	DashboardURL           string
	RuntimeDesired         string
	RuntimeActual          string
	RuntimeError           string
}
type Manager interface {
	Start(context.Context) error
	Stop(context.Context) error
	Restart(context.Context) error
	Status(context.Context) (Status, error)
	Logs(context.Context, LogOptions) error
}

var ErrUnsupported = errors.New("service manager is unsupported on this platform")
