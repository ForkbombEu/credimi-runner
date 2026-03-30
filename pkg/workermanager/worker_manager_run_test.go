package workermanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

func TestRunTemporalWorker_RetriesInitErrorUntilCanceled(t *testing.T) {
	setWorkerManagerTestHooks(t)
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
	require.Equal(t, 1, fake.workflowRegistrations)
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
