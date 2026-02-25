package workermanager

import (
	"context"
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

// RunTemporalWorker returns a function suitable for Process.RunFunc
func RunTemporalWorker(namespace string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		tracer := telemetry.GetTracer()

		ctx, span := tracer.Start(ctx, "worker.lifecycle")
		defer span.End()

		span.AddEvent("Connecting to Temporal")
		c, err := getTemporalClientWithNamespace(namespace)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			return err
		}
		runnerID := utils.GetEnvironmentVariable("CREDIMI_RUNNER_ID", "", true)
		taskqueue := fmt.Sprintf("%s-%s", runnerID, "TaskQueue")
		telemetry.TrackWorkerStart(ctx, namespace, runnerID)
		defer telemetry.TrackWorkerStop(ctx, namespace, runnerID)
		span.SetAttributes(
			attribute.String("worker.namespace", namespace),
			attribute.String("worker.runner_id", runnerID),
			attribute.String("worker.taskqueue", taskqueue),
			attribute.String("worker.start_time", time.Now().Format(time.RFC3339)),
		)

		w := worker.New(c, taskqueue, worker.Options{})
		span.AddEvent("worker.registering_workflows")
		workflowCount := 0
		// Register workflows
		for _, wf := range []workflowengine.Workflow{workflows.NewMobileAutomationWorkflow()} {
			w.RegisterWorkflowWithOptions(wf.Workflow, workflow.RegisterOptions{Name: wf.Name()})
			workflowCount++
			span.SetAttributes(attribute.Int("worker.workflows_registered", workflowCount))
		}

		activityTracer := NewActivityTracer(tracer, span)
		// Register activities
		span.AddEvent("worker.registering_activities")
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

		// Register activities with tracing
		for _, act := range activitiesList {
			wrapped := activityTracer.Wrap(act)
			w.RegisterActivityWithOptions(
				wrapped,
				activity.RegisterOptions{Name: act.Name()},
			)
		}
		span.SetAttributes(attribute.Int("worker.activities_registered", len(activitiesList)))
		span.AddEvent("worker.registration_complete")

		// Shutdown channel
		shutdownCh := make(chan interface{})
		go func() {
			<-ctx.Done()
			span.AddEvent("worker.shutdown_received")
			close(shutdownCh)
		}()

		span.AddEvent("worker.starting")
		log.Printf("Temporal worker running for namespace %s", namespace)
		if err := w.Run(shutdownCh); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			log.Printf("Temporal worker stopped with error: %v", err)
			return err
		}

		span.AddEvent("worker.stopped")
		span.SetAttributes(
			attribute.String("worker.end_time", time.Now().Format(time.RFC3339)),
		)
		log.Printf("Temporal worker stopped for namespace %s", namespace)
		return nil
	}
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
