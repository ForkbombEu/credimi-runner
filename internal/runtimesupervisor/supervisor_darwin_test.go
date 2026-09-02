//go:build darwin

package runtimesupervisor

import (
	"context"
	"testing"
)

// TestDarwinSupervisorGenerationReplacement exercises the real supervisor
// transition boundary used by the macOS application. The dependencies are
// deterministic fakes, while generation ownership, teardown and replacement
// are all production code.
func TestDarwinSupervisorGenerationReplacement(t *testing.T) {
	life := &testLife{}
	edgeImpl := &testEdge{}
	workers := &testWorkers{}
	s, _ := newTestSupervisor(t, life, edgeImpl, workers)
	ctx := context.Background()
	if err := s.Reconcile(ctx, validConfig()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	status := s.Status()
	if status.Desired != DesiredRunning || status.Actual != ActualRunning {
		t.Fatalf("start status=%+v", status)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Status().Desired == DesiredRunning || s.Status().Actual != ActualStopped {
		t.Fatalf("stop status=%+v", s.Status())
	}
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	old := s.currentGeneration()
	if err := s.Reconcile(ctx, validConfig()); err != nil {
		t.Fatal(err)
	}
	if s.currentGeneration() == nil || s.currentGeneration() == old {
		t.Fatal("reconcile did not replace generation")
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if s.currentGeneration() != nil {
		t.Fatal("generation retained after close")
	}
}
