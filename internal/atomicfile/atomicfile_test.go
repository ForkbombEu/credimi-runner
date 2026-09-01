package atomicfile

import (
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
