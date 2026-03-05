package workermanager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/telemetry"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/forkbombeu/credimi/pkg/workflowengine"
	"github.com/forkbombeu/credimi/pkg/workflowengine/activities"
	"github.com/forkbombeu/credimi/pkg/workflowengine/workflows"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

var clientCache sync.Map

type ActivityTracer struct {
	tracer     trace.Tracer
	workerSpan trace.Span
}

func NewActivityTracer(tracer trace.Tracer, workerSpan trace.Span) *ActivityTracer {
	return &ActivityTracer{tracer: tracer, workerSpan: workerSpan}
}

type temporalWorker interface {
	RegisterWorkflowWithOptions(w interface{}, options workflow.RegisterOptions)
	RegisterActivityWithOptions(a interface{}, options activity.RegisterOptions)
	Run(interruptCh <-chan interface{}) error
}

var (
	temporalClientGetter  = getTemporalClientWithNamespace
	temporalWorkerFactory = func(c client.Client, taskqueue string, options worker.Options) temporalWorker {
		return worker.New(c, taskqueue, options)
	}
	sleepWithContextFn = sleepWithContext
)

// RunTemporalWorker returns a function suitable for Process.RunFunc
func RunTemporalWorker(namespace string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		tracer := telemetry.GetTracer()
		ctx, span := tracer.Start(ctx, "worker.lifecycle")
		defer span.End()

		runnerID := utils.GetEnvironmentVariable("CREDIMI_RUNNER_ID", "", true)
		taskqueue := fmt.Sprintf("%s-%s", runnerID, "TaskQueue")
		backoff := time.Second
		const maxBackoff = 30 * time.Second

		telemetry.TrackWorkerStart(ctx, namespace, runnerID)
		defer telemetry.TrackWorkerStop(ctx, namespace, runnerID)

		span.SetAttributes(
			attribute.String("worker.namespace", namespace),
			attribute.String("worker.runner_id", runnerID),
			attribute.String("worker.taskqueue", taskqueue),
			attribute.String("worker.start_time", time.Now().Format(time.RFC3339)),
		)
		activitiesList := []workflowengine.ExecutableActivity{
			activities.NewHTTPActivity(),
			activities.NewRunMobileFlowActivity(),
			activities.NewStartEmulatorActivity(),
			activities.NewApkInstallActivity(),
			activities.NewStartRecordingActivity(),
			activities.NewStopRecordingActivity(),
			// activities.NewUnlockEmulatorActivity(),
			activities.NewCleanupDeviceActivity(),
		}

		workflowsList := []workflowengine.Workflow{workflows.NewMobileAutomationWorkflow()}
		span.SetAttributes(
			attribute.Int("worker.workflows_registered", len(workflowsList)),
			attribute.Int("worker.activities_registered", len(activitiesList)),
		)

		attempt := 0

		for {
			if ctx.Err() != nil {
				span.AddEvent("worker.stopped", trace.WithAttributes(attribute.String("reason", "context_done")))
				span.SetAttributes(attribute.String("worker.end_time", time.Now().Format(time.RFC3339)))
				log.Printf("Temporal worker stopped for namespace %s", namespace)
				return nil
			}

			attempt++
			span.AddEvent("worker.connecting", trace.WithAttributes(attribute.Int("worker.attempt", attempt)))

			c, err := temporalClientGetter(namespace)
			if err != nil {
				retryable := shouldRetryTemporalWorker(err)
				span.RecordError(err)
				span.AddEvent(
					"worker.connection_failed",
					trace.WithAttributes(
						attribute.Int("worker.attempt", attempt),
						attribute.Bool("worker.retryable", retryable),
					),
				)

				if !retryable {
					span.SetStatus(codes.Error, err.Error())
					return err
				}
				log.Printf("Temporal worker failed to initialize for namespace %s: %v (retrying in %s)", namespace, err, backoff)
				if !sleepWithContextFn(ctx, backoff) {
					span.AddEvent("worker.stopped", trace.WithAttributes(attribute.String("reason", "context_done_while_waiting_retry")))
					span.SetAttributes(attribute.String("worker.end_time", time.Now().Format(time.RFC3339)))
					return nil
				}
				backoff = growBackoff(backoff, maxBackoff)
				continue
			}

			w := temporalWorkerFactory(c, taskqueue, worker.Options{})

			// Register workflows
			for _, wf := range workflowsList {
				w.RegisterWorkflowWithOptions(wf.Workflow, workflow.RegisterOptions{Name: wf.Name()})
			}

			// Register activities
			activityTracer := NewActivityTracer(tracer, span)
			for _, act := range activitiesList {
				w.RegisterActivityWithOptions(activityTracer.Wrap(act), activity.RegisterOptions{Name: act.Name()})
			}

			// Shutdown channel
			shutdownCh := make(chan interface{})
			shutdownWatcherDone := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					span.AddEvent("worker.shutdown_received")
					close(shutdownCh)
				case <-shutdownWatcherDone:
				}
			}()

			log.Printf("Temporal worker running for namespace %s", namespace)
			if err := w.Run(shutdownCh); err != nil {
				close(shutdownWatcherDone)
				if ctx.Err() != nil {
					span.AddEvent("worker.stopped", trace.WithAttributes(attribute.String("reason", "context_done")))
					span.SetAttributes(attribute.String("worker.end_time", time.Now().Format(time.RFC3339)))
					log.Printf("Temporal worker stopped for namespace %s", namespace)
					return nil
				}
				retryable := shouldRetryTemporalWorker(err)
				span.RecordError(err)
				span.AddEvent(
					"worker.run_failed",
					trace.WithAttributes(
						attribute.Int("worker.attempt", attempt),
						attribute.Bool("worker.retryable", retryable),
					),
				)
				if !retryable {
					span.SetStatus(codes.Error, err.Error())
					log.Printf("Temporal worker stopped with non-retryable error for namespace %s: %v", namespace, err)
					return err
				}
				log.Printf("Temporal worker stopped with retryable error for namespace %s: %v (retrying in %s)", namespace, err, backoff)
				if !sleepWithContextFn(ctx, backoff) {
					span.AddEvent("worker.stopped", trace.WithAttributes(attribute.String("reason", "context_done_while_waiting_retry")))
					span.SetAttributes(attribute.String("worker.end_time", time.Now().Format(time.RFC3339)))
					return nil
				}
				backoff = growBackoff(backoff, maxBackoff)
				continue
			}
			close(shutdownWatcherDone)

			span.SetStatus(codes.Ok, "Worker exited without error")
			span.AddEvent("worker.stopped", trace.WithAttributes(attribute.String("reason", "run_completed")))
			span.SetAttributes(attribute.String("worker.end_time", time.Now().Format(time.RFC3339)))
			log.Printf("Temporal worker stopped for namespace %s", namespace)
			return nil
		}
	}
}

