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

const UpgradeRunnerImage = "upgrade-runner-image"

type request struct {
	Operation string `json:"operation"`
}

type response struct {
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

type Server struct {
	listener net.Listener
	close    chan struct{}
	closed   sync.Once
	upgrade  func(context.Context) error
	busy     func() bool
}

// Serve starts the private launcher control channel. The socket is deliberately
// an allow-listed application operation, not a command or Docker API proxy.
func Serve(path string, upgrade func(context.Context) error, busy func() bool) (*Server, error) {
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
	server := &Server{listener: listener, close: make(chan struct{}), upgrade: upgrade, busy: busy}
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
	if request.Operation != UpgradeRunnerImage {
		writeResponse(connection, response{Error: "unsupported launcher operation"})
		return
	}
	if s.busy != nil && s.busy() {
		writeResponse(connection, response{Error: "runner is busy; retry the upgrade when idle"})
		return
	}
	writeResponse(connection, response{Accepted: true})
	go func() {
		// The dashboard request is only an admission check. Re-read busy state
		// immediately before the destructive lifecycle operation so work that
		// started during the request cannot be interrupted by an upgrade.
		if s.busy != nil && s.busy() {
			return
		}
		_ = s.upgrade(context.Background())
	}()
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
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("connect to runner launcher: %w", err)
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(request{Operation: UpgradeRunnerImage}); err != nil {
		return fmt.Errorf("send runner upgrade request: %w", err)
	}
	var response response
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
		return fmt.Errorf("read runner upgrade response: %w", err)
	}
	if !response.Accepted {
		if response.Error == "" {
			return errors.New("runner launcher rejected upgrade")
		}
		return errors.New(response.Error)
	}
	return nil
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
