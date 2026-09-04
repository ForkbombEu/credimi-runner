package servicecoordination

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPresenceIsAtomicPrivateAndExpires(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(100, 0)
	if err := WritePresence(dir, now); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, CoordinatorFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	active, err := CoordinatorActive(dir, now.Add(14*time.Second))
	if err != nil || !active {
		t.Fatalf("active=%t err=%v", active, err)
	}
	active, err = CoordinatorActive(dir, now.Add(CoordinatorMaxAge+time.Second))
	if err != nil || active {
		t.Fatalf("expired active=%t err=%v", active, err)
	}
	if err := RemovePresence(dir); err != nil {
		t.Fatal(err)
	}
	active, err = CoordinatorActive(dir, now)
	if err != nil || active {
		t.Fatalf("removed active=%t err=%v", active, err)
	}
}

func TestStartPresenceCleansUpAfterCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cleanup, err := StartPresence(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	active, err := CoordinatorActive(dir, time.Now())
	if err != nil || !active {
		t.Fatalf("active=%t err=%v", active, err)
	}
	cancel()
	cleanup()
	if _, err := os.Stat(filepath.Join(dir, CoordinatorFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("coordinator file after cleanup: %v", err)
	}
}

func TestStartPresenceAcceptsNilContext(t *testing.T) {
	dir := t.TempDir()
	cleanup, err := StartPresence(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Join(dir, CoordinatorFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("coordinator file after cleanup: %v", err)
	}
}

func TestStartPresenceCreatesMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "runner")
	cleanup, err := StartPresence(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}

func TestStartPresenceReportsDirectoryCreationFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StartPresence(context.Background(), path); err == nil {
		t.Fatal("presence started below a regular file")
	}
}

func TestStartPresenceReleasesLockWhenPresenceWriteFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, CoordinatorFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := StartPresence(context.Background(), dir); err == nil {
		t.Fatal("presence started with an unusable state path")
	}
	if _, err := os.Stat(filepath.Join(dir, CoordinatorLockFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("coordinator lock after failed start: %v", err)
	}
}

func TestStartPresenceReportsUnreclaimableStaleLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, CoordinatorLockFile)
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockPath, "owner"), []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-CoordinatorMaxAge - time.Second)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := StartPresence(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "reclaim stale service coordinator lock") {
		t.Fatalf("unreclaimable lock error=%v", err)
	}
}

func TestCoordinatorOwnershipIsExclusive(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan struct {
		cleanup func()
		err     error
	}, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cleanup, err := StartPresence(ctx, dir)
			results <- struct {
				cleanup func()
				err     error
			}{cleanup, err}
		}()
	}
	wg.Wait()
	close(results)
	var acquired int
	for result := range results {
		if result.err == nil {
			acquired++
			result.cleanup()
		} else if !strings.Contains(result.err.Error(), "already coordinating") {
			t.Fatalf("unexpected ownership error: %v", result.err)
		}
	}
	if acquired != 1 {
		t.Fatalf("coordinators acquired=%d, want 1", acquired)
	}
}

