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
)

const (
	UpgradeRunnerImage = "upgrade-runner-image"
	ReconcileConfig    = "reconcile-config"
	RuntimeStart       = "runtime-start"
	RuntimeStop        = "runtime-stop"
	RuntimeRestart     = "runtime-restart"
	QuickTunnelURL     = "quick-tunnel-url"
)

type request struct {
	Operation string `json:"operation"`
}

type response struct {
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
	Value    string `json:"value,omitempty"`
}

// Operations contains the small, typed set of actions the inner dashboard
// may request from the outer launcher. It is intentionally not a command
// execution interface.
type Operations struct {
	ReconcileConfig func(context.Context) error
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
	server := &Server{listener: listener, close: make(chan struct{}), upgrade: upgrade, busy: busy, operations: operations}
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
		writeResponse(connection, response{Accepted: true})
		go func() {
			// Re-check immediately before the destructive operation.
			if s.busy != nil && s.busy() {
				return
			}
			_ = s.upgrade(context.Background())
		}()
	case ReconcileConfig:
		s.acceptAsync(connection, s.operations.ReconcileConfig)
	case RuntimeStart:
		s.acceptAsync(connection, s.operations.RuntimeStart)
	case RuntimeStop:
		s.acceptAsync(connection, s.operations.RuntimeStop)
	case RuntimeRestart:
		s.acceptAsync(connection, s.operations.RuntimeRestart)
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

func (s *Server) acceptAsync(connection net.Conn, operation func(context.Context) error) {
	if operation == nil {
		writeResponse(connection, response{Error: "launcher operation is not configured"})
		return
	}
	writeResponse(connection, response{Accepted: true})
	go func() { _ = operation(context.Background()) }()
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
	return requestOperation(ctx, path, UpgradeRunnerImage)
}

func RequestReconcile(ctx context.Context, path string) error {
	return requestOperation(ctx, path, ReconcileConfig)
}

func RequestRuntimeAction(ctx context.Context, path, action string) error {
	operation := map[string]string{"start": RuntimeStart, "stop": RuntimeStop, "restart": RuntimeRestart}[action]
	if operation == "" {
		return fmt.Errorf("unsupported runtime action %q", action)
	}
	return requestOperation(ctx, path, operation)
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

func requestOperation(ctx context.Context, path, operation string) error {
	connection, err := dial(ctx, path)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(request{Operation: operation}); err != nil {
		return fmt.Errorf("send launcher request: %w", err)
	}
	var response response
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
		return fmt.Errorf("read launcher response: %w", err)
	}
	if !response.Accepted {
		if response.Error == "" {
			return errors.New("runner launcher rejected request")
		}
		return errors.New(response.Error)
	}
	return nil
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
