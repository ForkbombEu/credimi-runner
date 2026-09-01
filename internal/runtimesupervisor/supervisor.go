package runtimesupervisor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/edge"
	"github.com/forkbombeu/credimi-runner/pkg/server"
)

const (
	startTimeout     = 2 * time.Minute
	stopTimeout      = 30 * time.Second
	cleanupTimeout   = 30 * time.Second
	edgeStartTimeout = 2 * time.Minute
	pauseTimeout     = 5 * time.Second
)

type LifecycleClient interface {
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	Heartbeat(context.Context) error
	StartHeartbeatLoop(context.Context) func()
}

type WorkerSet interface {
	Start(context.Context) error
	StopAll()
	WaitAll(context.Context) error
	Running() bool
}

type API interface {
	Start() error
	Shutdown(context.Context) error
	Listening() bool
}

type failureSource interface {
	Failures() <-chan error
}

type Dependencies struct {
	NewEdge                     func(config.Config) (edge.Edge, error)
	NewAPI                      func(config.Config, context.Context) (API, error)
	NewAPIWithStore             func(config.Config, context.Context, *server.ProcessStore) (API, error)
	NewWorkers                  func(config.Config) WorkerSet
	NewWorkersWithStore         func(config.Config, *server.ProcessStore) WorkerSet
	NewLifecycleClient          func(config.Config, *server.ProcessStore) LifecycleClient
	InitObservability           func(context.Context, config.Config) (func(context.Context) error, error)
	ValidateRuntimeCapabilities func(context.Context, config.Config) error
	Register                    func(context.Context, config.Config, string) error
	VerifyPublicEndpoint        func(context.Context, config.Config, string) error
	StartWorkers                func(context.Context, config.Config, *server.ProcessStore) error
	NewProcessStore             func() *server.ProcessStore
}

type Supervisor struct {
	transitionMu sync.Mutex
	mu           sync.RWMutex
	configDir    string
	stateStore   StateStore
	loadConfig   func() (config.Config, error)
	deps         Dependencies
	saveState    func(PersistentState) error
	generation   *generation
	state        PersistentState
}

func (s *Supervisor) ConfigDir() string { return s.configDir }

func New(configDir string, load func() (config.Config, error), deps Dependencies) (*Supervisor, error) {
	if load == nil {
		load = func() (config.Config, error) { return config.LoadFile(filepath.Join(configDir, "config.toml")) }
	}
	if deps.NewProcessStore == nil {
		deps.NewProcessStore = server.NewProcessStore
	}
	stateStore := StateStore{Path: filepath.Join(configDir, "runtime-state.json")}
	_, err := os.Stat(filepath.Join(configDir, "config.toml"))
	state, err := stateStore.Load(err == nil)
	if err != nil {
		return nil, err
	}
	// Actual state describes resources owned by this Supervisor process. A
	// persisted running state cannot prove that this new process owns a live
	// generation after an internal-service restart.
	state.Actual = ActualStopped
	return &Supervisor{configDir: configDir, stateStore: stateStore, loadConfig: load, deps: deps, saveState: stateStore.Save, state: state}, nil
}

// NewSupervisor is the descriptive constructor used by application wiring.
func NewSupervisor(configDir string, load func() (config.Config, error), deps Dependencies) (*Supervisor, error) {
	return New(configDir, load, deps)
}

func boundedContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= maximum {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, maximum)
}

func (s *Supervisor) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := Status{Desired: s.state.Desired, Actual: s.state.Actual, LastError: s.state.LastError}
	if s.generation != nil {
		publicURL, _ := s.generation.snapshot()
		status.PublicURL, status.APIListening, status.WorkersRunning, status.EdgeRunning = publicURL, s.generation.api.Listening(), s.generation.workersRunning(), s.generation.edgeRunning()
		status.HeartbeatRunning = s.generation.heartbeatRunning()
	}
	return status
}
func (s *Supervisor) ExecutionRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Desired == DesiredRunning
}
func (s *Supervisor) updateState(fn func(*PersistentState)) error {
	s.mu.Lock()
	next := s.state
	fn(&next)
	s.state = next
	s.mu.Unlock()
	save := s.saveState
	if save == nil {
		save = s.stateStore.Save
	}
	return save(next)
}
func (s *Supervisor) stateSnapshot() PersistentState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}
func (s *Supervisor) restoreState(state PersistentState) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}
func (s *Supervisor) setGeneration(g *generation) { s.mu.Lock(); s.generation = g; s.mu.Unlock() }
func (s *Supervisor) currentGeneration() *generation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

