// Package driver contains the only raw host and Docker observation commands
// used by the lifecycle controller.
package driver

import (
	"context"
	"os/exec"
)

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Driver is intentionally read-only. Lifecycle mutations belong to the
// controller transaction, not to dashboard handlers or observations.
type Driver interface {
	Observe(context.Context, Request) Result
}
