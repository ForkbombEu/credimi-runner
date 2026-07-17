package lifecyclelog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Tail returns at most lines complete JSONL records from a lifecycle log. The
// active log is intentionally bounded by Logger rotation, so reading it does
// not turn diagnostics into a runner-log ingestion path.
func Tail(path string, lines int) ([]Event, error) {
	if lines <= 0 {
		lines = 100
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	records := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	if len(records) > lines {
		records = records[len(records)-lines:]
	}
	events := make([]Event, 0, len(records))
	for _, record := range records {
		if len(bytes.TrimSpace(record)) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(record, &event); err != nil {
			return nil, fmt.Errorf("decode lifecycle record: %w", err)
		}
		events = append(events, event)
	}
	return events, nil
}

// ExportMarkdown creates a bounded, secret-free incident timeline. Values
// were sanitized before being written by Logger, so this function never opens
// configuration files or runner/Docker logs.
func ExportMarkdown(path string, lines int) (string, error) {
	events, err := Tail(path, lines)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	builder.WriteString("# Credimi Runner lifecycle diagnostic\n\n")
	builder.WriteString("This report is generated from the bounded lifecycle log. Secrets and URL query parameters are redacted.\n\n")
	if len(events) == 0 {
		builder.WriteString("No lifecycle events are available.\n")
		return builder.String(), nil
	}
	builder.WriteString("## Timeline\n\n")
	for _, event := range events {
		fmt.Fprintf(&builder, "- `%s` **%s** — %s", event.Timestamp.UTC().Format("2006-01-02T15:04:05Z"), event.Event, event.Message)
		if event.Phase != "" {
			fmt.Fprintf(&builder, " (phase: `%s`)", event.Phase)
		}
		if event.Error != "" {
			fmt.Fprintf(&builder, " — error: `%s`", event.Error)
		}
		builder.WriteByte('\n')
	}
	return builder.String(), nil
}
