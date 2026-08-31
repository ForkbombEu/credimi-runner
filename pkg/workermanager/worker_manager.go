package workermanager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/observability"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/forkbombeu/credimi/pkg/workflowengine"
	"github.com/forkbombeu/credimi/pkg/workflowengine/activities"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

const defaultTemporalAddress = "temporal.credimi.io:7233"

type temporalWorker interface {
	RegisterActivityWithOptions(a interface{}, options activity.RegisterOptions)
	Start() error
	Stop()
}

var (
	temporalClientGetter  = getTemporalClientWithNamespace
	temporalWorkerFactory = func(c client.Client, taskqueue string, options worker.Options) temporalWorker {
		return worker.New(c, taskqueue, options)
	}
	sleepWithContextFn = sleepWithContext
)

type registeredActivity struct {
	Activity workflowengine.ExecutableActivity
	Mobile   bool
}

func registeredActivities() []registeredActivity {
	return []registeredActivity{
		{Activity: activities.NewHTTPActivity()},
		{Activity: activities.NewRunMobileFlowActivity(), Mobile: true},
		{Activity: activities.NewSetupMobileDeviceActivity(), Mobile: true},
		{Activity: activities.NewApkInstallActivity(), Mobile: true},
		{Activity: activities.NewApkPostInstallChecksActivity(), Mobile: true},
		{Activity: activities.NewStartIOSSimulatorActivity(), Mobile: true},
		{Activity: activities.NewInstallIOSAppActivity(), Mobile: true},
		{Activity: activities.NewIOSPostInstallChecksActivity(), Mobile: true},
		{Activity: activities.NewStartRecordingActivity(), Mobile: true},
		{Activity: activities.NewStartIOSRecordingActivity(), Mobile: true},
		{Activity: activities.NewStopRecordingActivity(), Mobile: true},
		{Activity: activities.NewStopIOSRecordingActivity(), Mobile: true},
		{Activity: activities.NewListInstalledAppsActivity(), Mobile: true},
		{Activity: activities.NewDisableAndroidPlayStoreActivity(), Mobile: true},
		{Activity: activities.NewCleanupDeviceActivity(), Mobile: true},
	}
}

func registeredActivityExecutor(item registeredActivity, provider RuntimeConfigProvider) func(context.Context, workflowengine.ActivityInput) (workflowengine.ActivityResult, error) {
	if item.Mobile {
		return mobileActivityExecutor(item.Activity, provider)
	}
	return item.Activity.Execute
}

func temporalWorkerTraceAttrs(namespace, taskqueue, runnerID string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("namespace", namespace),
		attribute.String("task_queue", taskqueue),
	}
	if runnerID != "" {
		attrs = append(attrs, attribute.String("runner_id", runnerID))
	}
	return attrs
}

// RunTemporalWorker returns a function suitable for Process.RunFunc
func RunTemporalWorker(namespace string) func(ctx context.Context) error {
	return runTemporalWorker(namespace, nil, nil)
}

// RunTemporalWorkerReady reports readiness from the actual worker after its
// client, registrations, and long-running run path have been prepared.
func RunTemporalWorkerReady(namespace string) func(ctx context.Context, ready func(error)) error {
	return func(ctx context.Context, ready func(error)) error {
		return runTemporalWorker(namespace, nil, ready)(ctx)
	}
}

// RunTemporalWorkerWithConfigProvider starts a namespace worker whose mobile
// activities resolve the latest typed device inventory at activity start.
func RunTemporalWorkerWithConfigProvider(namespace string, provider RuntimeConfigProvider) func(ctx context.Context) error {
	return runTemporalWorker(namespace, provider, nil)
}

func RunTemporalWorkerWithConfigProviderReady(namespace string, provider RuntimeConfigProvider) func(ctx context.Context, ready func(error)) error {
	return func(ctx context.Context, ready func(error)) error {
		return runTemporalWorker(namespace, provider, ready)(ctx)
	}
}

