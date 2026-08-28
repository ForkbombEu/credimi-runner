package workermanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	otelapi "go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type fakeTemporalWorker struct {
	workflowRegistrations int
	activityRegistrations int
	workflowNames         []string
	activityNames         []string
	runErr                error
}

type closableTemporalClient struct {
	client.Client
	closed int
}

func (c *closableTemporalClient) Close() { c.closed++ }

func (f *fakeTemporalWorker) RegisterWorkflowWithOptions(w interface{}, options workflow.RegisterOptions) {
	f.workflowRegistrations++
	f.workflowNames = append(f.workflowNames, options.Name)
}

func (f *fakeTemporalWorker) RegisterActivityWithOptions(a interface{}, options activity.RegisterOptions) {
	f.activityRegistrations++
	f.activityNames = append(f.activityNames, options.Name)
}

func (f *fakeTemporalWorker) Run(interruptCh <-chan interface{}) error {
	return f.runErr
}

func setWorkerManagerTestHooks(t *testing.T) {
	t.Helper()

	origClientGetter := temporalClientGetter
	origWorkerFactory := temporalWorkerFactory
	origSleepWithContext := sleepWithContextFn

	t.Cleanup(func() {
		temporalClientGetter = origClientGetter
		temporalWorkerFactory = origWorkerFactory
		sleepWithContextFn = origSleepWithContext
	})
}

func installWorkerManagerTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	prev := otelapi.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otelapi.SetTracerProvider(tp)
	t.Cleanup(func() {
		require.NoError(t, tp.Shutdown(context.Background()))
		otelapi.SetTracerProvider(prev)
	})

	return recorder
}

func requireWorkerManagerSpanEvent(t *testing.T, recorder *tracetest.SpanRecorder, spanName, eventName string) {
	t.Helper()

	for _, span := range recorder.Ended() {
		if span.Name() != spanName {
			continue
		}
		for _, event := range span.Events() {
			if event.Name == eventName {
				return
			}
		}
	}
	t.Fatalf("expected event %q on span %q", eventName, spanName)
}

func TestVerifyTemporalWorkerChecksAndClosesStartupClient(t *testing.T) {
	setWorkerManagerTestHooks(t)
	startupClient := &closableTemporalClient{}
	temporalClientGetter = func(namespace string) (client.Client, error) {
		if namespace != "namespace-a" {
			t.Fatalf("namespace = %q", namespace)
		}
		return startupClient, nil
	}
	if err := VerifyTemporalWorker("namespace-a"); err != nil {
		t.Fatal(err)
	}
	if startupClient.closed != 1 {
		t.Fatalf("startup client close count = %d", startupClient.closed)
	}
	temporalClientGetter = func(string) (client.Client, error) { return nil, errors.New("dial failed") }
	if err := VerifyTemporalWorker("namespace-a"); err == nil || err.Error() != "dial failed" {
		t.Fatalf("startup error = %v", err)
	}
	temporalClientGetter = func(string) (client.Client, error) { return nil, nil }
	if err := VerifyTemporalWorker("namespace-a"); err == nil || err.Error() != "Temporal client is unavailable" {
		t.Fatalf("nil startup client error = %v", err)
	}
}

func TestRunTemporalWorker_RetriesInitErrorUntilCanceled(t *testing.T) {
	setWorkerManagerTestHooks(t)
	recorder := installWorkerManagerTracer(t)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")

	temporalClientGetter = func(namespace string) (client.Client, error) {
		return nil, errors.New("dial failed")
	}

	var sleeps []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	sleepWithContextFn = func(_ context.Context, d time.Duration) bool {
		sleeps = append(sleeps, d)
		cancel()
		return false
	}
	temporalWorkerFactory = func(c client.Client, taskqueue string, options worker.Options) temporalWorker {
		t.Fatal("worker factory should not be called on client init failure")
		return nil
	}

	run := RunTemporalWorker("namespace-a")
	err := run(ctx)
	require.NoError(t, err)
	require.Equal(t, []time.Duration{time.Second}, sleeps)
	requireWorkerManagerSpanEvent(t, recorder, "temporal_worker.run", "temporal_worker.init_failed")
	requireWorkerManagerSpanEvent(t, recorder, "temporal_worker.run", "temporal_worker.retry_scheduled")
	requireWorkerManagerSpanEvent(t, recorder, "temporal_worker.run", "temporal_worker.stopped")
}

