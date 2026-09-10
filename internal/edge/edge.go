package edge

import "context"

type Edge interface {
	Start(context.Context, string) (string, error)
	Stop(context.Context) error
	Close() error
	Running() bool
}
