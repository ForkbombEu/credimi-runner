package launcher

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpgradeRequestIsAllowListedAndAcceptedBeforeReplacement(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	server, err := Serve(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error {
		close(started)
		<-finished
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	if err := RequestUpgrade(context.Background(), server.listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upgrade operation was not started")
	}
	close(finished)
}

func TestLauncherRejectsUnknownAndExtraOperations(t *testing.T) {
	called := false
	server, err := Serve(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error {
		called = true
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	for _, payload := range []string{
		`{"operation":"exec","command":"docker rm -f anything"}`,
		`{"operation":"upgrade-runner-image","unexpected":true}`,
	} {
		connection, err := net.Dial("unix", server.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = connection.Write([]byte(payload + "\n"))
		var response response
		if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
			t.Fatal(err)
		}
		_ = connection.Close()
		if response.Accepted || response.Error == "" {
			t.Fatalf("payload %s response = %#v", payload, response)
		}
	}
	if called {
		t.Fatal("rejected operations invoked image replacement")
	}
}

func TestLauncherRejectsUpgradeWhileBusy(t *testing.T) {
	server, err := Serve(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error {
		t.Fatal("busy upgrade should not run")
		return errors.New("unreachable")
	}, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	if err := RequestUpgrade(context.Background(), server.listener.Addr().String()); err == nil {
		t.Fatal("busy launcher accepted upgrade")
	}
}

func TestLauncherRechecksBusyStateBeforeReplacement(t *testing.T) {
	var checks atomic.Int32
	called := make(chan struct{}, 1)
	server, err := Serve(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error {
		called <- struct{}{}
		return nil
	}, func() bool {
		return checks.Add(1) > 1
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := RequestUpgrade(context.Background(), server.listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
		t.Fatal("upgrade ran after the final busy check")
	case <-time.After(50 * time.Millisecond):
	}
	if got := checks.Load(); got < 2 {
		t.Fatalf("busy checks = %d, want an admission and final check", got)
	}
}
