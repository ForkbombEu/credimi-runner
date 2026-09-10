package runtimesupervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

// HTTPAPI owns the execution API for a single runtime generation.
type HTTPAPI struct {
	Listener        net.Listener
	Server          *http.Server
	shutdownTimeout time.Duration

	serveDone chan struct{}
	failures  chan error

	mu sync.Mutex

	started           bool
	shutdownRequested bool
}

func NewHTTPAPI(cfg config.Config, handler http.Handler) (*HTTPAPI, error) {
	listener, err := net.Listen("tcp", cfg.Server.APIListen)
	if err != nil {
		return nil, err
	}

	return newHTTPAPI(cfg, handler, listener), nil
}

func newHTTPAPI(cfg config.Config, handler http.Handler, listener net.Listener) *HTTPAPI {
	return &HTTPAPI{
		Listener: listener,
		Server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeout),
		},
		shutdownTimeout: time.Duration(cfg.Server.ShutdownTimeout),
		serveDone:       make(chan struct{}),
		failures:        make(chan error, 1),
	}
}

func (a *HTTPAPI) LocalOrigin() (string, error) {
	if a == nil || a.Listener == nil {
		return "", errors.New("execution API listener is nil")
	}
	return localOriginURL(a.Listener.Addr().String())
}

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (a *HTTPAPI) Start() error {
	if a == nil {
		return errors.New("execution API is nil")
	}

	a.mu.Lock()
	if a.shutdownRequested {
		a.mu.Unlock()
		return errors.New("execution API shutdown has already started")
	}
	if a.started {
		a.mu.Unlock()
		if channelClosed(a.serveDone) {
			return errors.New("execution API has already stopped")
		}
		return nil
	}
	a.started = true
	a.mu.Unlock()

	go a.serve()
	return nil
}

func (a *HTTPAPI) serve() {
	err := a.Server.Serve(a.Listener)

	a.mu.Lock()
	expected := a.shutdownRequested
	a.mu.Unlock()

	close(a.serveDone)

	if expected {
		return
	}
	if err == nil {
		a.reportFailure(errors.New("execution API exited unexpectedly"))
		return
	}
	a.reportFailure(fmt.Errorf("execution API serve failed: %w", err))
}

func (a *HTTPAPI) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}

	a.mu.Lock()
	if !a.started {
		a.shutdownRequested = true
		listener := a.Listener
		a.mu.Unlock()

		err := listener.Close()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
	a.shutdownRequested = true
	a.mu.Unlock()

	shutdownCtx, cancel := boundedContext(ctx, a.shutdownTimeout)
	defer cancel()

	if err := a.Server.Shutdown(shutdownCtx); err != nil {
		return err
	}

	select {
	case <-a.serveDone:
		return nil
	default:
	}

	select {
	case <-a.serveDone:
		return nil
	case <-shutdownCtx.Done():
		return shutdownCtx.Err()
	}
}

func (a *HTTPAPI) Listening() bool {
	if a == nil {
		return false
	}

	a.mu.Lock()
	started := a.started
	a.mu.Unlock()

	return started && !channelClosed(a.serveDone)
}

func (a *HTTPAPI) reportFailure(err error) {
	select {
	case a.failures <- err:
	default:
	}
}

func (a *HTTPAPI) Failures() <-chan error { return a.failures }
