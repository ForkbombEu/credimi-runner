package servicecoordination

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestRestartProtocolRoundTripAndValidation(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(200, 0)
	request, err := NewRestartRequest("fingerprint", now)
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
	result := RestartResult{RequestID: request.RequestID, Success: true, AppliedFingerprint: "fingerprint", UpdatedAt: now.Add(time.Second)}
	if err := WriteRestartResult(dir, result); err != nil {
		t.Fatal(err)
	}
	gotResult, err := ReadRestartResult(dir)
	if err != nil || gotResult != result {
		t.Fatalf("result=%+v err=%v", gotResult, err)
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
		t.Fatal("request without fingerprint accepted")
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
