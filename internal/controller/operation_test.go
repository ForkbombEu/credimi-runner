package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCoordinatorRunsActionAndRetainsSnapshot(t *testing.T) {
	coordinator := NewCoordinator(context.Background())
	started := make(chan struct{})
	snapshot, err := coordinator.Submit(OperationRuntimeStart, func(_ context.Context, report func(Progress)) error {
		close(started)
		report(Progress{Phase: "waiting_for_runner", Message: "waiting"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	finished, err := coordinator.Wait(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Phase != PhaseSucceeded || !strings.Contains(finished.Message, "completed") {
		t.Fatalf("snapshot = %#v", finished)
	}
	if got := coordinator.Current(); got.ID != snapshot.ID || got.Phase != PhaseSucceeded {
		t.Fatalf("current = %#v", got)
	}
}

func TestCoordinatorRejectsConcurrentMutation(t *testing.T) {
	coordinator := NewCoordinator(context.Background())
	release := make(chan struct{})
	first, err := coordinator.Submit(OperationRuntimeStart, func(ctx context.Context, _ func(Progress)) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Submit(OperationRuntimeStop, func(context.Context, func(Progress)) error { return nil }); err == nil {
		t.Fatal("expected concurrent operation conflict")
	} else {
		var conflict *ConflictError
		if !errors.As(err, &conflict) || conflict.Active.ID != first.ID {
			t.Fatalf("conflict = %v", err)
		}
	}
	close(release)
	if _, err := coordinator.Wait(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorCancelIsIndependentFromCallerContext(t *testing.T) {
	coordinator := NewCoordinator(context.Background())
	started := make(chan struct{})
	operation, err := coordinator.Submit(OperationRuntimeStart, func(ctx context.Context, _ func(Progress)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := coordinator.Cancel(operation.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := coordinator.Wait(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Phase != PhaseCancelled {
		t.Fatalf("phase = %s", finished.Phase)
	}
}

func TestCoordinatorWaitHonorsObserverDeadline(t *testing.T) {
	coordinator := NewCoordinator(context.Background())
	operation, err := coordinator.Submit(OperationRuntimeStart, func(ctx context.Context, _ func(Progress)) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := coordinator.Wait(ctx, operation.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v", err)
	}
	if err := coordinator.Cancel(operation.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = coordinator.Wait(context.Background(), operation.ID)
}