func TestRunTemporalWorker_NonRetryableRunErrorReturnsError(t *testing.T) {
	setWorkerManagerTestHooks(t)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")

	temporalClientGetter = func(namespace string) (client.Client, error) {
		return nil, nil
	}

	fake := &fakeTemporalWorker{runErr: serviceerror.NewInvalidArgument("bad worker config")}
	temporalWorkerFactory = func(c client.Client, taskqueue string, options worker.Options) temporalWorker {
		return fake
	}
	sleepWithContextFn = func(_ context.Context, d time.Duration) bool {
		t.Fatal("sleep should not be called for non-retryable worker errors")
		return false
	}

	run := RunTemporalWorker("namespace-b")
	err := run(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "bad worker config")
	require.Equal(t, 0, fake.workflowRegistrations)
	require.Equal(t, 15, fake.activityRegistrations)
	require.Contains(t, fake.activityNames, "Run APK post-install checks")
	require.Contains(t, fake.activityNames, "Setup iOS simulator")
	require.Contains(t, fake.activityNames, "Install iOS app on device")
	require.Contains(t, fake.activityNames, "Run iOS post-install checks")
	require.Contains(t, fake.activityNames, "List installed mobile apps")
	require.Contains(t, fake.activityNames, "Disable Android Play Store")
	require.Contains(t, fake.activityNames, "Start recording iOS device screen")
	require.Contains(t, fake.activityNames, "Stop recording iOS device screen")
}

func TestRunTemporalWorkerClosesItsGenerationClient(t *testing.T) {
	setWorkerManagerTestHooks(t)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")
	temporalClient := &closableTemporalClient{}
	temporalClientGetter = func(string) (client.Client, error) { return temporalClient, nil }
	temporalWorkerFactory = func(client.Client, string, worker.Options) temporalWorker { return &fakeTemporalWorker{} }

	if err := RunTemporalWorker("namespace-close")(context.Background()); err != nil {
		t.Fatal(err)
	}
	if temporalClient.closed != 1 {
		t.Fatalf("closed clients = %d, want 1", temporalClient.closed)
	}
}

func TestRunTemporalWorker_RetryableRunErrorThenSuccess(t *testing.T) {
	setWorkerManagerTestHooks(t)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")

	temporalClientGetter = func(namespace string) (client.Client, error) {
		return nil, nil
	}

	workers := []*fakeTemporalWorker{
		{runErr: errors.New("temporary transport failure")},
		{runErr: nil},
	}
	workerIdx := 0
	temporalWorkerFactory = func(c client.Client, taskqueue string, options worker.Options) temporalWorker {
		w := workers[workerIdx]
		workerIdx++
		return w
	}

	var sleeps []time.Duration
	sleepWithContextFn = func(_ context.Context, d time.Duration) bool {
		sleeps = append(sleeps, d)
		return true
	}

	run := RunTemporalWorker("namespace-c")
	err := run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, workerIdx)
	require.Equal(t, []time.Duration{time.Second}, sleeps)
}

func TestRunTemporalWorker_NonRetryableInitErrorReturnsError(t *testing.T) {
	setWorkerManagerTestHooks(t)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")

	temporalClientGetter = func(namespace string) (client.Client, error) {
		return nil, serviceerror.NewInvalidArgument("bad namespace")
	}
	sleepWithContextFn = func(_ context.Context, d time.Duration) bool {
		t.Fatal("sleep should not be called for non-retryable init errors")
		return false
	}

	run := RunTemporalWorker("namespace-d")
	err := run(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "bad namespace")
}

func TestRunTemporalWorkerWithConfigProviderUsesLiveConfigurationBoundary(t *testing.T) {
	setWorkerManagerTestHooks(t)
	invalid := RunTemporalWorkerWithConfigProvider("namespace", func() (RunnerRuntimeConfig, error) {
		return RunnerRuntimeConfig{}, errors.New("config not ready")
	})
	if err := invalid(context.Background()); err == nil || err.Error() != "load runner device inventory: config not ready" {
		t.Fatalf("invalid provider error = %v", err)
	}

	temporalClientGetter = func(string) (client.Client, error) { return nil, nil }
	fake := &fakeTemporalWorker{}
	temporalWorkerFactory = func(_ client.Client, _ string, options worker.Options) temporalWorker {
		if options.MaxConcurrentActivityExecutionSize != 0 {
			t.Fatalf("worker must use Temporal's normal concurrency instead of stale inventory sizing: %d", options.MaxConcurrentActivityExecutionSize)
		}
		return fake
	}
	providerCalls := 0
	provider := func() (RunnerRuntimeConfig, error) {
		providerCalls++
		return RunnerRuntimeConfig{RunnerID: "acme/runner", Devices: []DeviceRuntimeConfig{
			{ID: "acme/runner/one", Enabled: true},
			{ID: "acme/runner/two", Enabled: true},
		}}, nil
	}
	if err := RunTemporalWorkerWithConfigProvider("namespace", provider)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if providerCalls != 1 || fake.activityRegistrations == 0 {
		t.Fatalf("provider calls=%d registrations=%d", providerCalls, fake.activityRegistrations)
	}
}