func TestCoordinatorReclaimsStaleOwnership(t *testing.T) {
	dir := t.TempDir()
	stale := time.Now().Add(-CoordinatorMaxAge - time.Second)
	if err := os.WriteFile(filepath.Join(dir, CoordinatorLockFile), []byte("stale-owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, CoordinatorLockFile), stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := WritePresence(dir, stale); err != nil {
		t.Fatal(err)
	}
	cleanup, err := StartPresence(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
}

func TestCoordinatorRejectsFreshLockWithoutPresence(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, CoordinatorLockFile)
	if err := os.WriteFile(lockPath, []byte("active-owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StartPresence(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "already coordinating") {
		t.Fatalf("fresh lock error=%v", err)
	}
}

func TestCoordinatorCleanupPreservesReclaimedOwner(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cleanup, err := StartPresence(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !CoordinatorOwned(dir) {
		t.Fatal("new coordinator does not own its lease")
	}
	cancel()
	newNonce := "new-owner"
	if err := os.WriteFile(filepath.Join(dir, CoordinatorLockFile), []byte(newNonce), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, CoordinatorFile), Presence{PID: os.Getpid() + 1, Protocol: Protocol, Nonce: newNonce, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if CoordinatorOwned(dir) {
		t.Fatal("replaced coordinator still owns its lease")
	}
	cleanup()
	contents, err := os.ReadFile(filepath.Join(dir, CoordinatorLockFile))
	if err != nil || string(contents) != newNonce {
		t.Fatalf("reclaimed lock=%q err=%v", contents, err)
	}
	presence, err := ReadPresence(dir)
	if err != nil || presence.Nonce != newNonce {
		t.Fatalf("reclaimed presence=%+v err=%v", presence, err)
	}
}

func TestCoordinatorRefreshUpdatesPresenceOnlyWhileOwned(t *testing.T) {
	dir := t.TempDir()
	cleanup, err := StartPresence(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	presence, err := ReadPresence(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantTime := time.Now().Add(time.Second).UTC()
	if err := refreshCoordinator(dir, wantTime, presence.Nonce); err != nil {
		t.Fatal(err)
	}
	updated, err := ReadPresence(dir)
	if err != nil || !updated.UpdatedAt.Equal(wantTime) {
		t.Fatalf("updated presence=%+v err=%v", updated, err)
	}
	cleanup()
	if err := refreshCoordinator(dir, time.Now(), presence.Nonce); err == nil {
		t.Fatal("refresh after cleanup succeeded")
	}
}

func TestCoordinatorReleaseIsIdempotentWhenLockIsMissing(t *testing.T) {
	if err := releaseCoordinator(t.TempDir(), "owner"); err != nil {
		t.Fatalf("release missing lock: %v", err)
	}
}

func TestRestartProtocolRoundTripAndValidation(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(200, 0)
	request, err := NewRestartRequest("config-digest", true, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteRestartRequest(dir, request); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRestartRequest(dir)
	if err != nil || got != request {
		t.Fatalf("request=%+v err=%v", got, err)
	}
	if got.RequestedConfigDigest != "config-digest" || !got.ForceRestart {
		t.Fatalf("request schema=%+v", got)
	}
	result := RestartResult{RequestID: request.RequestID, Success: true, AppliedFingerprint: "fingerprint", UpdatedAt: now.Add(time.Second)}
	if err := WriteRestartResult(dir, result); err != nil {
		t.Fatal(err)
	}
	gotResult, err := ReadRestartResult(dir)
	if err != nil {
		t.Fatalf("result=%+v err=%v", gotResult, err)
	}
	if gotResult.RequestID != result.RequestID || gotResult.Success != result.Success ||
		gotResult.AppliedFingerprint != result.AppliedFingerprint || gotResult.Error != result.Error ||
		!gotResult.UpdatedAt.Equal(result.UpdatedAt) {
		t.Fatalf("result=%+v want=%+v", gotResult, result)
	}
	if err := WriteRestartRequest(dir, RestartRequest{}); err == nil {
		t.Fatal("incomplete request accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, RestartResultFile), []byte("null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRestartResult(dir); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid result error=%v", err)
	}
}

func TestProtocolRejectsCorruptAndIncompleteState(t *testing.T) {
	dir := t.TempDir()
	presencePath := filepath.Join(dir, CoordinatorFile)
	if err := os.WriteFile(presencePath, []byte("{\"pid\":0,\"protocol\":1,\"updated_at\":\"2026-01-01T00:00:00Z\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPresence(dir); err == nil {
		t.Fatal("invalid presence accepted")
	}
	if err := os.WriteFile(presencePath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CoordinatorActive(dir, time.Now()); err == nil {
		t.Fatal("malformed presence accepted")
	}
	if err := WriteRestartRequest(dir, RestartRequest{RequestID: "id", CreatedAt: time.Now()}); err == nil {
		t.Fatal("request without config digest accepted")
	}
	if err := WriteRestartResult(dir, RestartResult{Success: true, UpdatedAt: time.Now()}); err == nil {
		t.Fatal("result without request ID accepted")
	}
	if err := writeJSON(filepath.Join(dir, "unsupported.json"), make(chan int)); err == nil {
		t.Fatal("unsupported JSON value accepted")
	}
	if err := os.Remove(presencePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(presencePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(presencePath, "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemovePresence(dir); err == nil {
		t.Fatal("removing a directory as presence succeeded")
	}
}

func TestProtocolRejectsInvalidReadsAndMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadPresence(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing presence error=%v", err)
	}
	active, err := CoordinatorActive(dir, time.Now())
	if err != nil || active {
		t.Fatalf("missing coordinator active=%t err=%v", active, err)
	}
	if _, err := ReadRestartRequest(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing request error=%v", err)
	}
	if _, err := ReadRestartResult(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing result error=%v", err)
	}
	requestPath := filepath.Join(dir, RestartRequestFile)
	if err := os.WriteFile(requestPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRestartRequest(dir); err == nil {
		t.Fatal("incomplete request read successfully")
	}
	resultPath := filepath.Join(dir, RestartResultFile)
	if err := os.WriteFile(resultPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRestartResult(dir); err == nil {
		t.Fatal("incomplete result read successfully")
	}
}
