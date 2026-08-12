package launcher

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
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

	result := make(chan error, 1)
	go func() { result <- RequestUpgrade(context.Background(), server.listener.Addr().String()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upgrade operation was not started")
	}
	close(finished)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
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
	if err := RequestUpgrade(context.Background(), server.listener.Addr().String()); err == nil {
		t.Fatal("busy upgrade unexpectedly succeeded")
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

func TestLauncherTypedOperationsAreAllowListed(t *testing.T) {
	called := make(chan string, 2)
	server, err := ServeWithOperations(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error { return nil }, nil, Operations{
		ReconcileConfig: func(context.Context) error { called <- ReconcileConfig; return nil },
		RuntimeRestart:  func(context.Context) error { called <- RuntimeRestart; return nil },
		QuickTunnelURL:  func(context.Context) (string, error) { return "https://current.trycloudflare.com", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := RequestReconcile(context.Background(), server.listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if err := RequestRuntimeAction(context.Background(), server.listener.Addr().String(), "restart"); err != nil {
		t.Fatal(err)
	}
	url, err := RequestQuickTunnelURL(context.Background(), server.listener.Addr().String())
	if err != nil || url != "https://current.trycloudflare.com" {
		t.Fatalf("quick tunnel URL=%q err=%v", url, err)
	}
	for range 2 {
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("typed launcher operation was not invoked")
		}
	}
	if err := RequestRuntimeAction(context.Background(), server.listener.Addr().String(), "exec"); err == nil {
		t.Fatal("arbitrary runtime action was accepted")
	}
}

func TestLauncherPersistsSetupAndConfigOperationReferencesSeparately(t *testing.T) {
	dir := t.TempDir()
	server, err := ServeWithOperations(filepath.Join(dir, "control.sock"), func(context.Context) error { return nil }, nil, Operations{
		ReconcileSetup:  func(context.Context) error { return nil },
		ReconcileConfig: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	path := server.listener.Addr().String()
	setup, err := RequestSetupReconcileAsync(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	setupID, err := os.ReadFile(filepath.Join(dir, setupOperationFile))
	if err != nil || strings.TrimSpace(string(setupID)) != setup.ID {
		t.Fatalf("setup operation reference = %q, err=%v", setupID, err)
	}
	config, err := RequestReconcileAsync(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	configID, err := os.ReadFile(filepath.Join(dir, configOperationFile))
	if err != nil || strings.TrimSpace(string(configID)) != config.ID {
		t.Fatalf("config operation reference = %q, err=%v", configID, err)
	}
}

func TestRequestSetupReconcileWaitsForTerminalResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server, err := ServeWithOperations(path, func(context.Context) error { return nil }, nil, Operations{
		ReconcileSetup: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := RequestSetupReconcile(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "setup-operation")); err != nil {
		t.Fatalf("setup operation reference after terminal request: %v", err)
	}
}

func TestLauncherTypedOperationsReportRejectedAndUnconfiguredRequests(t *testing.T) {
	server, err := ServeWithOperations(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error { return nil }, nil, Operations{
		QuickTunnelURL: func(context.Context) (string, error) { return "", errors.New("tunnel is not ready") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	path := server.listener.Addr().String()
	if _, err := RequestQuickTunnelURL(context.Background(), path); err == nil || !strings.Contains(err.Error(), "tunnel is not ready") {
		t.Fatalf("quick tunnel rejection = %v", err)
	}
	if err := RequestReconcile(context.Background(), path); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured reconcile = %v", err)
	}
}

func TestLauncherOperationFailureIsReturnedToCaller(t *testing.T) {
	server, err := ServeWithOperations(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error { return nil }, nil, Operations{
		ReconcileConfig: func(context.Context) error { return errors.New("compose reconciliation failed") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := RequestReconcile(context.Background(), server.listener.Addr().String()); err == nil || !strings.Contains(err.Error(), "compose reconciliation failed") {
		t.Fatalf("reconcile failure = %v", err)
	}
}

func TestLauncherAsyncRuntimeActionCanBeObservedUntilCompletion(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	server, err := ServeWithOperations(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error { return nil }, nil, Operations{
		RuntimeStart: func(context.Context) error {
			close(started)
			<-finished
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	path := server.listener.Addr().String()
	handle, err := RequestRuntimeActionAsync(context.Background(), path, "start")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runtime start did not begin")
	}
	status, err := RequestOperationStatus(context.Background(), path, handle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseRunning && status.Phase != PhaseQueued {
		t.Fatalf("in-flight runtime status = %#v", status)
	}
	close(finished)
	if err := waitOperation(context.Background(), path, handle, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherOperationWaitHonorsCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	server, err := ServeWithOperations(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error { return nil }, nil, Operations{
		RuntimeStop: func(context.Context) error {
			close(started)
			<-finished
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	handle, err := RequestRuntimeActionAsync(ctx, server.listener.Addr().String(), "stop")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runtime stop did not begin")
	}
	cancel()
	if err := waitOperation(ctx, server.listener.Addr().String(), handle, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled operation error = %v", err)
	}
	close(finished)
}

func TestLauncherOperationWaitReturnsReconcileFailure(t *testing.T) {
	server, err := ServeWithOperations(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error { return nil }, nil, Operations{
		ReconcileConfig: func(context.Context) error { return errors.New("docker compose failed") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handle, err := RequestReconcileAsync(context.Background(), server.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := waitOperation(context.Background(), server.listener.Addr().String(), handle, nil); err == nil || !strings.Contains(err.Error(), "docker compose failed") {
		t.Fatalf("reconcile failure = %v", err)
	}
	if handle.ID == "" {
		t.Fatal("reconcile operation did not return an ID")
	}
}

func TestLauncherRejectsUnknownOperationStatus(t *testing.T) {
	server, err := ServeWithOperations(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error { return nil }, nil, Operations{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := operationStatus(context.Background(), server.listener.Addr().String(), "missing-operation"); err == nil || !strings.Contains(err.Error(), "operation not found") {
		t.Fatalf("unknown operation status = %v", err)
	}
	if _, err := RequestRuntimeActionAsync(context.Background(), server.listener.Addr().String(), "invalid"); err == nil {
		t.Fatal("invalid runtime action was accepted")
	}
}

func TestLauncherReportsMissingQuickTunnelOperationAndMalformedRequests(t *testing.T) {
	server, err := ServeWithOperations(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error { return nil }, nil, Operations{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	path := server.listener.Addr().String()
	if _, err := RequestQuickTunnelURL(context.Background(), path); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing quick tunnel operation = %v", err)
	}
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = connection.Write([]byte("not-json\n"))
	var result response
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&result); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if result.Accepted || result.Error != "invalid launcher request" {
		t.Fatalf("malformed request response = %#v", result)
	}
}

func TestLauncherRejectsInvalidServerConfiguration(t *testing.T) {
	if _, err := ServeWithOperations(filepath.Join(t.TempDir(), "control.sock"), nil, nil, Operations{}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing upgrade callback = %v", err)
	}
	server, err := ServeWithOperations(filepath.Join(t.TempDir(), "control.sock"), func(context.Context) error { return nil }, nil, Operations{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
}

func TestLauncherRejectsReconcileWhenOperationReferenceCannotBePublished(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "config-operation"), 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := ServeWithOperations(filepath.Join(dir, "control.sock"), func(context.Context) error { return nil }, nil, Operations{
		ReconcileConfig: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := RequestReconcileAsync(context.Background(), server.listener.Addr().String()); err == nil || !strings.Contains(err.Error(), "publish config-operation reference") {
		t.Fatalf("unpublishable operation reference error = %v", err)
	}
}
