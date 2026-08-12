package launcher

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	UpgradeRunnerImage  = "upgrade-runner-image"
	ReconcileConfig     = "reconcile-config"
	ReconcileSetup      = "reconcile-setup"
	RuntimeStart        = "runtime-start"
	RuntimeStop         = "runtime-stop"
	RuntimeRestart      = "runtime-restart"
	QuickTunnelURL      = "quick-tunnel-url"
	OperationStatus     = "operation-status"
	setupOperationFile  = "setup-operation"
	configOperationFile = "config-operation"
)

type OperationPhase string

const (
	PhaseQueued    OperationPhase = "queued"
	PhaseRunning   OperationPhase = "running"
	PhaseSucceeded OperationPhase = "succeeded"
	PhaseFailed    OperationPhase = "failed"
)

type OperationHandle struct {
	ID   string
	Kind string
}

type OperationResult struct {
	ID      string
	Kind    string
	Phase   OperationPhase
	Message string
	Error   string
}

type request struct {
	Operation string `json:"operation"`
	ID        string `json:"id,omitempty"`
}

type response struct {
	Accepted    bool           `json:"accepted"`
	Error       string         `json:"error,omitempty"`
	Value       string         `json:"value,omitempty"`
	OperationID string         `json:"operation_id,omitempty"`
	Phase       OperationPhase `json:"phase,omitempty"`
	Message     string         `json:"message,omitempty"`
}

// Operations contains the small, typed set of actions the inner dashboard
// may request from the outer launcher. It is intentionally not a command
// execution interface.
type Operations struct {
	ReconcileConfig func(context.Context) error
	ReconcileSetup  func(context.Context) error
	RuntimeStart    func(context.Context) error
	RuntimeStop     func(context.Context) error
	RuntimeRestart  func(context.Context) error
	QuickTunnelURL  func(context.Context) (string, error)
}

type Server struct {
	listener   net.Listener
	close      chan struct{}
	closed     sync.Once
	upgrade    func(context.Context) error
	busy       func() bool
	operations Operations
	mu         sync.RWMutex
	nextID     uint64
	results    map[string]OperationResult
}

// Serve starts the private launcher control channel. The socket is deliberately
// an allow-listed application operation, not a command or Docker API proxy.
func Serve(path string, upgrade func(context.Context) error, busy func() bool) (*Server, error) {
	return ServeWithOperations(path, upgrade, busy, Operations{})
}

func ServeWithOperations(path string, upgrade func(context.Context) error, busy func() bool, operations Operations) (*Server, error) {
	if upgrade == nil {
		return nil, errors.New("launcher upgrade operation is not configured")
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("launcher control socket path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create launcher control directory: %w", err)
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on launcher control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secure launcher control socket: %w", err)
	}
	server := &Server{listener: listener, close: make(chan struct{}), upgrade: upgrade, busy: busy, operations: operations, results: map[string]OperationResult{}}
	go server.acceptLoop()
	return server, nil
}

func (s *Server) acceptLoop() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.close:
				return
			default:
			}
			continue
		}
		go s.handle(connection)
	}
}

func (s *Server) handle(connection net.Conn) {
	defer connection.Close()
	decoder := json.NewDecoder(bufio.NewReader(&ioLimitReader{Reader: connection, Limit: 4096}))
	decoder.DisallowUnknownFields()
	var request request
	if err := decoder.Decode(&request); err != nil {
		writeResponse(connection, response{Error: "invalid launcher request"})
		return
	}
	switch request.Operation {
	case UpgradeRunnerImage:
		if s.busy != nil && s.busy() {
			writeResponse(connection, response{Error: "runner is busy; retry the upgrade when idle"})
			return
		}
		s.acceptAsync(connection, UpgradeRunnerImage, func(ctx context.Context) error {
			// Re-check immediately before the destructive operation.
			if s.busy != nil && s.busy() {
				return errors.New("runner is busy; retry the upgrade when idle")
			}
			return s.upgrade(ctx)
		})
	case ReconcileConfig:
		s.acceptAsync(connection, ReconcileConfig, s.operations.ReconcileConfig)
	case ReconcileSetup:
		s.acceptAsync(connection, ReconcileSetup, s.operations.ReconcileSetup)
	case RuntimeStart:
		s.acceptAsync(connection, RuntimeStart, s.operations.RuntimeStart)
	case RuntimeStop:
		s.acceptAsync(connection, RuntimeStop, s.operations.RuntimeStop)
	case RuntimeRestart:
		s.acceptAsync(connection, RuntimeRestart, s.operations.RuntimeRestart)
	case OperationStatus:
		s.writeOperationStatus(connection, request.ID)
	case QuickTunnelURL:
		if s.operations.QuickTunnelURL == nil {
			writeResponse(connection, response{Error: "quick tunnel URL operation is not configured"})
			return
		}
		value, err := s.operations.QuickTunnelURL(context.Background())
		if err != nil {
			writeResponse(connection, response{Error: err.Error()})
			return
		}
		writeResponse(connection, response{Accepted: true, Value: value})
	default:
		writeResponse(connection, response{Error: "unsupported launcher operation"})
	}
}

