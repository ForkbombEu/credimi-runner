package observability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
)

func Debug(ctx context.Context, loggerName, message string, attrs ...otellog.KeyValue) {
	emit(ctx, loggerName, otellog.SeverityDebug, "debug", message, attrs...)
}

func Info(ctx context.Context, loggerName, message string, attrs ...otellog.KeyValue) {
	emit(ctx, loggerName, otellog.SeverityInfo, "info", message, attrs...)
}

func Warn(ctx context.Context, loggerName, message string, attrs ...otellog.KeyValue) {
	emit(ctx, loggerName, otellog.SeverityWarn, "warn", message, attrs...)
}

func Error(ctx context.Context, loggerName, message string, err error, attrs ...otellog.KeyValue) {
	if err != nil {
		attrs = append(attrs, otellog.String("error", err.Error()))
	}
	emit(ctx, loggerName, otellog.SeverityError, "error", message, attrs...)
}

func emit(ctx context.Context, loggerName string, severity otellog.Severity, severityText, message string, attrs ...otellog.KeyValue) {
	if ctx == nil {
		ctx = context.Background()
	}
	attrs = normalizeTelemetryAttrs(attrs)
	var record otellog.Record
	now := time.Now().UTC()
	record.SetTimestamp(now)
	record.SetObservedTimestamp(now)
	record.SetSeverity(severity)
	record.SetSeverityText(severityText)
	record.SetBody(otellog.StringValue(formatLogMessage(message, attrs)))
	if len(attrs) > 0 {
		record.AddAttributes(attrs...)
	}
	logglobal.Logger(loggerName).Emit(ctx, record)
}

func formatLogMessage(message string, attrs []otellog.KeyValue) string {
	if len(attrs) == 0 || strings.Contains(message, "\n") {
		return message
	}

	var b strings.Builder
	b.WriteString(message)

	keys := make([]string, 0, len(attrs))
	values := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		key := strings.TrimSpace(string(attr.Key))
		if key == "" {
			continue
		}
		keys = append(keys, key)
		values[key] = attr.Value.String()
	}
	sort.Strings(keys)

	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		b.WriteString("\n")
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(values[key])
	}

	return b.String()
}

func Attrs(fields map[string]string) []otellog.KeyValue {
	if len(fields) == 0 {
		return nil
	}
	attrs := make([]otellog.KeyValue, 0, len(fields))
	for key, value := range fields {
		attrs = append(attrs, otellog.String(key, value))
	}
	return attrs
}

func String(key, value string) otellog.KeyValue {
	return otellog.String(key, value)
}

func Int64(key string, value int64) otellog.KeyValue {
	return otellog.Int64(key, value)
}

func Bool(key string, value bool) otellog.KeyValue {
	return otellog.Bool(key, value)
}

func Messagef(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

func normalizeTelemetryAttrs(attrs []otellog.KeyValue) []otellog.KeyValue {
	values := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		key := strings.TrimSpace(string(attr.Key))
		if key == "" {
			continue
		}
		values[key] = attr.Value.String()
	}

	workflowID := values["workflow_id"]
	runID := values["run_id"]
	rootWorkflowID := values["root_workflow_id"]
	rootRunID := values["root_run_id"]
	parentWorkflowID := values["parent_workflow_id"]
	parentRunID := values["parent_run_id"]

	if rootWorkflowID == "" {
		rootWorkflowID = workflowID
	}
	if rootRunID == "" {
		rootRunID = runID
	}
	if parentWorkflowID == "" {
		parentWorkflowID = workflowID
	}
	if parentRunID == "" {
		parentRunID = runID
	}
	if rootWorkflowID == "" {
		rootWorkflowID = "-"
	}
	if rootRunID == "" {
		rootRunID = "-"
	}
	if parentWorkflowID == "" {
		parentWorkflowID = rootWorkflowID
	}
	if parentRunID == "" {
		parentRunID = rootRunID
	}

	return appendMissingAttrs(attrs,
		otellog.String("root_workflow_id", rootWorkflowID),
		otellog.String("root_run_id", rootRunID),
		otellog.String("parent_workflow_id", parentWorkflowID),
		otellog.String("parent_run_id", parentRunID),
	)
}

func appendMissingAttrs(attrs []otellog.KeyValue, extras ...otellog.KeyValue) []otellog.KeyValue {
	seen := make(map[string]struct{}, len(attrs))
	for _, attr := range attrs {
		key := strings.TrimSpace(string(attr.Key))
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
	}
	for _, extra := range extras {
		key := strings.TrimSpace(string(extra.Key))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		attrs = append(attrs, extra)
	}
	return attrs
}