func shouldRetryTemporalWorker(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Do not retry for permanent setup/configuration failures.
	var namespaceNotFound *serviceerror.NamespaceNotFound
	var invalidArgument *serviceerror.InvalidArgument
	var permissionDenied *serviceerror.PermissionDenied
	var unimplemented *serviceerror.Unimplemented
	if errors.As(err, &namespaceNotFound) ||
		errors.As(err, &invalidArgument) ||
		errors.As(err, &permissionDenied) ||
		errors.As(err, &unimplemented) {
		return false
	}

	// Retry by default to survive transient transport/server failures.
	return true
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func growBackoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}

func getTemporalClientWithNamespace(namespace string) (client.Client, error) {
	if c, ok := clientCache.Load(namespace); ok {
		return c.(client.Client), nil
	}
	hostPort := utils.GetEnvironmentVariable("TEMPORAL_ADDRESS", client.DefaultHostPort)
	c, err := client.NewLazyClient(client.Options{
		HostPort:  hostPort,
		Namespace: namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create client: %w", err)
	}

	clientCache.Store(namespace, c)
	return c, nil
}

func (at *ActivityTracer) Wrap(act workflowengine.ExecutableActivity) func(
	ctx context.Context,
	input workflowengine.ActivityInput,
) (workflowengine.ActivityResult, error) {
	return func(ctx context.Context, input workflowengine.ActivityInput) (workflowengine.ActivityResult, error) {
		startTime := time.Now()
		activityName := act.Name()

		at.workerSpan.AddEvent(fmt.Sprintf("activity.%s.started", activityName))

		ctx, activitySpan := at.tracer.Start(ctx, fmt.Sprintf("Activity: %s", activityName))
		defer activitySpan.End()

		activitySpan.SetAttributes(
			attribute.String("activity.name", act.Name()),
			attribute.String("activity.type", fmt.Sprintf("%T", act)),
		)

		result, err := act.Execute(ctx, input)
		duration := time.Since(startTime)
		activitySpan.SetAttributes(
			attribute.Int64("activity.duration_ms", duration.Milliseconds()),
		)
		if err != nil {
			activitySpan.SetStatus(codes.Error, err.Error())
			activitySpan.RecordError(err)
			at.workerSpan.AddEvent(fmt.Sprintf("activity.%s.failed", activityName))
		} else {
			activitySpan.SetStatus(codes.Ok, "Activity completed successfully")
			at.workerSpan.AddEvent(fmt.Sprintf("activity.%s.completed", activityName))
		}
		telemetry.TrackActivity(ctx, activityName, duration, err)
		return result, err
	}
}
