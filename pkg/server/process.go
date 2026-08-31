package server

import (
	"context"
	"log"
	"sync"
)

type ProcessStore struct {
	mu        sync.Mutex
	processes map[string]*Process
}

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

type Process struct {
	mu           sync.Mutex
	Name         string
	Running      bool
	CancelFunc   context.CancelFunc
	RunFunc      func(ctx context.Context) error
	ReadyRunFunc func(ctx context.Context, ready func(error)) error
	generation   uint64
}

func NewReadyProcess(name string, runFunc func(ctx context.Context, ready func(error)) error) *Process {
	return &Process{Name: name, ReadyRunFunc: runFunc}
}

func NewProcess(name string, runFunc func(ctx context.Context) error) *Process {
	return &Process{
		Name:    name,
		RunFunc: runFunc,
	}
}

func (p *Process) Start() error {
	return p.start(context.Background(), false)
}

// StartReady waits until a readiness-aware worker has completed its real
// initialization. Legacy workers retain Start's asynchronous semantics.
func (p *Process) StartReady(ctx context.Context) error {
	return p.start(ctx, true)
}

func (p *Process) start(waitCtx context.Context, waitReady bool) error {
	p.mu.Lock()
	if p.Running {
		p.mu.Unlock()
		log.Printf("Worker for namespace %s already running", p.Name)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.CancelFunc = cancel
	p.Running = true
	p.generation++
	generation := p.generation
	run := p.RunFunc
	readyRun := p.ReadyRunFunc
	p.mu.Unlock()
	ready := make(chan error, 1)
	var readyOnce sync.Once
	signalReady := func(err error) { readyOnce.Do(func() { ready <- err }) }

	go func() {
		defer func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			// A stop/start may have already begun a new worker. The old
			// goroutine must not mark that replacement as stopped.
			if p.generation == generation {
				p.Running = false
			}
		}()

		if readyRun != nil {
			if err := readyRun(ctx, signalReady); err != nil {
				signalReady(err)
				log.Printf("Process %s stopped with error: %v", p.Name, err)
			} else {
				log.Printf("Process %s stopped", p.Name)
			}
		} else if run != nil {
			signalReady(nil)
			if err := run(ctx); err != nil {
				log.Printf("Process %s stopped with error: %v", p.Name, err)
			} else {
				log.Printf("Process %s stopped", p.Name)
			}
		}
	}()

	log.Printf("Worker started for namespace %s", p.Name)
	if waitReady && readyRun != nil {
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

func (p *Process) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Running
}
