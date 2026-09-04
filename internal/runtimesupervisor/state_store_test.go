package runtimesupervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/atomicfile"
)

func TestStateStoreDefaultsAndAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(atomicfile.OwnerUIDEnv, strconv.Itoa(os.Getuid()))
	t.Setenv(atomicfile.OwnerGIDEnv, strconv.Itoa(os.Getgid()))
	store := StateStore{Path: filepath.Join(dir, "nested", "runtime-state.json")}
	state, err := store.Load(false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Desired != DesiredStopped || state.Actual != ActualStopped {
		t.Fatalf("unexpected unconfigured state: %+v", state)
	}
	state, err = store.Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Desired != DesiredRunning || state.Actual != ActualStopped {
		t.Fatalf("unexpected configured default: %+v", state)
	}
	want := PersistentState{Desired: DesiredRunning, Actual: ActualFailed, LastError: "boom"}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Desired != want.Desired || got.Actual != want.Actual || got.LastError != want.LastError || got.UpdatedAt.IsZero() {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("state mode %o", info.Mode().Perm())
	}
}

func TestStateStoreRejectsMalformedState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-state.json")
	if err := os.WriteFile(path, []byte(`{"desired":"bad","actual":"running"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (StateStore{Path: path}).Load(true); err == nil {
		t.Fatal("expected invalid state error")
	}
}

func TestStateStoreRejectsInvalidActualAndEmptyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-state.json")
	if err := os.WriteFile(path, []byte(`{"desired":"running","actual":"broken"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (StateStore{Path: path}).Load(true); err == nil {
		t.Fatal("expected invalid actual state error")
	}
	if err := (StateStore{}).Save(PersistentState{}); err == nil {
		t.Fatal("expected empty path error")
	}
}
