package server

import (
	"context"
	"sync/atomic"
	"testing"
)

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