func (s *Supervisor) Start(ctx context.Context) error {
	ctx, cancel := boundedContext(ctx, startTimeout)
	defer cancel()
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	return s.startLocked(ctx)
}

func (s *Supervisor) startLocked(ctx context.Context) error {
	previousState := s.stateSnapshot()
	if err := s.updateState(func(st *PersistentState) { st.Desired, st.Actual, st.LastError = DesiredRunning, ActualStarting, "" }); err != nil {
		s.restoreState(previousState)
		return err
	}
	if g := s.currentGeneration(); g != nil && g.isExecuting() {
		return s.updateState(func(st *PersistentState) { st.Actual = ActualRunning })
	}
	if g := s.currentGeneration(); g != nil {
		if _, localErr := s.teardownGeneration(ctx, g, "start_cleanup", false); localErr != nil {
			return s.fail(DesiredRunning, localErr)
		}
		if !g.localResourcesClosed() {
			return s.fail(DesiredRunning, errors.New("previous runtime generation still owns resources"))
		}
		s.setGeneration(nil)
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return s.fail(DesiredRunning, err)
	}
	if err := config.ValidateForPlatform(cfg, runtime.GOOS); err != nil {
		return s.fail(DesiredRunning, err)
	}
	g, err := s.newGeneration(ctx, cfg)
	if err != nil {
		return s.fail(DesiredRunning, err)
	}
	s.setGeneration(g)
	if err := s.activate(ctx, g); err != nil {
		return s.rollback(err)
	}
	if err := s.updateState(func(st *PersistentState) { st.Desired, st.Actual, st.LastError = DesiredRunning, ActualRunning, "" }); err != nil {
		return s.rollback(err)
	}
	s.monitorGeneration(g)
	return nil
}

