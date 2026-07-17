package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type OperationKind string

const (
	OperationRuntimeStart   OperationKind = "runtime_start"
	OperationRuntimeStop    OperationKind = "runtime_stop"
	OperationRuntimeRestart OperationKind = "runtime_restart"
	OperationRuntimeDown    OperationKind = "runtime_down"
	OperationRegistration   OperationKind = "registration"
)

type OperationPhase string

const (
	PhaseQueued    OperationPhase = "queued"
	PhaseRunning   OperationPhase = "running"
	PhaseSucceeded OperationPhase = "succeeded"
	PhaseFailed    OperationPhase = "failed"
	PhaseCancelled OperationPhase = "cancelled"
)

type Progress struct {
	Phase   string
	Message string
}

type Action func(context.Context, func(Progress)) error

type Snapshot struct {
	ID         string
	Kind       OperationKind
	Phase      OperationPhase
	StartedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt time.Time
	Message    string
	Error      string
}

var ErrOperationConflict = errors.New("another lifecycle operation is already running")

type ConflictError struct {
	Active Snapshot
}

func (e *ConflictError) Error() string {
	if e == nil {
		return ErrOperationConflict.Error()
	}
	return fmt.Sprintf("%s: %s (%s)", ErrOperationConflict, e.Active.ID, e.Active.Kind)
}

func (e *ConflictError) Unwrap() error { return ErrOperationConflict }

type Coordinator struct {
	mu         sync.Mutex
	parent     context.Context
	nextID     uint64
	active     *operation
	last       Snapshot
	byID       map[string]*operation
	history    []Snapshot
	maxHistory int
}

type operation struct {
	mu     sync.Mutex
	snap   Snapshot
	cancel context.CancelFunc
	done   chan struct{}
}

func NewCoordinator(parent context.Context) *Coordinator {
	if parent == nil {
		parent = context.Background()
	}
	return &Coordinator{parent: parent, byID: make(map[string]*operation), maxHistory: 32}
}

func (c *Coordinator) Submit(kind OperationKind, action Action) (Snapshot, error) {
	if c == nil {
		return Snapshot{}, errors.New("operation coordinator is nil")
	}
	if action == nil {
		return Snapshot{}, errors.New("operation action is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		active := c.active.snapshot()
		if active.Phase == PhaseQueued || active.Phase == PhaseRunning {
			return Snapshot{}, &ConflictError{Active: active}
		}
	}
	c.nextID++
	now := time.Now().UTC()
	snapshot := Snapshot{
		ID: c.operationIDLocked(), Kind: kind, Phase: PhaseQueued,
		StartedAt: now, UpdatedAt: now, Message: "operation queued",
	}
	ctx, cancel := context.WithCancel(c.parent)
	op := &operation{snap: snapshot, cancel: cancel, done: make(chan struct{})}
	c.active = op
	c.byID[snapshot.ID] = op
	go c.run(op, ctx, action)
	return snapshot, nil
}

func (c *Coordinator) run(op *operation, ctx context.Context, action Action) {
	c.update(op, Progress{Phase: string(PhaseRunning), Message: "operation started"})
	err := action(ctx, func(progress Progress) { c.update(op, progress) })
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			c.finish(op, PhaseCancelled, "operation cancelled", err)
		} else {
			c.finish(op, PhaseFailed, "operation failed", err)
		}
		return
	}
	if ctx.Err() != nil {
		c.finish(op, PhaseCancelled, "operation cancelled", ctx.Err())
		return
	}
	c.finish(op, PhaseSucceeded, "operation completed", nil)
}

func (c *Coordinator) update(op *operation, progress Progress) {
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.snap.Phase != PhaseQueued && op.snap.Phase != PhaseRunning {
		return
	}
	op.snap.Phase = PhaseRunning
	if progress.Phase != "" {
		op.snap.Message = progress.Phase + ": " + progress.Message
	} else if progress.Message != "" {
		op.snap.Message = progress.Message
	}
	op.snap.UpdatedAt = time.Now().UTC()
}

func (c *Coordinator) finish(op *operation, phase OperationPhase, message string, err error) {
	op.mu.Lock()
	if op.snap.Phase != PhaseQueued && op.snap.Phase != PhaseRunning {
		op.mu.Unlock()
		return
	}
	op.snap.Phase = phase
	op.snap.Message = message
	op.snap.UpdatedAt = time.Now().UTC()
	op.snap.FinishedAt = op.snap.UpdatedAt
	if err != nil {
		op.snap.Error = err.Error()
	}
	snapshot := op.snap
	op.mu.Unlock()
	close(op.done)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = snapshot
	c.history = append(c.history, snapshot)
	if len(c.history) > c.maxHistory {
		c.history = append([]Snapshot(nil), c.history[len(c.history)-c.maxHistory:]...)
	}
	if c.active == op {
		c.active = nil
	}
}

func (c *Coordinator) Current() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		return c.active.snapshot()
	}
	return c.last
}

func (c *Coordinator) Get(id string) (Snapshot, bool) {
	if c == nil {
		return Snapshot{}, false
	}
	c.mu.Lock()
	op, ok := c.byID[id]
	c.mu.Unlock()
	if !ok {
		return Snapshot{}, false
	}
	return op.snapshot(), true
}

func (c *Coordinator) History() []Snapshot {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Snapshot(nil), c.history...)
}

func (c *Coordinator) Cancel(id string) error {
	if c == nil {
		return errors.New("operation coordinator is nil")
	}
	c.mu.Lock()
	op, ok := c.byID[id]
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("operation %q not found", id)
	}
	op.mu.Lock()
	active := op.snap.Phase == PhaseQueued || op.snap.Phase == PhaseRunning
	op.mu.Unlock()
	if !active {
		return nil
	}
	op.cancel()
	return nil
}

func (c *Coordinator) Wait(ctx context.Context, id string) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	op, ok := c.byID[id]
	c.mu.Unlock()
	if !ok {
		return Snapshot{}, fmt.Errorf("operation %q not found", id)
	}
	select {
	case <-op.done:
		return op.snapshot(), nil
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
}

func (c *Coordinator) operationIDLocked() string {
	return fmt.Sprintf("op-%d", c.nextID)
}

func (op *operation) snapshot() Snapshot {
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.snap
}
