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
)

type fakeTemporalWorker struct {
	activityRegistrations int
	activityNames         []string
	startErr              error
	fatalErr              error
	startHook             func()
	options               worker.Options
	stops                 int
	stopStarted           chan struct{}
	stopRelease           chan struct{}
}

type closableTemporalClient struct {
	client.Client
	closed    int
	closeHook func()
}

func (c *closableTemporalClient) Close() {
	c.closed++
	if c.closeHook != nil {
		c.closeHook()
	}
}

func (f *fakeTemporalWorker) RegisterActivityWithOptions(a interface{}, options activity.RegisterOptions) {
	f.activityRegistrations++
	f.activityNames = append(f.activityNames, options.Name)
}

func (f *fakeTemporalWorker) Start() error {
	if f.startErr != nil {
		return f.startErr
	}
	if f.fatalErr != nil && f.options.OnFatalError != nil {
		f.options.OnFatalError(f.fatalErr)
	}
	if f.startHook != nil {
		f.startHook()
	}
	return nil
}

func (f *fakeTemporalWorker) Stop() {
	f.stops++
	if f.stopStarted != nil {
		close(f.stopStarted)
	}
	if f.stopRelease != nil {
		<-f.stopRelease
	}
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
	err := run(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, []time.Duration{time.Second}, sleeps)
	requireWorkerManagerSpanEvent(t, recorder, "temporal_worker.run", "temporal_worker.init_failed")
	requireWorkerManagerSpanEvent(t, recorder, "temporal_worker.run", "temporal_worker.retry_scheduled")
	requireWorkerManagerSpanEvent(t, recorder, "temporal_worker.run", "temporal_worker.stopped")
}

