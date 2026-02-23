package telemetry

import (
	"context"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	prometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

var (
	meter metric.Meter

	activeWorkers    metric.Int64UpDownCounter
	activitiesTotal  metric.Int64Counter
	activitiesErrors metric.Int64Counter
	activityDuration metric.Float64Histogram
)

func InitMetrics() func() {
	exporter, err := prometheus.New()
	if err != nil {
		log.Fatalf("Failed to create prometheus exporter: %v", err)
	}

	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName("credimi-runner"),
			semconv.ServiceVersion("1.0.0"),
		),
	)
	if err != nil {
		log.Fatalf("Failed to create resource: %v", err)
	}
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
		sdkmetric.WithResource(res),
	)

	otel.SetMeterProvider(provider)
	meter = provider.Meter("github.com/forkbombeu/credimi-runner")

	initInstruments()

	return func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			log.Printf("Error shutting down meter provider: %v", err)
		}
	}
}

func initInstruments() {
	var err error
	//Synchronous instrument that supports increments and decrements
	activeWorkers, err = meter.Int64UpDownCounter(
		"worker.active",
		metric.WithDescription("Number of active workers"),
	)
	if err != nil {
		log.Printf("Failed to create activeWorkers: %v", err)
	}
	//Synchronous instrument that supports non-negative increments
	activitiesTotal, err = meter.Int64Counter(
		"activities.total",
		metric.WithDescription("Total number of activities executed"),
	)
	if err != nil {
		log.Printf("Failed to create activitiesTotal: %v", err)
	}
	activitiesErrors, err = meter.Int64Counter(
		"activities.errors",
		metric.WithDescription("Total number of activity errors"),
	)
	if err != nil {
		log.Printf("Failed to create activitiesErrors: %v", err)
	}
	//Synchronous instrument that supports arbitrary values
	activityDuration, err = meter.Float64Histogram(
		"activity.duration",
		metric.WithDescription("Duration of activities in seconds"),
	)
	if err != nil {
		log.Printf("Failed to create activityDuration: %v", err)
	}
	testCounter, _ := meter.Int64Counter("test_forever")
	testCounter.Add(context.Background(), 1)
}

func TrackWorkerStart(ctx context.Context, namespace, runnerID string) {
	if activeWorkers != nil {
		activeWorkers.Add(ctx, 1, metric.WithAttributes(
			attribute.String("namespace", namespace),
			attribute.String("runnerID", runnerID),
			attribute.String("worker.status", "started"),
		))
	}
}

func TrackWorkerStop(ctx context.Context, namespace string, runnerID string) {
	if activeWorkers != nil {
		activeWorkers.Add(ctx, -1, metric.WithAttributes(
			attribute.String("namespace", namespace),
			attribute.String("runnerID", runnerID),
			attribute.String("worker.status", "stopped"),
		))
	}
}

func TrackActivity(ctx context.Context, name string, duration time.Duration, err error) {
	attrs := []attribute.KeyValue{
		attribute.String("activity.name", name),
	}
	if activitiesTotal != nil {
		activitiesTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
	if err != nil && activitiesErrors != nil {
		activitiesErrors.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
	if activityDuration != nil {
		activityDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
	}
}
