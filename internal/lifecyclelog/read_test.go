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
