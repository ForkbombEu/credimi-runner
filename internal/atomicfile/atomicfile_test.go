package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicRepairsOwnershipBeforeRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.json")
	owner := &Ownership{UID: 1000, GID: 1000}
	var chownedPath string
	var chownedUID, chownedGID int
	err := WriteAtomicWithChown(path, 0o600, owner, func(writer io.Writer) error {
		_, err := writer.Write([]byte("ok"))
		return err
	}, func(path string, uid, gid int) error {
		chownedPath, chownedUID, chownedGID = path, uid, gid
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if chownedPath == path || chownedUID != 1000 || chownedGID != 1000 {
		t.Fatalf("chown = %q %d:%d", chownedPath, chownedUID, chownedGID)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "ok" {
		t.Fatalf("written file = %q/%v", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v", err)
	}
}

func TestFromEnvironment(t *testing.T) {
	t.Setenv(OwnerUIDEnv, "1000")
	t.Setenv(OwnerGIDEnv, "1001")
	owner := FromEnvironment()
	if owner == nil || owner.UID != 1000 || owner.GID != 1001 {
		t.Fatalf("owner = %#v", owner)
	}
	t.Setenv(OwnerGIDEnv, "invalid")
	if FromEnvironment() != nil {
		t.Fatal("invalid owner environment accepted")
	}
}

func TestWriteAtomicWriterFailureLeavesDestinationAndRemovesTemporary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteAtomicWithChown(path, 0o600, nil, func(io.Writer) error {
		return os.ErrInvalid
	}, func(string, int, int) error { return nil })
	if !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("writer error = %v", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "old" {
		t.Fatalf("destination after writer failure = %q/%v", got, readErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "shared.json" {
		t.Fatalf("temporary files remain: %v", entries)
	}
}

func TestWriteAtomicChownFailureLeavesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteAtomicWithChown(path, 0o600, &Ownership{UID: 1000, GID: 1000}, func(writer io.Writer) error {
		_, writeErr := writer.Write([]byte("new"))
		return writeErr
	}, func(string, int, int) error { return os.ErrPermission })
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("chown error = %v", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "old" {
		t.Fatalf("destination after chown failure = %q/%v", got, readErr)
	}
}

func TestWriteAtomicNilOwnerOverwritesWithMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.json")
	chownCalls := 0
	err := WriteAtomicWithChown(path, 0o600, nil, func(writer io.Writer) error {
		_, writeErr := writer.Write([]byte("new"))
		return writeErr
	}, func(string, int, int) error {
		chownCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if chownCalls != 0 {
		t.Fatalf("nil owner caused %d chown calls", chownCalls)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new" {
		t.Fatalf("destination = %q/%v", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", err)
	}
}