// VerifyTemporalWorker performs the connection step required before a worker
// can enter its long-running loop. The caller closes the short-lived client;
// the worker creates and owns its own client afterwards.
func VerifyTemporalWorker(namespace string) error {
	client, err := temporalClientGetter(namespace)
	if err != nil {
		return err
	}
	if client == nil {
		return errors.New("Temporal client is unavailable")
	}
	client.Close()
	return nil
}

func runTemporalWorker(namespace string, provider RuntimeConfigProvider, ready func(error)) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		var readyOnce sync.Once
		signalReady := func(err error) {
			if ready != nil {
				readyOnce.Do(func() { ready(err) })
			}
		}
		temporalInterceptor, err := observability.NewTemporalInterceptor()
		if err != nil {
			err = fmt.Errorf("unable to create temporal tracing interceptor: %w", err)
			signalReady(err)
			return err
		}
		runnerID := ""
		if provider != nil {
			config, err := provider()
			if err != nil {
				err = fmt.Errorf("load runner device inventory: %w", err)
				signalReady(err)
				return err
			}
			runnerID = strings.TrimLeft(strings.TrimSpace(config.RunnerID), "/")
		} else {
			runnerID = strings.TrimLeft(strings.TrimSpace(utils.GetEnvironmentVariable("CREDIMI_RUNNER_ID", "")), "/")
		}
		if runnerID == "" {
			err := errors.New("runner ID is required")
			signalReady(err)
			return err
		}
		taskqueue := fmt.Sprintf("%s-%s", runnerID, "TaskQueue")
		ctx, span := observability.Tracer("credimi-runner.temporal").Start(ctx, "temporal_worker.run", trace.WithAttributes(temporalWorkerTraceAttrs(namespace, taskqueue, runnerID)...))
		defer span.End()
		backoff := time.Second
		const maxBackoff = 30 * time.Second

		for {
			if ctx.Err() != nil {
				signalReady(ctx.Err())
				span.AddEvent("temporal_worker.stopped", trace.WithAttributes(attribute.String("reason", "context_canceled")))
				log.Printf("Temporal worker stopped for namespace %s", namespace)
				return nil
			}

			c, err := temporalClientGetter(namespace)
			if err != nil {
				span.AddEvent("temporal_worker.init_failed", trace.WithAttributes(attribute.String("backoff", backoff.String()), attribute.String("error", err.Error())))
				if !shouldRetryTemporalWorker(err) {
					signalReady(err)
					span.RecordError(err)
					span.SetStatus(codes.Error, "temporal worker initialization failed")
					return err
				}
				observability.Warn(ctx, "credimi-runner.temporal", "temporal worker initialization failed",
					observability.String("namespace", namespace),
					observability.String("task_queue", taskqueue),
					observability.String("backoff", backoff.String()),
					observability.String("runner_id", runnerID),
					observability.String("error", err.Error()),
				)
				span.AddEvent("temporal_worker.retry_scheduled", trace.WithAttributes(attribute.String("reason", "init_failed"), attribute.String("backoff", backoff.String())))
				log.Printf("Temporal worker failed to initialize for namespace %s: %v (retrying in %s)", namespace, err, backoff)
				if !sleepWithContextFn(ctx, backoff) {
					span.AddEvent("temporal_worker.stopped", trace.WithAttributes(attribute.String("reason", "retry_canceled")))
					signalReady(ctx.Err())
					return nil
				}
				backoff = growBackoff(backoff, maxBackoff)
				continue
			}

			fatal := make(chan error, 1)
			w := temporalWorkerFactory(c, taskqueue, worker.Options{
				Interceptors: []interceptor.WorkerInterceptor{temporalInterceptor},
				OnFatalError: func(err error) {
					select {
					case fatal <- err:
					default:
					}
				},
			})

			// Register activities
			for _, item := range registeredActivities() {
				w.RegisterActivityWithOptions(registeredActivityExecutor(item, provider), activity.RegisterOptions{Name: item.Activity.Name()})
			}
			wStartErr := w.Start()
			if wStartErr != nil {
				if c != nil {
					c.Close()
				}
				if !shouldRetryTemporalWorker(wStartErr) {
					signalReady(wStartErr)
					return wStartErr
				}
				if !sleepWithContextFn(ctx, backoff) {
					signalReady(ctx.Err())
					return nil
				}
				backoff = growBackoff(backoff, maxBackoff)
				continue
			}
			signalReady(nil)
			var stopOnce sync.Once
			stopWorker := func() { stopOnce.Do(w.Stop) }
			// Temporal reports fatal post-start failures through OnFatalError.
			// They are distinct from Start errors and retain the existing retry
			// behavior without a second Run/shutdown channel.
			log.Printf("Temporal worker running for namespace %s", namespace)
			span.AddEvent("temporal_worker.started")
			observability.Info(ctx, "credimi-runner.temporal", "temporal worker running",
				observability.String("namespace", namespace),
				observability.String("task_queue", taskqueue),
				observability.String("runner_id", runnerID),
			)
			select {
			case <-ctx.Done():
				err = nil
			case err = <-fatal:
			}
			stopWorker()
			if c != nil {
				c.Close()
			}
			if err != nil {
				if ctx.Err() != nil {
					span.AddEvent("temporal_worker.stopped", trace.WithAttributes(attribute.String("reason", "context_canceled")))
					log.Printf("Temporal worker stopped for namespace %s", namespace)
					return nil
				}
				if !shouldRetryTemporalWorker(err) {
					span.AddEvent("temporal_worker.stopped", trace.WithAttributes(attribute.String("reason", "non_retryable_error"), attribute.String("error", err.Error())))
					span.RecordError(err)
					span.SetStatus(codes.Error, "temporal worker stopped with non-retryable error")
					observability.Error(ctx, "credimi-runner.temporal", "temporal worker stopped with non-retryable error", err,
						observability.String("namespace", namespace),
						observability.String("task_queue", taskqueue),
						observability.String("runner_id", runnerID),
					)
					log.Printf("Temporal worker stopped with non-retryable error for namespace %s: %v", namespace, err)
					return err
				}
				observability.Warn(ctx, "credimi-runner.temporal", "temporal worker stopped with retryable error",
					observability.String("namespace", namespace),
					observability.String("task_queue", taskqueue),
					observability.String("runner_id", runnerID),
					observability.String("backoff", backoff.String()),
					observability.String("error", err.Error()),
				)
				span.AddEvent("temporal_worker.stopped", trace.WithAttributes(attribute.String("reason", "retryable_error"), attribute.String("error", err.Error())))
				span.AddEvent("temporal_worker.retry_scheduled", trace.WithAttributes(attribute.String("reason", "run_failed"), attribute.String("backoff", backoff.String())))
				log.Printf("Temporal worker stopped with retryable error for namespace %s: %v (retrying in %s)", namespace, err, backoff)
				if !sleepWithContextFn(ctx, backoff) {
					span.AddEvent("temporal_worker.stopped", trace.WithAttributes(attribute.String("reason", "retry_canceled")))
					signalReady(ctx.Err())
					return nil
				}
				backoff = growBackoff(backoff, maxBackoff)
				continue
			}

			log.Printf("Temporal worker stopped for namespace %s", namespace)
			span.AddEvent("temporal_worker.stopped", trace.WithAttributes(attribute.String("reason", "graceful_shutdown")))
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
	temporalInterceptor, err := observability.NewTemporalInterceptor()
	if err != nil {
		return nil, fmt.Errorf("unable to create tracing interceptor: %w", err)
	}
	hostPort := utils.GetEnvironmentVariable("TEMPORAL_ADDRESS", defaultTemporalAddress)
	c, err := client.NewLazyClient(client.Options{
		HostPort:  hostPort,
		Namespace: namespace,
		Interceptors: []interceptor.ClientInterceptor{
			temporalInterceptor,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create client: %w", err)
	}

	return c, nil
}