func (s *Supervisor) newGeneration(parent context.Context, cfg config.Config) (result *generation, resultErr error) {
	// Generation lifetime is owned by the supervisor, not by the bounded
	// transition context. The latter is canceled as soon as Start/Reconcile
	// returns and must never tear down a successfully activated generation.
	ctx, cancel := context.WithCancel(context.Background())
	g := &generation{cfg: cfg, ctx: ctx, cancel: cancel, fatal: make(chan error, 1), workersClosed: true, apiClosed: true, edgeClosed: true, otelClosed: true, contextClosed: false, stopHeartbeat: func() {}}
	created := false
	defer func() {
		if created {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		remoteErr, localErr := s.teardownGeneration(cleanupCtx, g, "generation_create_failed", false)
		cleanupCancel()
		resultErr = errors.Join(resultErr, remoteErr, localErr)
	}()
	if s.deps.NewProcessStore != nil {
		g.store = s.deps.NewProcessStore()
	} else {
		g.store = server.NewProcessStore()
	}
	if s.deps.InitObservability != nil {
		shutdown, err := s.deps.InitObservability(ctx, cfg)
		if err != nil {
			cancel()
			return nil, err
		}
		g.shutdownObservability = shutdown
		g.setOTELClosed(false)
	}
	if s.deps.NewAPIWithStore != nil {
		api, err := s.deps.NewAPIWithStore(cfg, ctx, g.store)
		if err != nil {
			cancel()
			return nil, err
		}
		g.api = api
		g.setAPIClosed(false)
	} else if s.deps.NewAPI != nil {
		api, err := s.deps.NewAPI(cfg, ctx)
		if err != nil {
			cancel()
			return nil, err
		}
		g.api = api
		g.setAPIClosed(false)
	}
	if g.api == nil {
		if cfg.Server.APIListen != "" {
			api, err := NewHTTPAPI(cfg, http.NewServeMux())
			if err != nil {
				cancel()
				return nil, err
			}
			g.api = api
			g.setAPIClosed(false)
		} else {
			g.api = &noopAPI{}
		}
	}
	if s.deps.NewWorkersWithStore != nil {
		g.workers = s.deps.NewWorkersWithStore(cfg, g.store)
	} else if s.deps.NewWorkers != nil {
		g.workers = s.deps.NewWorkers(cfg)
	} else if s.deps.StartWorkers != nil {
		g.workers = &callbackWorkers{start: func(ctx context.Context) error { return s.deps.StartWorkers(ctx, cfg, g.store) }}
	}
	if s.deps.NewLifecycleClient != nil {
		g.lifecycle = s.deps.NewLifecycleClient(cfg, g.store)
	}
	if s.deps.NewEdge != nil {
		e, err := s.deps.NewEdge(cfg)
		if err != nil {
			cancel()
			return nil, err
		}
		g.edge = e
		g.setEdgeClosed(false)
	}
	g.watchFailureSource(g.api)
	g.watchFailureSource(g.edge)
	created = true
	return g, nil
}

func (s *Supervisor) activate(ctx context.Context, g *generation) error {
	cfg := g.cfg
	if err := g.api.Start(); err != nil {
		return fmt.Errorf("start execution API: %w", err)
	}
	if err := g.fatalError(); err != nil {
		return err
	}
	if g.edge != nil {
		origin, err := localOriginURL(cfg.Server.APIListen)
		if err != nil {
			return fmt.Errorf("build edge origin: %w", err)
		}
		if err := func() error {
			edgeCtx, cancel := boundedContext(ctx, edgeStartTimeout)
			defer cancel()
			url, err := g.edge.Start(edgeCtx, origin)
			if err == nil {
				g.setPublicURL(url)
			}
			return err
		}(); err != nil {
			return fmt.Errorf("start edge: %w", err)
		}
		if err := g.fatalError(); err != nil {
			return err
		}
	}
	if s.deps.VerifyPublicEndpoint != nil {
		if err := s.deps.VerifyPublicEndpoint(ctx, cfg, g.publicURL); err != nil {
			return fmt.Errorf("verify public endpoint: %w", err)
		}
	}
	if s.deps.ValidateRuntimeCapabilities != nil {
		if err := s.deps.ValidateRuntimeCapabilities(ctx, cfg); err != nil {
			return fmt.Errorf("validate runtime capabilities: %w", err)
		}
	}
	if err := g.fatalError(); err != nil {
		return err
	}
	if s.deps.Register != nil {
		if err := s.deps.Register(ctx, cfg, g.publicURL); err != nil {
			return fmt.Errorf("register runtime: %w", err)
		}
	}
	if err := g.fatalError(); err != nil {
		return err
	}
	if g.workers != nil || s.deps.StartWorkers != nil {
		g.setWorkersClosed(false)
		if err := g.workers.Start(ctx); err != nil {
			return fmt.Errorf("start workers: %w", err)
		}
		g.setWorkersClosed(false)
	}
	if g.lifecycle != nil {
		if err := g.fatalError(); err != nil {
			return err
		}
		if err := g.lifecycle.Resume(ctx, "runtime_start"); err != nil {
			return fmt.Errorf("resume lifecycle: %w", err)
		}
		if err := g.lifecycle.Heartbeat(ctx); err != nil {
			return fmt.Errorf("initial heartbeat: %w", err)
		}
		if err := g.fatalError(); err != nil {
			return err
		}
		stop := g.lifecycle.StartHeartbeatLoop(g.ctx)
		var stopOnce sync.Once
		g.mu.Lock()
		g.stopHeartbeat = func() {
			stopOnce.Do(func() {
				stop()
				g.mu.Lock()
				g.heartbeatActive = false
				g.mu.Unlock()
			})
		}
		g.heartbeatActive = true
		g.mu.Unlock()
	}
	if err := g.fatalError(); err != nil {
		return err
	}
	g.setExecuting(true)
	return nil
}

func (s *Supervisor) rollback(startErr error) error {
	g := s.currentGeneration()
	if g == nil {
		return s.fail(DesiredRunning, startErr)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	remoteErr, localErr := s.teardownGeneration(cleanupCtx, g, "start_failed", true)
	if localErr == nil && g != nil && g.localResourcesClosed() {
		s.setGeneration(nil)
	}
	combined := errors.Join(startErr, remoteErr, localErr)
	stateErr := s.updateState(func(st *PersistentState) {
		st.Desired, st.Actual, st.LastError = DesiredRunning, ActualFailed, combined.Error()
	})
	return errors.Join(combined, stateErr)
}

func (s *Supervisor) fail(desired DesiredState, err error) error {
	stateErr := s.updateState(func(st *PersistentState) { st.Desired, st.Actual, st.LastError = desired, ActualFailed, err.Error() })
	return errors.Join(err, stateErr)
}

func (s *Supervisor) monitorGeneration(g *generation) {
	if g == nil || g.fatal == nil {
		return
	}
	go func() {
		select {
		case <-g.ctx.Done():
			return
		case err := <-g.fatal:
			if err == nil {
				return
			}
			s.handleGenerationFailure(g, err)
		}
	}()
}

func (s *Supervisor) handleGenerationFailure(g *generation, componentErr error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	if s.currentGeneration() != g || !g.isExecuting() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	remoteErr, localErr := s.teardownGeneration(ctx, g, "component_failed", true)
	cancel()
	combined := errors.Join(componentErr, remoteErr, localErr)
	if localErr == nil && g.localResourcesClosed() {
		s.setGeneration(nil)
	}
	stateErr := s.updateState(func(st *PersistentState) {
		st.Desired, st.Actual, st.LastError = DesiredRunning, ActualFailed, combined.Error()
	})
	if stateErr != nil {
		log.Printf("persist runtime failure state: %v", stateErr)
	}
}

func (s *Supervisor) Stop(ctx context.Context) error {
	ctx, cancel := boundedContext(ctx, stopTimeout)
	defer cancel()
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	stateErr := s.updateState(func(st *PersistentState) { st.Desired, st.Actual, st.LastError = DesiredStopped, ActualStopping, "" })
	g := s.currentGeneration()
	if g == nil {
		return errors.Join(stateErr, s.updateState(func(st *PersistentState) { st.Actual = ActualStopped }))
	}
	remoteErr, localErr := s.teardownGeneration(ctx, g, "runtime_stop", true)
	if localErr == nil && g.localResourcesClosed() {
		s.setGeneration(nil)
		finalErr := s.updateState(func(st *PersistentState) {
			st.Desired, st.Actual, st.LastError = DesiredStopped, ActualStopped, ""
			if remoteErr != nil {
				st.LastError = remoteErr.Error()
			}
		})
		return errors.Join(stateErr, remoteErr, finalErr)
	}
	combined := errors.Join(remoteErr, localErr)
	finalErr := s.updateState(func(st *PersistentState) {
		st.Desired, st.Actual, st.LastError = DesiredStopped, ActualFailed, combined.Error()
	})
	return errors.Join(stateErr, combined, finalErr)
}

func (s *Supervisor) Restart(ctx context.Context) error {
	ctx, cancel := boundedContext(ctx, startTimeout)
	defer cancel()
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	previousState := s.stateSnapshot()
	if err := s.updateState(func(st *PersistentState) { st.Desired, st.Actual, st.LastError = DesiredRunning, ActualStopping, "" }); err != nil {
		s.restoreState(previousState)
		return err
	}
	if g := s.currentGeneration(); g != nil {
		remoteErr, localErr := s.teardownGeneration(ctx, g, "runtime_restart", true)
		if localErr != nil {
			return s.fail(DesiredRunning, errors.Join(remoteErr, localErr))
		}
		if !g.localResourcesClosed() {
			return s.fail(DesiredRunning, errors.New("previous generation is not closed"))
		}
		s.setGeneration(nil)
	}
	return s.startLocked(ctx)
}

func (s *Supervisor) Reconcile(ctx context.Context, cfg config.Config) error {
	ctx, cancel := boundedContext(ctx, startTimeout)
	defer cancel()
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	desired := func() DesiredState { s.mu.RLock(); defer s.mu.RUnlock(); return s.state.Desired }()
	if desired == DesiredStopped {
		if g := s.currentGeneration(); g != nil {
			remoteErr, localErr := s.teardownGeneration(ctx, g, "config_reconcile", false)
			if localErr != nil || !g.localResourcesClosed() {
				if localErr == nil {
					localErr = errors.New("generation teardown incomplete")
				}
				return s.fail(desired, errors.Join(remoteErr, localErr))
			}
			s.setGeneration(nil)
		}
		return s.updateState(func(st *PersistentState) { st.Actual, st.LastError = ActualStopped, "" })
	}
	if g := s.currentGeneration(); g != nil {
		remoteErr, localErr := s.teardownGeneration(ctx, g, "config_reconcile", desired == DesiredRunning)
		if localErr != nil {
			return s.fail(desired, errors.Join(remoteErr, localErr))
		}
		if !g.localResourcesClosed() {
			return s.fail(desired, errors.New("generation teardown incomplete"))
		}
		s.setGeneration(nil)
		if remoteErr != nil {
			return s.fail(desired, remoteErr)
		}
	}
	if err := s.updateState(func(st *PersistentState) { st.Actual = ActualStopped; st.LastError = "" }); err != nil {
		return s.fail(desired, err)
	}
	g, err := s.newGeneration(ctx, cfg)
	if err != nil {
		return s.fail(desired, err)
	}
	s.setGeneration(g)
	if desired == DesiredRunning {
		if err := s.activate(ctx, g); err != nil {
			return s.rollback(err)
		}
		if err := s.updateState(func(st *PersistentState) { st.Actual = ActualRunning }); err != nil {
			return s.rollback(err)
		}
		s.monitorGeneration(g)
		return nil
	}
	if _, err := s.teardownGeneration(ctx, g, "config_reconcile", false); err != nil {
		return s.fail(desired, err)
	}
	if !g.localResourcesClosed() {
		return s.fail(desired, errors.New("stopped generation teardown incomplete"))
	}
	s.setGeneration(nil)
	return s.updateState(func(st *PersistentState) { st.Actual = ActualStopped })
}

func (s *Supervisor) Close(ctx context.Context) error {
	ctx, cancel := boundedContext(ctx, stopTimeout)
	defer cancel()
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	desired := func() DesiredState { s.mu.RLock(); defer s.mu.RUnlock(); return s.state.Desired }()
	g := s.currentGeneration()
	if g == nil {
		return s.updateState(func(st *PersistentState) { st.Desired, st.Actual = desired, ActualStopped })
	}
	remoteErr, localErr := s.teardownGeneration(ctx, g, "service_shutdown", true)
	if localErr == nil && g.localResourcesClosed() {
		s.setGeneration(nil)
		finalErr := s.updateState(func(st *PersistentState) {
			st.Desired, st.Actual, st.LastError = desired, ActualStopped, ""
			if remoteErr != nil {
				st.LastError = remoteErr.Error()
			}
		})
		return errors.Join(remoteErr, finalErr)
	}
	combined := errors.Join(remoteErr, localErr)
	finalErr := s.updateState(func(st *PersistentState) {
		st.Desired, st.Actual, st.LastError = desired, ActualFailed, combined.Error()
	})
	return errors.Join(combined, finalErr)
}

func (s *Supervisor) teardownGeneration(ctx context.Context, g *generation, reason string, pause bool) (remoteErr, localErr error) {
	if g == nil {
		return nil, nil
	}
	g.stopHeartbeat()
	if pause && g.lifecycle != nil {
		pauseCtx, cancel := boundedContext(ctx, pauseTimeout)
		remoteErr = g.lifecycle.Pause(pauseCtx, reason)
		cancel()
	}
	if g.workers != nil {
		g.workers.StopAll()
		g.setWorkersClosed(false)
	}
	if g.api != nil && !g.apiIsClosed() {
		if err := g.api.Shutdown(ctx); err != nil {
			localErr = errors.Join(localErr, err)
		} else {
			g.setAPIClosed(true)
		}
	}
	if g.edge != nil && !g.edgeIsClosed() {
		if err := g.edge.Stop(ctx); err != nil {
			localErr = errors.Join(localErr, err)
		} else {
			g.setPublicURL("")
			if err := g.edge.Close(); err != nil {
				localErr = errors.Join(localErr, err)
			} else {
				g.setEdgeClosed(true)
			}
		}
	}
	if g.workers != nil && !g.workersAreClosed() {
		if err := g.workers.WaitAll(ctx); err != nil {
			localErr = errors.Join(localErr, err)
		} else {
			g.setWorkersClosed(true)
		}
	}
	if g.shutdownObservability != nil && !g.otelIsClosed() {
		if err := g.shutdownObservability(ctx); err != nil {
			localErr = errors.Join(localErr, err)
		} else {
			g.setOTELClosed(true)
		}
	}
	if !g.contextIsClosed() {
		g.cancel()
		g.setContextClosed(true)
	}
	g.setExecuting(false)
	return remoteErr, localErr
}

type generation struct {
	mu                                                              sync.RWMutex
	cfg                                                             config.Config
	ctx                                                             context.Context
	cancel                                                          context.CancelFunc
	api                                                             API
	store                                                           *server.ProcessStore
	workers                                                         WorkerSet
	lifecycle                                                       LifecycleClient
	edge                                                            edge.Edge
	publicURL                                                       string
	fatal                                                           chan error
	stopHeartbeat                                                   func()
	shutdownObservability                                           func(context.Context) error
	executing, heartbeatActive                                      bool
	workersClosed, apiClosed, edgeClosed, otelClosed, contextClosed bool
}

func (g *generation) watchFailureSource(component any) {
	source, ok := component.(failureSource)
	if !ok {
		return
	}
	failures := source.Failures()
	if failures == nil {
		return
	}
	go func() {
		select {
		case <-g.ctx.Done():
		case err, ok := <-failures:
			if ok && err != nil {
				select {
				case g.fatal <- err:
				default:
				}
			}
		}
	}()
}

func (g *generation) fatalError() error {
	if g == nil || g.fatal == nil {
		return nil
	}
	select {
	case err := <-g.fatal:
		return err
	default:
		return nil
	}
}

func (g *generation) localResourcesClosed() bool {
	if g == nil {
		return true
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.workersClosed && g.apiClosed && g.edgeClosed && g.otelClosed && g.contextClosed
}
func (g *generation) apiIsClosed() bool  { g.mu.RLock(); defer g.mu.RUnlock(); return g.apiClosed }
func (g *generation) edgeIsClosed() bool { g.mu.RLock(); defer g.mu.RUnlock(); return g.edgeClosed }
func (g *generation) otelIsClosed() bool { g.mu.RLock(); defer g.mu.RUnlock(); return g.otelClosed }
func (g *generation) contextIsClosed() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.contextClosed
}
func (g *generation) workersAreClosed() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.workersClosed
}
func (g *generation) setAPIClosed(v bool)     { g.mu.Lock(); g.apiClosed = v; g.mu.Unlock() }
func (g *generation) setEdgeClosed(v bool)    { g.mu.Lock(); g.edgeClosed = v; g.mu.Unlock() }
func (g *generation) setOTELClosed(v bool)    { g.mu.Lock(); g.otelClosed = v; g.mu.Unlock() }
func (g *generation) setContextClosed(v bool) { g.mu.Lock(); g.contextClosed = v; g.mu.Unlock() }
func (g *generation) setWorkersClosed(v bool) { g.mu.Lock(); g.workersClosed = v; g.mu.Unlock() }
func (g *generation) workersRunning() bool {
	return g != nil && g.workers != nil && g.workers.Running()
}
func (g *generation) edgeRunning() bool { return g != nil && g.edge != nil && g.edge.Running() }
func (g *generation) heartbeatRunning() bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.heartbeatActive
}
func (g *generation) isExecuting() bool         { g.mu.RLock(); defer g.mu.RUnlock(); return g.executing }
func (g *generation) setExecuting(value bool)   { g.mu.Lock(); g.executing = value; g.mu.Unlock() }
func (g *generation) setPublicURL(value string) { g.mu.Lock(); g.publicURL = value; g.mu.Unlock() }
func (g *generation) snapshot() (string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.publicURL, g.executing
}

