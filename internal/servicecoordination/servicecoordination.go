// Package servicecoordination contains the small shared-file protocol between
// the Dashboard and an attached host Credimi Runner command.
package servicecoordination

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/atomicfile"
)

const (
	CoordinatorFile      = "service-coordinator.json"
	RestartRequestFile   = "service-restart-request.json"
	RestartResultFile    = "service-restart-result.json"
	Protocol             = 1
	CoordinatorMaxAge    = 15 * time.Second
	CoordinatorHeartbeat = 5 * time.Second
)

type Presence struct {
	PID       int       `json:"pid"`
	Protocol  int       `json:"protocol"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StartPresence publishes an attached host command and refreshes it until ctx
// is canceled. The cleanup only removes this process's own record.
func StartPresence(ctx context.Context, configDir string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := WritePresence(configDir, time.Now()); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			close(done)
		})
		<-finished
		if presence, err := ReadPresence(configDir); err == nil && presence.PID == os.Getpid() {
			_ = RemovePresence(configDir)
		}
	}
	go func() {
		defer close(finished)
		ticker := time.NewTicker(CoordinatorHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case now := <-ticker.C:
				_ = WritePresence(configDir, now)
			}
		}
	}()
	return cleanup, nil
}

type RestartRequest struct {
	RequestID            string    `json:"request_id"`
	RequestedFingerprint string    `json:"requested_fingerprint"`
	CreatedAt            time.Time `json:"created_at"`
}

type RestartResult struct {
	RequestID          string    `json:"request_id"`
	Success            bool      `json:"success"`
	AppliedFingerprint string    `json:"applied_fingerprint,omitempty"`
	Error              string    `json:"error,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func WritePresence(configDir string, now time.Time) error {
	return writeJSON(filepath.Join(configDir, CoordinatorFile), Presence{PID: os.Getpid(), Protocol: Protocol, UpdatedAt: now.UTC()})
}

func ReadPresence(configDir string) (Presence, error) {
	var presence Presence
	if err := readJSON(filepath.Join(configDir, CoordinatorFile), &presence); err != nil {
		return presence, err
	}
	if presence.Protocol != Protocol || presence.PID <= 0 || presence.UpdatedAt.IsZero() {
		return Presence{}, errors.New("service coordinator state is invalid")
	}
	return presence, nil
}

func CoordinatorActive(configDir string, now time.Time) (bool, error) {
	presence, err := ReadPresence(configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return now.Sub(presence.UpdatedAt) >= 0 && now.Sub(presence.UpdatedAt) <= CoordinatorMaxAge, nil
}

func RemovePresence(configDir string) error {
	err := os.Remove(filepath.Join(configDir, CoordinatorFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func NewRestartRequest(fingerprint string, now time.Time) (RestartRequest, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return RestartRequest{}, fmt.Errorf("generate service restart request ID: %w", err)
	}
	return RestartRequest{
		RequestID:            hex.EncodeToString(bytes),
		RequestedFingerprint: strings.TrimSpace(fingerprint),
		CreatedAt:            now.UTC(),
	}, nil
}

func WriteRestartRequest(configDir string, request RestartRequest) error {
	if strings.TrimSpace(request.RequestID) == "" || strings.TrimSpace(request.RequestedFingerprint) == "" || request.CreatedAt.IsZero() {
		return errors.New("service restart request is incomplete")
	}
	return writeJSON(filepath.Join(configDir, RestartRequestFile), request)
}

func ReadRestartRequest(configDir string) (RestartRequest, error) {
	var request RestartRequest
	if err := readJSON(filepath.Join(configDir, RestartRequestFile), &request); err != nil {
		return request, err
	}
	if strings.TrimSpace(request.RequestID) == "" || strings.TrimSpace(request.RequestedFingerprint) == "" || request.CreatedAt.IsZero() {
		return RestartRequest{}, errors.New("service restart request is invalid")
	}
	return request, nil
}

func WriteRestartResult(configDir string, result RestartResult) error {
	if strings.TrimSpace(result.RequestID) == "" || result.UpdatedAt.IsZero() {
		return errors.New("service restart result is incomplete")
	}
	return writeJSON(filepath.Join(configDir, RestartResultFile), result)
}

func ReadRestartResult(configDir string) (RestartResult, error) {
	var result RestartResult
	if err := readJSON(filepath.Join(configDir, RestartResultFile), &result); err != nil {
		return result, err
	}
	if strings.TrimSpace(result.RequestID) == "" || result.UpdatedAt.IsZero() {
		return RestartResult{}, errors.New("service restart result is invalid")
	}
	return result, nil
}

func writeJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return atomicfile.WriteAtomic(path, 0o600, atomicfile.FromEnvironment(), func(writer io.Writer) error {
		_, err := writer.Write(raw)
		return err
	})
}

func readJSON(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, value); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}
