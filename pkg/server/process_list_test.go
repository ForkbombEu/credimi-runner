package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunningProcessNames(t *testing.T) {
	procs := []*Process{
		{Name: "zeta", Running: true},
		{Name: "alpha", Running: false},
		{Name: "beta", Running: true},
	}

	names := runningProcessNames(procs)

	require.Equal(t, []string{"zeta", "beta"}, names)
}

func TestProcessStoreStopAllStopsRegisteredWorkers(t *testing.T) {
	store := NewProcessStore()
	stopped := 0
	for _, name := range []string{"alpha", "beta"} {
		process := NewProcess(name, nil)
		process.Running = true
		process.CancelFunc = func() { stopped++ }
		store.Add(process)
	}
	store.Add(NewProcess("idle", testProcessRunner(func(context.Context) error { return nil })))
	store.StopAll()
	if stopped != 2 {
		t.Fatalf("stop calls = %d, want 2", stopped)
	}
	for _, process := range store.List() {
		if process.Running {
			t.Fatalf("process %q remains running", process.Name)
		}
	}
}
