package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type runtimeControlServerFake struct {
	mu     sync.Mutex
	starts int
}

func (f *runtimeControlServerFake) StartExistingWorkers(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return nil
}

func (f *runtimeControlServerFake) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

type runtimeControlLifecycleFake struct {
	mu      sync.Mutex
	pauses  []string
	resumes []string
}

func (f *runtimeControlLifecycleFake) Pause(_ context.Context, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses = append(f.pauses, reason)
	return nil
}

func (f *runtimeControlLifecycleFake) Resume(_ context.Context, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, reason)
	return nil
}

func (f *runtimeControlLifecycleFake) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pauses), len(f.resumes)
}

type runtimeControlStoreFake struct {
	mu    sync.Mutex
	stops int
}

func (f *runtimeControlStoreFake) StopAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
}

func (f *runtimeControlStoreFake) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops
}

func TestRuntimeControlLoopKeepsDashboardAliveWhileTogglingWorkers(t *testing.T) {
	dir := t.TempDir()
	server := &runtimeControlServerFake{}
	lifecycle := &runtimeControlLifecycleFake{}
	store := &runtimeControlStoreFake{}
	ctx, cancel := context.WithCancel(context.Background())
	stop := startRuntimeControlLoop(ctx, dir, server, lifecycle, store)
	t.Cleanup(func() {
		cancel()
		stop()
	})

	if err := writeRuntimeCommand(dir, "stop"); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeControl(t, filepath.Join(dir, "runtime-paused"), true)
	pauses, _ := lifecycle.counts()
	if store.stopCount() != 1 || pauses != 1 {
		t.Fatalf("stop state = %#v %#v", store, lifecycle)
	}
	if err := writeRuntimeCommand(dir, "start"); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeControl(t, filepath.Join(dir, "runtime-paused"), false)
	_, resumes := lifecycle.counts()
	if server.startCount() != 1 || resumes != 1 {
		t.Fatalf("start state = %#v %#v", server, lifecycle)
	}
	if err := writeRuntimeCommand(dir, "restart"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && (store.stopCount() < 2 || server.startCount() < 2) {
		time.Sleep(10 * time.Millisecond)
	}
	if store.stopCount() != 2 || server.startCount() != 2 {
		t.Fatalf("restart state = %#v %#v", store, server)
	}
	if _, err := os.Stat(filepath.Join(dir, "runtime-control")); !os.IsNotExist(err) {
		t.Fatalf("runtime control request was not consumed: %v", err)
	}
}

func TestWriteRuntimeCommandValidatesActionAndKeepsRequestPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := writeRuntimeCommand(dir, "suspend"); err == nil || !strings.Contains(err.Error(), "unsupported runtime action") {
		t.Fatalf("unsupported action error = %v", err)
	}
	if err := writeRuntimeCommand(dir, "restart"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runtime-control")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime control mode = %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "restart\n" {
		t.Fatalf("runtime control content=%q err=%v", raw, err)
	}
}

func TestRequestRuntimeCommandAndWaitConfirmsStateTransition(t *testing.T) {
	dir := t.TempDir()
	if err := writeRuntimeState(dir, "running"); err != nil {
		t.Fatal(err)
	}
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "runtime-control")); err == nil {
				_ = writeRuntimeState(dir, "paused")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if err := requestRuntimeCommandAndWait(context.Background(), dir, "stop", "paused"); err != nil {
		t.Fatal(err)
	}
}

func TestRequestRuntimeCommandAndWaitReportsRuntimeFailure(t *testing.T) {
	dir := t.TempDir()
	if err := writeRuntimeState(dir, "running"); err != nil {
		t.Fatal(err)
	}
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "runtime-control")); err == nil {
				_ = writeRuntimeState(dir, "failed: workers did not start")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if err := requestRuntimeCommandAndWait(context.Background(), dir, "start", "running"); err == nil || !strings.Contains(err.Error(), "workers did not start") {
		t.Fatalf("runtime failure = %v", err)
	}
}

func TestWriteRuntimeStatePersistsSequenceAndPrivateMode(t *testing.T) {
	dir := t.TempDir()
	if err := writeRuntimeState(dir, "running"); err != nil {
		t.Fatal(err)
	}
	if err := writeRuntimeState(dir, "paused"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runtime-state-sequence"))
	if err != nil || strings.TrimSpace(string(raw)) != "2" {
		t.Fatalf("runtime state sequence = %q, err=%v", raw, err)
	}
	state, err := os.ReadFile(filepath.Join(dir, "runtime-state"))
	if err != nil || string(state) != "paused\n" {
		t.Fatalf("runtime state = %q, err=%v", state, err)
	}
	for _, name := range []string{"runtime-state", "runtime-state-sequence"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestRuntimeStateAndCommandRejectUnavailableStorage(t *testing.T) {
	if err := writeRuntimeState("", "running"); err != nil {
		t.Fatalf("empty runtime state directory = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := writeRuntimeState(missing, "running"); err == nil {
		t.Fatal("runtime state unexpectedly succeeded for missing directory")
	}
	if err := writeRuntimeCommand(missing, "start"); err == nil {
		t.Fatal("runtime command unexpectedly succeeded for missing directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := requestRuntimeCommandAndWait(ctx, missing, "start", "running"); err == nil {
		t.Fatal("runtime command unexpectedly succeeded for unavailable storage")
	}
}

func waitForRuntimeControl(t *testing.T, path string, wantPresent bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, err := os.Stat(path)
		present := err == nil
		if present == wantPresent {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runtime control marker %q present=%v, want %v", path, !wantPresent, wantPresent)
}
