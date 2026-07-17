package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControllerLeaseExcludesSecondOwnerAndPublishesMetadata(t *testing.T) {
	dir := t.TempDir()
	lease, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	metadata := Metadata{
		ControllerID: "controller-1", PID: os.Getpid(), ConfigDir: dir,
		ListenHost: "0.0.0.0", ListenPort: 8051,
		ProbeURL: "http://127.0.0.1:8051/healthz", StartedAt: time.Now(),
	}
	if err := lease.Publish(metadata); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.ControllerID != metadata.ControllerID || got.ListenPort != metadata.ListenPort {
		t.Fatalf("metadata = %#v", got)
	}
	second, err := Acquire(dir)
	if !errors.Is(err, ErrAlreadyRunning) || second != nil {
		t.Fatalf("second acquire = %#v, %v", second, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "controller.json")); !os.IsNotExist(err) {
		t.Fatalf("metadata should be removed on close: %v", err)
	}
	third, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
}

func TestControllerProbeRejectsUnavailableEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := Probe(ctx, Metadata{ProbeURL: "http://127.0.0.1:1/healthz"})
	if err == nil {
		t.Fatal("expected unavailable controller probe")
	}
}
