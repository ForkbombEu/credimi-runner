package lifecyclelog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailAndExportMarkdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.jsonl")
	logger, err := New(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Emit(Event{Event: "operation.started", Message: "start", Fields: map[string]any{"api_key": "secret"}}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Emit(Event{Event: "operation.failed", Message: "failed", Error: "device offline"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := Tail(path, 1)
	if err != nil || len(events) != 1 || events[0].Event != "operation.failed" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	report, err := ExportMarkdown(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "operation.failed") || strings.Contains(report, "secret") {
		t.Fatalf("report=%q", report)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestTailAndExportHandleMissingOrEmptyLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.jsonl")
	if _, err := Tail(path, 10); err == nil {
		t.Fatal("expected missing lifecycle log error")
	}
	if _, err := ExportMarkdown(path, 10); err == nil {
		t.Fatal("expected missing lifecycle report error")
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if events, err := Tail(path, 0); err != nil || len(events) != 0 {
		t.Fatalf("empty tail events=%#v err=%v", events, err)
	}
	if report, err := ExportMarkdown(path, 10); err != nil || !strings.Contains(report, "No lifecycle events") {
		t.Fatalf("empty report=%q err=%v", report, err)
	}
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Tail(path, 10); err == nil {
		t.Fatal("expected malformed event error")
	}
}
