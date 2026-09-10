// Package atomicfile provides the small atomic-write primitive used by files
// shared between the host CLI and the persistent runner container.
package atomicfile

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

const (
	OwnerUIDEnv = "CREDIMI_CONFIG_OWNER_UID"
	OwnerGIDEnv = "CREDIMI_CONFIG_OWNER_GID"
)

type Ownership struct {
	UID int
	GID int
}

// FromEnvironment returns the host ownership contract exported by the service
// container. Native execution leaves it unset and retains normal OS ownership.
func FromEnvironment() *Ownership {
	uid, err := strconv.Atoi(os.Getenv(OwnerUIDEnv))
	if err != nil {
		return nil
	}
	gid, err := strconv.Atoi(os.Getenv(OwnerGIDEnv))
	if err != nil {
		return nil
	}
	if uid < 0 || gid < 0 {
		return nil
	}
	return &Ownership{UID: uid, GID: gid}
}

func RepairOwnership(path string, owner *Ownership) error {
	if owner == nil || os.Geteuid() != 0 {
		return nil
	}
	if err := os.Chown(path, owner.UID, owner.GID); err != nil {
		return fmt.Errorf("set shared file owner: %w", err)
	}
	return nil
}

// WriteAtomic writes a file in the destination directory and replaces the
// destination only after the contents and requested mode are durable. When
// running as root with a configured service owner, ownership is repaired on
// the temporary file before the rename.
func WriteAtomic(path string, mode fs.FileMode, owner *Ownership, write func(io.Writer) error) error {
	return writeAtomic(path, mode, owner, write, os.Chown, os.Geteuid() == 0)
}

// WriteAtomicWithChown is a testable form of WriteAtomic. It is intentionally
// small so ownership tests do not need to run as root.
func WriteAtomicWithChown(path string, mode fs.FileMode, owner *Ownership, write func(io.Writer) error, chown func(string, int, int) error) error {
	return writeAtomic(path, mode, owner, write, chown, true)
}

func writeAtomic(path string, mode fs.FileMode, owner *Ownership, write func(io.Writer) error, chown func(string, int, int) error, repairOwner bool) error {
	if path == "" {
		return fmt.Errorf("atomic file path is empty")
	}
	if write == nil {
		return fmt.Errorf("atomic file writer is nil")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create atomic file directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".atomic-*")
	if err != nil {
		return fmt.Errorf("create atomic file temporary: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if owner != nil && repairOwner {
		if err := chown(temporaryPath, owner.UID, owner.GID); err != nil {
			_ = temporary.Close()
			return fmt.Errorf("set atomic file owner: %w", err)
		}
	}
	if err := write(temporary); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace atomic file: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open atomic file directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync atomic file directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close atomic file directory: %w", closeErr)
	}
	return nil
}
