package device

import (
	"context"
	"sync"
)

type gate struct{ token chan struct{} }

// Gates serializes work per canonical device ID without imposing a global lock.
type Gates struct {
	mu    sync.Mutex
	gates map[string]*gate
}

func NewGates() *Gates { return &Gates{gates: make(map[string]*gate)} }

func (g *Gates) Acquire(ctx context.Context, deviceID string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	entry := g.gates[deviceID]
	if entry == nil {
		entry = &gate{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		g.gates[deviceID] = entry
	}
	g.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-entry.token:
	}
	var once sync.Once
	return func() { once.Do(func() { entry.token <- struct{}{} }) }, nil
}
