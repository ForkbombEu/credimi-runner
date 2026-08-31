package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadyProcessPropagatesActualStartupFailure(t *testing.T) {
	process := NewReadyProcess("namespace", func(context.Context, func(error)) error {
		return errors.New("worker registration failed")
	})
	err := process.StartReady(context.Background())
	if err == nil || err.Error() != "worker registration failed" {
		t.Fatalf("startup error = %v", err)
	}
	if err := process.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if process.IsRunning() {
		t.Fatal("failed startup left process running")
	}
}

func TestProcessCannotRestartUntilOldWorkerExits(t *testing.T) {
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
	if err := process.Start(); err == nil || err.Error() != "worker is still stopping" {
		t.Fatalf("restart while stopping error = %v", err)
	}
	close(releaseFirst)
	<-firstExited
	if err := process.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	if !process.IsRunning() {
		t.Fatal("replacement worker did not start")
	}
	process.Stop()
	if err := process.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProcessStopAndWaitWaitsForRunner(t *testing.T) {
	started := make(chan struct{})
	process := NewProcess("namespace", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return nil
	})
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := process.StopAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if process.IsRunning() {
		t.Fatal("process remains running after StopAndWait")
	}
}

func TestProcessStopIsIdempotent(t *testing.T) {
	var cancels atomic.Int32
	process := NewProcess("namespace", func(ctx context.Context) error { <-ctx.Done(); return nil })
	process.Start()
	process.mu.Lock()
	original := process.CancelFunc
	process.CancelFunc = func() { cancels.Add(1); original() }
	process.mu.Unlock()
	process.Stop()
	process.Stop()
	if err := process.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := cancels.Load(); got != 1 {
		t.Fatalf("cancel calls = %d, want 1", got)
	}
}

func TestProcessWaitHonorsContextDeadline(t *testing.T) {
	release := make(chan struct{})
	process := NewProcess("namespace", func(context.Context) error { <-release; return nil })
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	process.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := process.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v", err)
	}
	close(release)
	if err := process.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProcessStoreStopAllAndWaitCancelsAllBeforeWaiting(t *testing.T) {
	store := NewProcessStore()
	var canceled sync.WaitGroup
	canceled.Add(2)
	release := make(chan struct{})
	for _, name := range []string{"one", "two"} {
		process := NewProcess(name, func(ctx context.Context) error {
			<-ctx.Done()
			canceled.Done()
			<-release
			return nil
		})
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		store.Add(process)
	}
	done := make(chan error, 1)
	go func() { done <- store.StopAllAndWait(context.Background()) }()
	canceled.Wait()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