func TestRunTemporalWorkerUnblocksReadinessWhenStartupRetryIsCanceled(t *testing.T) {
	setWorkerManagerTestHooks(t)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	temporalClientGetter = func(string) (client.Client, error) {
		return nil, errors.New("dial failed")
	}
	sleepWithContextFn = func(context.Context, time.Duration) bool {
		cancel()
		return false
	}
	ready := make(chan error, 1)
	err := RunTemporalWorker("namespace-ready-retry")(ctx, func(err error) { ready <- err })
	require.NoError(t, err)
	select {
	case readyErr := <-ready:
		require.ErrorIs(t, readyErr, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("startup readiness was not released after cancellation")
	}
}

func TestRunTemporalWorker_NonRetryableRunErrorReturnsError(t *testing.T) {
	setWorkerManagerTestHooks(t)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")

	temporalClientGetter = func(namespace string) (client.Client, error) {
		return nil, nil
	}

	fake := &fakeTemporalWorker{startErr: serviceerror.NewInvalidArgument("bad worker config")}
	temporalWorkerFactory = func(c client.Client, taskqueue string, options worker.Options) temporalWorker {
		fake.options = options
		return fake
	}
	sleepWithContextFn = func(_ context.Context, d time.Duration) bool {
		t.Fatal("sleep should not be called for non-retryable worker errors")
		return false
	}

	run := RunTemporalWorker("namespace-b")
	err := run(context.Background(), nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "bad worker config")
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

func TestRunTemporalWorkerStartFailureClosesClientSynchronously(t *testing.T) {
	setWorkerManagerTestHooks(t)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")
	startupClient := &closableTemporalClient{}
	fake := &fakeTemporalWorker{startErr: serviceerror.NewInvalidArgument("worker startup failed")}
	temporalClientGetter = func(string) (client.Client, error) { return startupClient, nil }
	temporalWorkerFactory = func(_ client.Client, _ string, options worker.Options) temporalWorker {
		fake.options = options
		return fake
	}

	err := RunTemporalWorker("namespace-start-failure")(context.Background(), nil)
	require.ErrorContains(t, err, "worker startup failed")
	require.Equal(t, 1, startupClient.closed)
	require.Zero(t, fake.stops)
}

func TestRunTemporalWorkerWaitsForWorkerStopBeforeClosingClient(t *testing.T) {
	setWorkerManagerTestHooks(t)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")
	ctx, cancel := context.WithCancel(context.Background())
	stopStarted := make(chan struct{})
	stopRelease := make(chan struct{})
	clientClosed := make(chan struct{})
	startupClient := &closableTemporalClient{closeHook: func() { close(clientClosed) }}
	fake := &fakeTemporalWorker{startHook: cancel, stopStarted: stopStarted, stopRelease: stopRelease}
	temporalClientGetter = func(string) (client.Client, error) { return startupClient, nil }
	temporalWorkerFactory = func(_ client.Client, _ string, options worker.Options) temporalWorker {
		fake.options = options
		return fake
	}

	done := make(chan error, 1)
	go func() { done <- RunTemporalWorker("namespace-stop-order")(ctx, nil) }()
	<-stopStarted
	select {
	case <-done:
		t.Fatal("worker returned before Stop completed")
	default:
	}
	select {
	case <-clientClosed:
		t.Fatal("client closed before worker Stop completed")
	default:
	}
	close(stopRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if fake.stops != 1 || startupClient.closed != 1 {
		t.Fatalf("worker stops=%d client closes=%d", fake.stops, startupClient.closed)
	}
}

func TestRunTemporalWorkerClosesItsGenerationClient(t *testing.T) {
	setWorkerManagerTestHooks(t)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")
	temporalClient := &closableTemporalClient{}
	temporalClientGetter = func(string) (client.Client, error) { return temporalClient, nil }
	ctx, cancel := context.WithCancel(context.Background())
	temporalWorkerFactory = func(client.Client, string, worker.Options) temporalWorker {
		return &fakeTemporalWorker{startHook: cancel}
	}

	if err := RunTemporalWorker("namespace-close")(ctx, nil); err != nil {
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

	ctx, cancel := context.WithCancel(context.Background())
	workers := []*fakeTemporalWorker{
		{fatalErr: errors.New("temporary transport failure")},
		{startHook: cancel},
	}
	workerIdx := 0
	temporalWorkerFactory = func(c client.Client, taskqueue string, options worker.Options) temporalWorker {
		w := workers[workerIdx]
		w.options = options
		workerIdx++
		return w
	}

	var sleeps []time.Duration
	sleepWithContextFn = func(_ context.Context, d time.Duration) bool {
		sleeps = append(sleeps, d)
		return true
	}

	run := RunTemporalWorker("namespace-c")
	err := run(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, 2, workerIdx)
	require.Equal(t, []time.Duration{time.Second}, sleeps)
	require.Equal(t, 1, workers[0].stops)
	require.Equal(t, 1, workers[1].stops)
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
	err := run(context.Background(), nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "bad namespace")
}

func TestRunTemporalWorkerWithConfigProviderUsesLiveConfigurationBoundary(t *testing.T) {
	setWorkerManagerTestHooks(t)
	invalid := RunTemporalWorkerWithConfigProvider("namespace", func() (RunnerRuntimeConfig, error) {
		return RunnerRuntimeConfig{}, errors.New("config not ready")
	})
	if err := invalid(context.Background(), nil); err == nil || err.Error() != "load runner device inventory: config not ready" {
		t.Fatalf("invalid provider error = %v", err)
	}

	temporalClientGetter = func(string) (client.Client, error) { return nil, nil }
	fake := &fakeTemporalWorker{}
	ctx, cancel := context.WithCancel(context.Background())
	temporalWorkerFactory = func(_ client.Client, _ string, options worker.Options) temporalWorker {
		if options.MaxConcurrentActivityExecutionSize != 0 {
			t.Fatalf("worker must use Temporal's normal concurrency instead of stale inventory sizing: %d", options.MaxConcurrentActivityExecutionSize)
		}
		fake.options = options
		fake.startHook = cancel
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
	if err := RunTemporalWorkerWithConfigProvider("namespace", provider)(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if providerCalls != 1 || fake.activityRegistrations == 0 {
		t.Fatalf("provider calls=%d registrations=%d", providerCalls, fake.activityRegistrations)
	}
}