type noopAPI struct{}

type callbackWorkers struct {
	mu      sync.Mutex
	start   func(context.Context) error
	running bool
}

func (w *callbackWorkers) Start(ctx context.Context) error {
	err := w.start(ctx)
	if err == nil {
		w.mu.Lock()
		w.running = true
		w.mu.Unlock()
	}
	return err
}
func (w *callbackWorkers) StopAll()                    { w.mu.Lock(); w.running = false; w.mu.Unlock() }
func (*callbackWorkers) WaitAll(context.Context) error { return nil }
func (w *callbackWorkers) Running() bool               { w.mu.Lock(); defer w.mu.Unlock(); return w.running }

func (*noopAPI) Start() error                   { return nil }
func (*noopAPI) Shutdown(context.Context) error { return nil }
func (*noopAPI) Listening() bool                { return false }

type HTTPAPI struct {
	Listener        net.Listener
	Server          *http.Server
	shutdownTimeout time.Duration
	done            chan struct{}
	failures        chan error
	mu              sync.Mutex
	closed          bool
	started         bool
	serving         bool
	expectedClose   bool
}

var listenTCP = net.Listen

func NewHTTPAPI(cfg config.Config, handler http.Handler) (*HTTPAPI, error) {
	l, err := listenTCP("tcp", cfg.Server.APIListen)
	if err != nil {
		return nil, err
	}
	return &HTTPAPI{
		Listener:        l,
		Server:          &http.Server{Handler: handler, ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeout)},
		shutdownTimeout: time.Duration(cfg.Server.ShutdownTimeout),
		done:            make(chan struct{}),
		failures:        make(chan error, 1),
	}, nil
}
func (a *HTTPAPI) Start() error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}
	if a.closed {
		a.mu.Unlock()
		return errors.New("execution API is closed")
	}
	a.started = true
	a.serving = true
	a.mu.Unlock()
	go func() {
		err := a.Server.Serve(a.Listener)
		a.mu.Lock()
		expected := a.expectedClose
		a.serving = false
		a.closed = true
		a.mu.Unlock()
		close(a.done)
		if !expected {
			if err == nil {
				err = errors.New("execution API exited unexpectedly")
			}
			a.reportFailure(fmt.Errorf("execution API serve failed: %s", err.Error()))
		}
	}()
	return nil
}
func (a *HTTPAPI) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	if !a.started {
		a.expectedClose = true
		a.closed = true
		close(a.done)
		listener := a.Listener
		a.mu.Unlock()
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
		return nil
	}
	a.expectedClose = true
	a.mu.Unlock()
	shutdownCtx := ctx
	cancel := func() {}
	if a.shutdownTimeout > 0 {
		shutdownCtx, cancel = boundedContext(ctx, a.shutdownTimeout)
	}
	err := a.Server.Shutdown(shutdownCtx)
	cancel()
	select {
	case <-a.done:
		return err
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}
func (a *HTTPAPI) Listening() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.serving
}

func (a *HTTPAPI) reportFailure(err error) {
	select {
	case a.failures <- err:
	default:
	}
}

func (a *HTTPAPI) Failures() <-chan error { return a.failures }
