package lifecyclelog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoggerWritesStructuredRedactedEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.jsonl")
	logger, err := New(path, Options{Now: func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Emit(Event{
		Level: "error", Event: "registration.failed", Message: "registration failed",
		Fields: map[string]any{
			"api_key":    "do-not-write",
			"public_url": "https://runner.example/path?token=do-not-write",
			"attempt":    2,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "do-not-write") {
		t.Fatalf("secret leaked into lifecycle log: %s", raw)
	}
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("invalid JSONL event: %v", err)
	}
	if event.Schema != SchemaVersion || event.Timestamp.IsZero() || event.Fields["api_key"] != "<redacted>" {
		t.Fatalf("event = %#v", event)
	}
	if event.Fields["public_url"] != "https://runner.example/path" {
		t.Fatalf("sanitized URL = %#v", event.Fields["public_url"])
	}
}

func TestLoggerRotatesBoundedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lifecycle.jsonl")
	logger, err := New(path, Options{MaxBytes: 220, Backups: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if err := logger.Emit(Event{Level: "info", Event: "operation.phase_changed", Message: strings.Repeat("x", 35)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".1", ".2"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatalf("missing rotated file %s: %v", suffix, err)
		}
		if info.Size() > 220 {
			t.Fatalf("file %s exceeds limit: %d", suffix, info.Size())
		}
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatal("active lifecycle log is empty")
	}
	var event Event
	if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
		t.Fatalf("active log is not JSONL: %v", err)
	}
}

func TestLoggerRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lifecycle.jsonl")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, Options{}); err == nil {
		t.Fatal("expected symlink rejection")
	}
}
