//go:build !darwin

package servicemanager

import "context"

type LaunchAgentManager struct{ ConfigDir, BinaryPath string }

func (LaunchAgentManager) Start(context.Context) error            { return ErrUnsupported }
func (LaunchAgentManager) Stop(context.Context) error             { return ErrUnsupported }
func (LaunchAgentManager) Restart(context.Context) error          { return ErrUnsupported }
func (LaunchAgentManager) Enable(context.Context) error           { return ErrUnsupported }
func (LaunchAgentManager) Disable(context.Context) error          { return ErrUnsupported }
func (LaunchAgentManager) Status(context.Context) (Status, error) { return Status{}, ErrUnsupported }
func (LaunchAgentManager) Logs(context.Context, LogOptions) error { return ErrUnsupported }
