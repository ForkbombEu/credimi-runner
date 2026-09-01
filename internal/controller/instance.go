package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/atomicfile"
)

var ErrAlreadyRunning = errors.New("credimi runner dashboard is already running")

type Metadata struct {
	Schema            int       `json:"schema"`
	ControllerID      string    `json:"controller_id"`
	PID               int       `json:"pid"`
	StartedAt         time.Time `json:"started_at"`
	ConfigDir         string    `json:"config_dir"`
	ListenHost        string    `json:"listen_host"`
	ListenPort        int       `json:"listen_port"`
	ProbeURL          string    `json:"probe_url"`
	PublicURL         string    `json:"public_url"`
	ConfigFingerprint string    `json:"config_fingerprint"`
	IdentityToken     string    `json:"identity_token"`
}

func NewIdentityToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate controller identity token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

type Lease struct {
	file      *os.File
	configDir string
	metadata  string
	owned     bool
}

func Acquire(configDir string) (*Lease, error) {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return nil, errors.New("controller config directory is empty")
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("create controller config directory: %w", err)
	}
	path := filepath.Join(configDir, "controller.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open controller lock: %w", err)
	}
	if err := atomicfile.RepairOwnership(path, atomicfile.FromEnvironment()); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("set controller lock owner: %w", err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errLockBusy) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock controller: %w", err)
	}
	return &Lease{file: file, configDir: configDir, metadata: filepath.Join(configDir, "controller.json"), owned: true}, nil
}

func (l *Lease) Publish(metadata Metadata) error {
	if l == nil || l.file == nil || !l.owned {
		return errors.New("controller lease is not held")
	}
	metadata.Schema = 1
	if metadata.StartedAt.IsZero() {
		metadata.StartedAt = time.Now().UTC()
	} else {
		metadata.StartedAt = metadata.StartedAt.UTC()
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return atomicfile.WriteAtomic(l.metadata, 0o600, atomicfile.FromEnvironment(), func(writer io.Writer) error {
		if _, err := writer.Write(raw); err != nil {
			return fmt.Errorf("write controller metadata: %w", err)
		}
		return nil
	})
}

func ReadMetadata(configDir string) (Metadata, error) {
	var metadata Metadata
	raw, err := os.ReadFile(filepath.Join(configDir, "controller.json"))
	if err != nil {
		return metadata, err
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return metadata, fmt.Errorf("decode controller metadata: %w", err)
	}
	if metadata.Schema != 1 || metadata.ListenPort < 1 || metadata.ListenPort > 65535 || metadata.ProbeURL == "" || metadata.ControllerID == "" || metadata.IdentityToken == "" {
		return metadata, errors.New("controller metadata is invalid")
	}
	return metadata, nil
}

func Probe(ctx context.Context, metadata Metadata) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata.ProbeURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-Credimi-Controller-Token", metadata.IdentityToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("controller probe returned %s", response.Status)
	}
	var identity struct {
		ControllerID      string `json:"controller_id"`
		ConfigFingerprint string `json:"config_fingerprint"`
	}
	if err := json.NewDecoder(response.Body).Decode(&identity); err != nil {
		return fmt.Errorf("decode controller identity: %w", err)
	}
	if identity.ControllerID != metadata.ControllerID || identity.ConfigFingerprint != metadata.ConfigFingerprint {
		return errors.New("controller identity does not match metadata")
	}
	return nil
}

func (l *Lease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	if l.owned {
		_ = os.Remove(l.metadata)
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	l.owned = false
	return errors.Join(unlockErr, closeErr)
}
