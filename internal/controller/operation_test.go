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

func TestCoordinatorExposesHistoryAndOperationErrors(t *testing.T) {
	var nilCoordinator *Coordinator
	if nilCoordinator.Current().ID != "" || nilCoordinator.History() != nil {
		t.Fatal("nil coordinator should expose empty state")
	}
	if _, ok := nilCoordinator.Get("op-1"); ok {
		t.Fatal("nil coordinator should not find operations")
	}
	if err := nilCoordinator.Cancel("op-1"); err == nil {
		t.Fatal("nil coordinator cancellation should fail")
	}

	coordinator := NewCoordinator(context.Background())
	operation, err := coordinator.Submit(OperationRuntimeStop, func(context.Context, func(Progress)) error { return errors.New("stop failed") })
	if err != nil {
		t.Fatal(err)
	}
	finished, err := coordinator.Wait(context.Background(), operation.ID)
	if err != nil || finished.Phase != PhaseFailed || finished.Error != "stop failed" {
		t.Fatalf("finished=%#v err=%v", finished, err)
	}
	if got, ok := coordinator.Get(operation.ID); !ok || got.ID != operation.ID {
		t.Fatalf("Get = %#v ok=%v", got, ok)
	}
	if got := coordinator.History(); len(got) != 1 || got[0].ID != operation.ID {
		t.Fatalf("History = %#v", got)
	}
	if err := coordinator.Cancel(operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Cancel("missing"); err == nil {
		t.Fatal("expected missing operation cancellation error")
	}
	if _, err := coordinator.Wait(context.Background(), "missing"); err == nil {
		t.Fatal("expected missing operation wait error")
	}
	conflict := &ConflictError{Active: operation}
	if !strings.Contains(conflict.Error(), operation.ID) || !errors.Is(conflict, ErrOperationConflict) {
		t.Fatalf("conflict = %v", conflict)
	}
}
