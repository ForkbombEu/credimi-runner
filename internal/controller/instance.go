package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/atomicfile"
	controlleridentity "github.com/forkbombeu/credimi-runner/internal/controller/identity"
)

var ErrAlreadyRunning = errors.New("credimi runner dashboard is already running")

type Metadata = controlleridentity.Metadata

const ProbeTimeout = controlleridentity.ProbeTimeout

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
	return controlleridentity.ReadMetadata(configDir)
}

func Probe(ctx context.Context, metadata Metadata) error {
	return controlleridentity.Probe(ctx, metadata)
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
