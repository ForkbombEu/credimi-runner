package observability

import (
	"context"
	"errors"
	"strings"
	"testing"

	otellog "go.opentelemetry.io/otel/log"
)

func TestFormatLogMessage(t *testing.T) {
	if got := formatLogMessage("plain", nil); got != "plain" {
		t.Fatalf("expected plain message without attrs, got %q", got)
	}

	msg := formatLogMessage("runner started", []otellog.KeyValue{
		otellog.String("runner_id", "runner-1"),
		otellog.String("namespace", "ns-1"),
		otellog.String("runner_id", "runner-1"),
		otellog.String("", "ignored"),
	})
	if !strings.Contains(msg, "runner started") {
		t.Fatalf("expected original message, got %q", msg)
	}
	if !strings.Contains(msg, "runner_id=runner-1") || !strings.Contains(msg, "namespace=ns-1") {
		t.Fatalf("expected formatted attributes, got %q", msg)
	}

	if got := formatLogMessage("already\nformatted", []otellog.KeyValue{otellog.String("x", "y")}); got != "already\nformatted" {
		t.Fatalf("expected multiline message unchanged, got %q", got)
	}
}

func TestAttrsHelpersAndEmitWrappers(t *testing.T) {
	attrs := Attrs(map[string]string{"a": "1", "b": "2"})
	if len(attrs) != 2 {
		t.Fatalf("expected 2 attrs, got %d", len(attrs))
	}
	if Attrs(nil) != nil {
		t.Fatalf("expected nil attrs for nil map")
	}

	if String("k", "v").Value.AsString() != "v" {
		t.Fatalf("unexpected string key value")
	}
	if Int64("n", 42).Value.AsInt64() != 42 {
		t.Fatalf("unexpected int64 key value")
	}
	if !Bool("b", true).Value.AsBool() {
		t.Fatalf("unexpected bool key value")
	}
	if Messagef("x=%d", 7) != "x=7" {
		t.Fatalf("unexpected Messagef output")
	}

	ctx := context.Background()
	Debug(ctx, "test.logger", "debug message", otellog.String("key", "value"))
	Info(ctx, "test.logger", "info message", otellog.String("key", "value"))
	Warn(ctx, "test.logger", "warn message", otellog.String("key", "value"))
	Error(ctx, "test.logger", "error message", errors.New("boom"), otellog.String("key", "value"))
	emit(nil, "test.logger", otellog.SeverityInfo, "info", "message", otellog.String("key", "value"))
}

func TestNormalizeTelemetryAttrs(t *testing.T) {
	t.Run("fills workflow defaults from current workflow attrs", func(t *testing.T) {
		attrs := normalizeTelemetryAttrs([]otellog.KeyValue{
			otellog.String("workflow_id", "wf-1"),
			otellog.String("run_id", "run-1"),
		})
		msg := formatLogMessage("x", attrs)
		for _, expected := range []string{
			"root_workflow_id=wf-1",
			"root_run_id=run-1",
			"parent_workflow_id=wf-1",
			"parent_run_id=run-1",
		} {
			if !strings.Contains(msg, expected) {
				t.Fatalf("expected %q in %q", expected, msg)
			}
		}
	})

	t.Run("fills placeholders for runner lifecycle logs", func(t *testing.T) {
		attrs := normalizeTelemetryAttrs([]otellog.KeyValue{
			otellog.String("runner_id", "runner-1"),
		})
		msg := formatLogMessage("x", attrs)
		for _, expected := range []string{
			"root_workflow_id=-",
			"root_run_id=-",
			"parent_workflow_id=-",
			"parent_run_id=-",
		} {
			if !strings.Contains(msg, expected) {
				t.Fatalf("expected %q in %q", expected, msg)
			}
		}
	})

	t.Run("preserves explicit root and parent attrs", func(t *testing.T) {
		attrs := normalizeTelemetryAttrs([]otellog.KeyValue{
			otellog.String("workflow_id", "child-wf"),
			otellog.String("run_id", "child-run"),
			otellog.String("root_workflow_id", "root-wf"),
			otellog.String("root_run_id", "root-run"),
			otellog.String("parent_workflow_id", "parent-wf"),
			otellog.String("parent_run_id", "parent-run"),
		})
		msg := formatLogMessage("x", attrs)
		for _, expected := range []string{
			"root_workflow_id=root-wf",
			"root_run_id=root-run",
			"parent_workflow_id=parent-wf",
			"parent_run_id=parent-run",
		} {
			if !strings.Contains(msg, expected) {
				t.Fatalf("expected %q in %q", expected, msg)
			}
		}
	})
}
