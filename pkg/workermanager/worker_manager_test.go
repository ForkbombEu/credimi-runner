package workermanager

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
)

func resetClientCacheForTest() {
	clientCache = sync.Map{}
}

func TestGetTemporalClientWithNamespace_Cache(t *testing.T) {
	resetClientCacheForTest()
	t.Setenv("TEMPORAL_ADDRESS", client.DefaultHostPort)

	c1, err := getTemporalClientWithNamespace("ns-one")
	require.NoError(t, err)
	t.Cleanup(func() { c1.Close() })

	c2, err := getTemporalClientWithNamespace("ns-one")
	require.NoError(t, err)
	require.Same(t, c1, c2)

	c3, err := getTemporalClientWithNamespace("ns-two")
	require.NoError(t, err)
	t.Cleanup(func() { c3.Close() })
	require.NotSame(t, c1, c3)
}

func TestRunTemporalWorker_MissingRunnerIDReturnsError(t *testing.T) {
	resetClientCacheForTest()
	t.Setenv("TEMPORAL_ADDRESS", client.DefaultHostPort)
	require.NoError(t, os.Unsetenv("CREDIMI_RUNNER_ID"))

	run := RunTemporalWorker("namespace-a")
	require.ErrorContains(t, run(context.Background()), "runner ID is required")
}

func TestRunTemporalWorker_ReturnsOnCanceledContext(t *testing.T) {
	resetClientCacheForTest()
	t.Setenv("TEMPORAL_ADDRESS", client.DefaultHostPort)
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")

	run := RunTemporalWorker("namespace-b")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunTemporalWorker did not return after context cancellation")
	}
}
