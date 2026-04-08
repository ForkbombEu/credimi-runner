package observability

import (
	"context"
	"sync"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	instrumentsOnce sync.Once
	instrumentsErr  error

	runnerStartsTotal   metric.Int64Counter
	workerStartsTotal   metric.Int64Counter
	workerStartFailures metric.Int64Counter
)

func RecordRunnerStart(ctx context.Context, attrs ...attribute.KeyValue) {
	if err := initInstruments(); err != nil {
		return
	}
	runnerStartsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

func RecordWorkerStart(ctx context.Context, attrs ...attribute.KeyValue) {
	if err := initInstruments(); err != nil {
		return
	}
	workerStartsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

func RecordWorkerStartFailure(ctx context.Context, attrs ...attribute.KeyValue) {
	if err := initInstruments(); err != nil {
		return
	}
	workerStartFailures.Add(ctx, 1, metric.WithAttributes(attrs...))
}

func initInstruments() error {
	instrumentsOnce.Do(func() {
		meter := otelapi.Meter("credimi-runner")

		runnerStartsTotal, instrumentsErr = meter.Int64Counter(
			"credimi_runner_starts_total",
			metric.WithDescription("Total number of credimi-runner process starts."),
		)
		if instrumentsErr != nil {
			return
		}

		workerStartsTotal, instrumentsErr = meter.Int64Counter(
			"credimi_runner_worker_starts_total",
			metric.WithDescription("Total number of worker start attempts."),
		)
		if instrumentsErr != nil {
			return
		}

		workerStartFailures, instrumentsErr = meter.Int64Counter(
			"credimi_runner_worker_start_failures_total",
			metric.WithDescription("Total number of worker start failures."),
		)
	})
	return instrumentsErr
}
