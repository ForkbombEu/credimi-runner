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
	CoordinatorLockFile  = "service-coordinator.lock"
	RestartRequestFile   = "service-restart-request.json"
	RestartResultFile    = "service-restart-result.json"
	Protocol             = 1
	CoordinatorMaxAge    = 15 * time.Second
	CoordinatorHeartbeat = 5 * time.Second
)

var coordinatorHeartbeat = CoordinatorHeartbeat

type Presence struct {
	PID       int       `json:"pid"`
	Protocol  int       `json:"protocol"`
	Nonce     string    `json:"nonce,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StartPresence publishes an attached host command and refreshes it until ctx
// is canceled. The cleanup only removes this process's own record.
func StartPresence(ctx context.Context, configDir string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("create service coordination directory: %w", err)
	}
	nonce, err := acquireCoordinator(configDir)
	if err != nil {
		return nil, err
	}
	if err := writePresence(configDir, time.Now(), nonce); err != nil {
		_ = releaseCoordinator(configDir, nonce)
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
		if ownsCoordinator(configDir, nonce) == nil {
			presence, err := ReadPresence(configDir)
			if err == nil && presence.PID == os.Getpid() && presence.Nonce == nonce {
				_ = RemovePresence(configDir)
			}
		}
		_ = releaseCoordinator(configDir, nonce)
	}
	go func() {
		defer close(finished)
		ticker := time.NewTicker(coordinatorHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case now := <-ticker.C:
				_ = refreshCoordinator(configDir, now, nonce)
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

func writePresence(configDir string, now time.Time, nonce string) error {
	return writeJSON(filepath.Join(configDir, CoordinatorFile), Presence{PID: os.Getpid(), Protocol: Protocol, Nonce: nonce, UpdatedAt: now.UTC()})
}

func acquireCoordinator(configDir string) (string, error) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("generate service coordinator nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	path := filepath.Join(configDir, CoordinatorLockFile)
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := file.WriteString(nonce); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return "", fmt.Errorf("write service coordinator lock: %w", writeErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(path)
				return "", fmt.Errorf("close service coordinator lock: %w", closeErr)
			}
			return nonce, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create service coordinator lock: %w", err)
		}
		if _, presenceErr := ReadPresence(configDir); presenceErr == nil {
			if active, activeErr := CoordinatorActive(configDir, time.Now()); activeErr == nil && active {
				return "", errors.New("another attached Credimi Runner is already coordinating this config directory")
			}
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("inspect service coordinator lock: %w", statErr)
		}
		if time.Since(info.ModTime()) <= CoordinatorMaxAge {
			return "", errors.New("another attached Credimi Runner is already coordinating this config directory")
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("reclaim stale service coordinator lock: %w", err)
		}
	}
	return "", errors.New("another attached Credimi Runner is already coordinating this config directory")
}

func touchCoordinator(configDir, nonce string) error {
	path := filepath.Join(configDir, CoordinatorLockFile)
	if err := ownsCoordinator(configDir, nonce); err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(path, now, now)
}

func ownsCoordinator(configDir, nonce string) error {
	contents, err := os.ReadFile(filepath.Join(configDir, CoordinatorLockFile))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(contents)) != nonce {
		return errors.New("service coordinator ownership was lost")
	}
	return nil
}

func refreshCoordinator(configDir string, now time.Time, nonce string) error {
	if err := touchCoordinator(configDir, nonce); err != nil {
		return err
	}
	return writePresence(configDir, now, nonce)
}

// CoordinatorOwned reports whether the current process still owns the
// attached-coordinator lease. It prevents an old process from continuing to
// handle requests after a stale lease has been reclaimed.
func CoordinatorOwned(configDir string) bool {
	presence, err := ReadPresence(configDir)
	if err != nil || presence.PID != os.Getpid() || presence.Nonce == "" {
		return false
	}
	return ownsCoordinator(configDir, presence.Nonce) == nil
}

func releaseCoordinator(configDir, nonce string) error {
	path := filepath.Join(configDir, CoordinatorLockFile)
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(string(contents)) != nonce {
		return nil
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
