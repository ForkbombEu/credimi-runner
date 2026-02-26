package workermanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
)

func TestShouldRetryTemporalWorker(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		require.False(t, shouldRetryTemporalWorker(nil))
	})

	t.Run("context canceled", func(t *testing.T) {
		require.False(t, shouldRetryTemporalWorker(context.Canceled))
	})

	t.Run("context deadline exceeded", func(t *testing.T) {
		require.True(t, shouldRetryTemporalWorker(context.DeadlineExceeded))
	})

	t.Run("namespace not found", func(t *testing.T) {
		require.False(t, shouldRetryTemporalWorker(serviceerror.NewNamespaceNotFound("ns")))
	})

	t.Run("invalid argument", func(t *testing.T) {
		require.False(t, shouldRetryTemporalWorker(serviceerror.NewInvalidArgument("bad")))
	})

	t.Run("permission denied", func(t *testing.T) {
		require.False(t, shouldRetryTemporalWorker(serviceerror.NewPermissionDenied("denied", "")))
	})

	t.Run("unavailable", func(t *testing.T) {
		require.True(t, shouldRetryTemporalWorker(serviceerror.NewUnavailable("temporary")))
	})

	t.Run("generic error", func(t *testing.T) {
		require.True(t, shouldRetryTemporalWorker(errors.New("boom")))
	})
}

func TestGrowBackoff(t *testing.T) {
	require.Equal(t, 2*time.Second, growBackoff(time.Second, 30*time.Second))
	require.Equal(t, 30*time.Second, growBackoff(20*time.Second, 30*time.Second))
}

func TestSleepWithContext(t *testing.T) {
	t.Run("context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.False(t, sleepWithContext(ctx, 50*time.Millisecond))
	})

	t.Run("timer fires", func(t *testing.T) {
		require.True(t, sleepWithContext(context.Background(), time.Millisecond))
	})
}
