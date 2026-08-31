package server

import (
	"context"
	"errors"
	"log"
	"sync"
)

type ProcessStore struct {
	mu        sync.Mutex
	processes map[string]*Process
}

// ProcessRunFunc starts a worker and reports its initial readiness exactly
// once. Returning from the function means the worker has fully terminated.
type ProcessRunFunc func(ctx context.Context, started func(error)) error

func NewProcessStore() *ProcessStore {
	return &ProcessStore{
		processes: map[string]*Process{},
	}
}

func (s *ProcessStore) Add(p *Process) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processes[p.Name] = p
}

func (s *ProcessStore) Get(name string) (*Process, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.processes[name]
	return p, ok
}

func (s *ProcessStore) List() []*Process {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]*Process, 0, len(s.processes))
	for _, p := range s.processes {
		list = append(list, p)
	}
	return list
}

// StopAll stops every registered worker without changing the inventory. The
// dashboard uses this for a local runtime pause while keeping its control
// process alive.
func (s *ProcessStore) StopAll() {
	for _, process := range s.List() {
		process.Stop()
	}
}

// StopAllAndWait cancels every process before waiting for any of them. This
// lets a generation shut down all workers concurrently while still providing
// a deterministic completion boundary.
func (s *ProcessStore) StopAllAndWait(ctx context.Context) error {
	s.StopAll()
	return s.WaitAll(ctx)
}

// WaitAll waits for every registered process to finish. Callers that need to
// sequence other shutdown work between cancellation and waiting can use this
// after StopAll.
func (s *ProcessStore) WaitAll(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	processes := s.List()
	var group sync.WaitGroup
	errs := make(chan error, len(processes))
	for _, process := range processes {
		process := process
		group.Add(1)
		go func() {
			defer group.Done()
			if err := process.Wait(ctx); err != nil {
				errs <- err
			}
		}()
	}
	group.Wait()
	close(errs)
	var joined []error
	for err := range errs {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

type Process struct {
	mu         sync.Mutex
	Name       string
	Running    bool
	CancelFunc context.CancelFunc
	RunFunc    ProcessRunFunc
	generation uint64
	done       chan struct{}
}

func NewProcess(name string, runFunc ProcessRunFunc) *Process {
	return &Process{
		Name:    name,
		RunFunc: runFunc,
	}
}

// Start waits until the runner reports that its actual worker is initialized.
func (p *Process) Start(waitCtx context.Context) error {
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	p.mu.Lock()
	if p.Running {
		p.mu.Unlock()
		log.Printf("Worker for namespace %s already running", p.Name)
		return nil
	}
	if p.done != nil {
		select {
		case <-p.done:
		default:
			p.mu.Unlock()
			return errors.New("worker is still stopping")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.CancelFunc = cancel
	p.Running = true
	p.generation++
	generation := p.generation
	done := make(chan struct{})
	p.done = done
	run := p.RunFunc
	p.mu.Unlock()
	ready := make(chan error, 1)
	var readyOnce sync.Once
	signalReady := func(err error) { readyOnce.Do(func() { ready <- err }) }

	go func() {
		defer func() {
			p.mu.Lock()
			// A stop/start may have already begun a new worker. The old
			// goroutine must not mark that replacement as stopped.
			if p.generation == generation {
				p.Running = false
			}
			close(done)
			p.mu.Unlock()
		}()

		if run != nil {
			if err := run(ctx, signalReady); err != nil {
				signalReady(err)
				log.Printf("Process %s stopped with error: %v", p.Name, err)
			} else {
				signalReady(nil)
				log.Printf("Process %s stopped", p.Name)
			}
		} else {
			signalReady(nil)
		}
	}()

	log.Printf("Worker started for namespace %s", p.Name)
	select {
	case err := <-ready:
		if err != nil {
			p.Stop()
			return err
		}
	case <-waitCtx.Done():
		p.Stop()
		return waitCtx.Err()
	}
	return nil
}

func (p *Process) Stop() {
	p.mu.Lock()
	if !p.Running {
		p.mu.Unlock()
		return
	}
	cancel := p.CancelFunc
	p.Running = false
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	log.Printf("Worker stopped for namespace %s", p.Name)
}

// Wait blocks until the current process goroutine has returned. A process
// that has never started is already complete.
func (p *Process) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StopAndWait requests cancellation and waits for the worker goroutine to
// finish all owned cleanup.
func (p *Process) StopAndWait(ctx context.Context) error {
	p.Stop()
	return p.Wait(ctx)
}

func (p *Process) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Running
}
