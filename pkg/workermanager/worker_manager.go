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
	"github.com/forkbombeu/credimi/pkg/workflowengine/workflows"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

var clientCache sync.Map

const defaultTemporalAddress = "temporal.credimi.io:7233"

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
		temporalInterceptor, err := observability.NewTemporalInterceptor()
		if err != nil {
			return fmt.Errorf("unable to create temporal tracing interceptor: %w", err)
		}
		runnerID := utils.GetEnvironmentVariable("CREDIMI_RUNNER_ID", "", true)
		runnerID = strings.TrimLeft(strings.TrimSpace(runnerID), "/")
		taskqueue := fmt.Sprintf("%s-%s", runnerID, "TaskQueue")
		backoff := time.Second
		const maxBackoff = 30 * time.Second

		for {
			if ctx.Err() != nil {
				log.Printf("Temporal worker stopped for namespace %s", namespace)
				return nil
			}

			c, err := temporalClientGetter(namespace)
			if err != nil {
				if !shouldRetryTemporalWorker(err) {
					return err
				}
				observability.Warn(ctx, "credimi-runner.temporal", "temporal worker initialization failed",
					observability.String("namespace", namespace),
					observability.String("task_queue", taskqueue),
					observability.String("backoff", backoff.String()),
					observability.String("runner_id", runnerID),
					observability.String("error", err.Error()),
				)
				log.Printf("Temporal worker failed to initialize for namespace %s: %v (retrying in %s)", namespace, err, backoff)
				if !sleepWithContextFn(ctx, backoff) {
					return nil
				}
				backoff = growBackoff(backoff, maxBackoff)
				continue
			}

			w := temporalWorkerFactory(c, taskqueue, worker.Options{
				Interceptors: []interceptor.WorkerInterceptor{temporalInterceptor},
			})

			// Register workflows
			for _, wf := range []workflowengine.Workflow{workflows.NewMobileAutomationWorkflow()} {
				w.RegisterWorkflowWithOptions(wf.Workflow, workflow.RegisterOptions{Name: wf.Name()})
			}

			// Register activities
			for _, act := range []workflowengine.ExecutableActivity{
				activities.NewHTTPActivity(),
				activities.NewRunMobileFlowActivity(),
				activities.NewStartEmulatorActivity(),
				activities.NewApkInstallActivity(),
				activities.NewApkPostInstallChecksActivity(),
				activities.NewStartIOSSimulatorActivity(),
				activities.NewInstallIOSAppActivity(),
				activities.NewIOSPostInstallChecksActivity(),
				activities.NewStartRecordingActivity(),
				activities.NewStartIOSRecordingActivity(),
				activities.NewStopRecordingActivity(),
				activities.NewStopIOSRecordingActivity(),
				// activities.NewUnlockEmulatorActivity(),
				activities.NewListInstalledAppsActivity(),
				activities.NewDisableAndroidPlayStoreActivity(),
				activities.NewCleanupDeviceActivity(),
			} {
				w.RegisterActivityWithOptions(act.Execute, activity.RegisterOptions{Name: act.Name()})
			}

			// Shutdown channel
			shutdownCh := make(chan interface{})
			go func() {
				<-ctx.Done()
				close(shutdownCh)
			}()

			log.Printf("Temporal worker running for namespace %s", namespace)
			observability.Info(ctx, "credimi-runner.temporal", "temporal worker running",
				observability.String("namespace", namespace),
				observability.String("task_queue", taskqueue),
				observability.String("runner_id", runnerID),
			)
			if err := w.Run(shutdownCh); err != nil {
				if ctx.Err() != nil {
					log.Printf("Temporal worker stopped for namespace %s", namespace)
					return nil
				}
				if !shouldRetryTemporalWorker(err) {
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
				log.Printf("Temporal worker stopped with retryable error for namespace %s: %v (retrying in %s)", namespace, err, backoff)
				if !sleepWithContextFn(ctx, backoff) {
					return nil
				}
				backoff = growBackoff(backoff, maxBackoff)
				continue
			}

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

	clientCache.Store(namespace, c)
	return c, nil
}
