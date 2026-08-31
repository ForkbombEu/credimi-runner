package server

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
)

func TestReadyProcessPropagatesActualStartupFailure(t *testing.T) {
	process := NewReadyProcess("namespace", func(context.Context, func(error)) error {
		return errors.New("worker registration failed")
	})
	err := process.StartReady(context.Background())
	if err == nil || err.Error() != "worker registration failed" {
		t.Fatalf("startup error = %v", err)
	}
	for i := 0; i < 100 && process.IsRunning(); i++ {
		// The worker goroutine clears its own state after signalling failure.
		runtime.Gosched()
	}
	if process.IsRunning() {
		t.Fatal("failed startup left process running")
	}
}

func TestProcessRestartKeepsNewWorkerRunningWhileOldWorkerExits(t *testing.T) {
	firstStarted := make(chan struct{})
	firstCancelled := make(chan struct{})
	firstExited := make(chan struct{})
	releaseFirst := make(chan struct{})
	var starts atomic.Int32
	process := NewProcess("namespace", func(ctx context.Context) error {
		if starts.Add(1) == 1 {
			close(firstStarted)
			<-ctx.Done()
			close(firstCancelled)
			<-releaseFirst
			close(firstExited)
			return nil
		}
		<-ctx.Done()
		return nil
	})

	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	<-firstStarted
	process.Stop()
	<-firstCancelled
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	if !process.IsRunning() {
		t.Fatal("replacement worker did not start")
	}
	close(releaseFirst)
	<-firstExited
	if !process.IsRunning() {
		t.Fatal("old worker exit marked replacement worker stopped")
	}
	process.Stop()
}
