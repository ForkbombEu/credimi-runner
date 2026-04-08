package observability

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func TestMetricsInitializationAndRecording(t *testing.T) {
	resetInstrumentsForTest()

	if err := initInstruments(); err != nil {
		t.Fatalf("initInstruments returned error: %v", err)
	}
	if runnerStartsTotal == nil || workerStartsTotal == nil || workerStartFailures == nil {
		t.Fatalf("expected metrics instruments to be initialized")
	}

	ctx := context.Background()
	attrs := []attribute.KeyValue{attribute.String("runner_id", "runner-1")}
	RecordRunnerStart(ctx, attrs...)
	RecordWorkerStart(ctx, attrs...)
	RecordWorkerStartFailure(ctx, attrs...)
}

func resetInstrumentsForTest() {
	instrumentsOnce = sync.Once{}
	instrumentsErr = nil
	runnerStartsTotal = nil
	workerStartsTotal = nil
	workerStartFailures = nil
}