func (s *Server) acceptAsync(connection net.Conn, kind string, operation func(context.Context) error) {
	if operation == nil {
		writeResponse(connection, response{Error: "launcher operation is not configured"})
		return
	}
	handle, err := s.startOperation(kind, operation)
	if err != nil {
		writeResponse(connection, response{Error: err.Error()})
		return
	}
	writeResponse(connection, response{Accepted: true, OperationID: handle.ID, Phase: PhaseQueued})
}

func (s *Server) startOperation(kind string, operation func(context.Context) error) (OperationHandle, error) {
	s.mu.Lock()
	s.nextID++
	handle := OperationHandle{ID: fmt.Sprintf("%s-%d", kind, s.nextID), Kind: kind}
	s.results[handle.ID] = OperationResult{ID: handle.ID, Kind: kind, Phase: PhaseQueued}
	s.mu.Unlock()
	operationFile := ""
	switch kind {
	case ReconcileSetup:
		operationFile = setupOperationFile
	case ReconcileConfig:
		operationFile = configOperationFile
	}
	if operationFile != "" {
		if err := persistOperationReference(filepath.Dir(s.listener.Addr().String()), operationFile, handle.ID); err != nil {
			s.mu.Lock()
			delete(s.results, handle.ID)
			s.mu.Unlock()
			return OperationHandle{}, err
		}
	}
	go func() {
		s.setOperation(OperationResult{ID: handle.ID, Kind: kind, Phase: PhaseRunning})
		err := operation(context.Background())
		result := OperationResult{ID: handle.ID, Kind: kind, Phase: PhaseSucceeded, Message: "completed"}
		if err != nil {
			result.Phase = PhaseFailed
			result.Error = err.Error()
			result.Message = "operation failed"
		}
		s.setOperation(result)
	}()
	return handle, nil
}

func persistOperationReference(configDir, filename, operationID string) error {
	path := filepath.Join(configDir, filename)
	temporary, err := os.CreateTemp(configDir, ".operation-")
	if err != nil {
		return fmt.Errorf("create %s reference: %w", filename, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure %s reference: %w", filename, err)
	}
	if _, err := temporary.WriteString(operationID + "\n"); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s reference: %w", filename, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s reference: %w", filename, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish %s reference: %w", filename, err)
	}
	return nil
}

func (s *Server) setOperation(result OperationResult) {
	s.mu.Lock()
	s.results[result.ID] = result
	s.mu.Unlock()
}

func (s *Server) writeOperationStatus(connection net.Conn, id string) {
	s.mu.RLock()
	result, ok := s.results[id]
	s.mu.RUnlock()
	if !ok {
		writeResponse(connection, response{Error: "launcher operation not found"})
		return
	}
	writeResponse(connection, response{Accepted: true, OperationID: result.ID, Phase: result.Phase, Message: result.Message, Error: result.Error})
}

func writeResponse(connection net.Conn, response response) {
	_ = json.NewEncoder(connection).Encode(response)
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.closed.Do(func() {
		close(s.close)
		err = s.listener.Close()
		_ = os.Remove(s.listener.Addr().String())
	})
	return err
}

// RequestUpgrade asks the outer launcher to replace the Linux runner image.
func RequestUpgrade(ctx context.Context, path string) error {
	handle, err := requestAsync(ctx, path, UpgradeRunnerImage)
	return waitOperation(ctx, path, handle, err)
}

func RequestReconcile(ctx context.Context, path string) error {
	handle, err := RequestReconcileAsync(ctx, path)
	return waitOperation(ctx, path, handle, err)
}

func RequestSetupReconcile(ctx context.Context, path string) error {
	handle, err := RequestSetupReconcileAsync(ctx, path)
	return waitOperation(ctx, path, handle, err)
}

