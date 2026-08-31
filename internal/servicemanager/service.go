package servicemanager

import (
	"context"
	"errors"
)

type LogOptions struct {
	Follow bool
	Lines  int
}

// BootstrapOptions overrides the image used while the first service
// configuration is being prepared. It is intentionally owned by the service
// manager because only that manager renders and starts the service.
type BootstrapOptions struct {
	Image      string
	PullPolicy string
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
