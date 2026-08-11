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
	Name       string
	Running    bool
	CancelFunc context.CancelFunc
	RunFunc    func(ctx context.Context) error
}

func NewProcess(name string, runFunc func(ctx context.Context) error) *Process {
	return &Process{
		Name:    name,
		RunFunc: runFunc,
	}
}

func (p *Process) Start() error {
	if p.Running {
		log.Printf("Worker for namespace %s already running", p.Name)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.CancelFunc = cancel
	p.Running = true

	go func() {
		defer func() { p.Running = false }()

		if p.RunFunc != nil {
			if err := p.RunFunc(ctx); err != nil {
				log.Printf("Process %s stopped with error: %v", p.Name, err)
			} else {
				log.Printf("Process %s stopped", p.Name)
			}
		}
	}()

	log.Printf("Worker started for namespace %s", p.Name)
	return nil
}

func (p *Process) Stop() {
	if !p.Running {
		return
	}
	p.CancelFunc()
	p.Running = false
	log.Printf("Worker stopped for namespace %s", p.Name)
}