func RequestRuntimeAction(ctx context.Context, path, action string) error {
	operation := map[string]string{"start": RuntimeStart, "stop": RuntimeStop, "restart": RuntimeRestart}[action]
	if operation == "" {
		return fmt.Errorf("unsupported runtime action %q", action)
	}
	handle, err := requestAsync(ctx, path, operation)
	return waitOperation(ctx, path, handle, err)
}

func RequestReconcileAsync(ctx context.Context, path string) (OperationHandle, error) {
	return requestAsync(ctx, path, ReconcileConfig)
}

func RequestSetupReconcileAsync(ctx context.Context, path string) (OperationHandle, error) {
	return requestAsync(ctx, path, ReconcileSetup)
}

// RequestOperationStatus reconnects to an already accepted launcher operation.
// The operation remains owned by the outer launcher, so callers may use this
// after the process that submitted the operation has been replaced.
func RequestOperationStatus(ctx context.Context, path, id string) (OperationResult, error) {
	return operationStatus(ctx, path, id)
}

func RequestRuntimeActionAsync(ctx context.Context, path, action string) (OperationHandle, error) {
	operation := map[string]string{"start": RuntimeStart, "stop": RuntimeStop, "restart": RuntimeRestart}[action]
	if operation == "" {
		return OperationHandle{}, fmt.Errorf("unsupported runtime action %q", action)
	}
	return requestAsync(ctx, path, operation)
}

func RequestQuickTunnelURL(ctx context.Context, path string) (string, error) {
	connection, err := dial(ctx, path)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(request{Operation: QuickTunnelURL}); err != nil {
		return "", fmt.Errorf("send quick tunnel URL request: %w", err)
	}
	var result response
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&result); err != nil {
		return "", fmt.Errorf("read quick tunnel URL response: %w", err)
	}
	if !result.Accepted {
		if result.Error == "" {
			return "", errors.New("runner launcher rejected quick tunnel URL request")
		}
		return "", errors.New(result.Error)
	}
	return result.Value, nil
}

func requestAsync(ctx context.Context, path, operation string) (OperationHandle, error) {
	connection, err := dial(ctx, path)
	if err != nil {
		return OperationHandle{}, err
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(request{Operation: operation}); err != nil {
		return OperationHandle{}, fmt.Errorf("send launcher request: %w", err)
	}
	var result response
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&result); err != nil {
		return OperationHandle{}, fmt.Errorf("read launcher response: %w", err)
	}
	if !result.Accepted {
		if result.Error == "" {
			return OperationHandle{}, errors.New("runner launcher rejected request")
		}
		return OperationHandle{}, errors.New(result.Error)
	}
	if result.OperationID == "" {
		return OperationHandle{}, errors.New("runner launcher returned no operation ID")
	}
	return OperationHandle{ID: result.OperationID, Kind: operation}, nil
}

func waitOperation(ctx context.Context, path string, handle OperationHandle, err error) error {
	if err != nil {
		return err
	}
	for {
		result, queryErr := operationStatus(ctx, path, handle.ID)
		if queryErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return queryErr
		}
		switch result.Phase {
		case PhaseSucceeded:
			return nil
		case PhaseFailed:
			if result.Error != "" {
				return errors.New(result.Error)
			}
			return errors.New(result.Message)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func operationStatus(ctx context.Context, path, id string) (OperationResult, error) {
	connection, err := dial(ctx, path)
	if err != nil {
		return OperationResult{}, err
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(request{Operation: OperationStatus, ID: id}); err != nil {
		return OperationResult{}, fmt.Errorf("send launcher status request: %w", err)
	}
	var result response
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&result); err != nil {
		return OperationResult{}, fmt.Errorf("read launcher status response: %w", err)
	}
	if !result.Accepted {
		return OperationResult{}, errors.New(result.Error)
	}
	return OperationResult{ID: result.OperationID, Phase: result.Phase, Message: result.Message, Error: result.Error}, nil
}

func dial(ctx context.Context, path string) (net.Conn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("connect to runner launcher: %w", err)
	}
	return connection, nil
}

type ioLimitReader struct {
	Reader io.Reader
	Limit  int64
}

func (r *ioLimitReader) Read(p []byte) (int, error) {
	if int64(len(p)) > r.Limit {
		p = p[:r.Limit]
	}
	n, err := r.Reader.Read(p)
	r.Limit -= int64(n)
	if r.Limit <= 0 && err == nil {
		return n, errors.New("launcher request too large")
	}
	return n, err
}
