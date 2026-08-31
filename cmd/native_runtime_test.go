//go:build darwin

package cmd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/pkg/server"
)

type nativeEdgeFake struct {
	stopErrs []error
	starts   int
	stops    int
}

func (f *nativeEdgeFake) Start(context.Context) error { f.starts++; return nil }
func (f *nativeEdgeFake) Stop(context.Context) error {
	f.stops++
	if len(f.stopErrs) == 0 {
		return nil
	}
	err := f.stopErrs[0]
	f.stopErrs = f.stopErrs[1:]
	return err
}
func (*nativeEdgeFake) Close() error                                   { return nil }
func (*nativeEdgeFake) QuickTunnelURL(context.Context) (string, error) { return "", nil }
func (*nativeEdgeFake) VerifyPublicURL(context.Context, string) error  { return nil }
func (*nativeEdgeFake) Status(context.Context) dashboardruntime.RuntimeStatus {
	return dashboardruntime.RuntimeStatus{}
}

type nativeLifecycleFake struct {
	pauses     int
	pauseErr   error
	resumes    int
	heartbeats int
}

func (f *nativeLifecycleFake) Pause(context.Context, string) error     { f.pauses++; return f.pauseErr }
func (f *nativeLifecycleFake) Resume(context.Context, string) error    { f.resumes++; return nil }
func (f *nativeLifecycleFake) Heartbeat(context.Context) error         { f.heartbeats++; return nil }
func (*nativeLifecycleFake) StartHeartbeatLoop(context.Context) func() { return noopCancel }

func testNativeGeneration(t *testing.T, edge nativeEdge) *nativeRuntimeGeneration {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	generation := &nativeRuntimeGeneration{
		ctx: ctx, cancel: cancel, listener: listener, http: &http.Server{},
		store: server.NewProcessStore(), lifecycle: &nativeLifecycleFake{}, edge: edge,
		stopBeat: noopCancel, shutdownOTEL: func(context.Context) error { return nil }, edgeStarted: true,
	}
	return generation
}

type nativeServiceFake struct{ starts int }

func (f *nativeServiceFake) StartExistingWorkers(context.Context) error { f.starts++; return nil }

func TestNativeSupervisorFullLifecycleHasOneActiveGeneration(t *testing.T) {
	dir := t.TempDir()
	edge := &nativeEdgeFake{}
	lifecycle := &nativeLifecycleFake{}
	generation := testNativeGeneration(t, edge)
	generation.edgeStarted = false
	generation.lifecycle = lifecycle
	generation.service = &nativeServiceFake{}
	supervisor := NewNativeRuntimeSupervisor(dir)
	supervisor.generation = generation

	if err := supervisor.StartExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.StartExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if supervisor.generation != nil || supervisor.ExecutionRunning() {
		t.Fatal("full lifecycle left an active generation or running intent")
	}
	if edge.starts != 2 || edge.stops != 2 {
		t.Fatalf("edge starts=%d stops=%d, want two complete transitions", edge.starts, edge.stops)
	}
	if lifecycle.resumes != 2 || lifecycle.heartbeats != 2 {
		t.Fatalf("resumes=%d heartbeats=%d", lifecycle.resumes, lifecycle.heartbeats)
	}
}

func TestNativeGenerationRetriesFailedEdgeStop(t *testing.T) {
	edge := &nativeEdgeFake{stopErrs: []error{errors.New("edge still running"), nil}}
	generation := testNativeGeneration(t, edge)
	if err := generation.stopEdge(context.Background()); err == nil {
		t.Fatal("first edge stop unexpectedly succeeded")
	}
	if !generation.edgeStarted {
		t.Fatal("failed edge stop released ownership")
	}
	if err := generation.stopEdge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if generation.edgeStarted || edge.stops != 2 {
		t.Fatalf("edge ownership=%t stops=%d", generation.edgeStarted, edge.stops)
	}
}

func TestNativeSupervisorStopRetriesEdgeAndKeepsTruthfulState(t *testing.T) {
	dir := t.TempDir()
	edge := &nativeEdgeFake{stopErrs: []error{errors.New("edge still running"), nil}}
	generation := testNativeGeneration(t, edge)
	supervisor := NewNativeRuntimeSupervisor(dir)
	supervisor.generation = generation
	supervisor.executing = true
	supervisor.intent = ExecutionRunning
	if err := supervisor.Stop(context.Background()); err == nil {
		t.Fatal("stop unexpectedly succeeded")
	}
	state, err := os.ReadFile(filepath.Join(dir, "runtime-state"))
	if err != nil || !strings.HasPrefix(string(state), "failed:stopped:") {
		t.Fatalf("runtime state=%q err=%v", state, err)
	}
	if !generation.edgeStarted {
		t.Fatal("failed stop released edge ownership")
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = os.ReadFile(filepath.Join(dir, "runtime-state"))
	if err != nil || strings.TrimSpace(string(state)) != "stopped" {
		t.Fatalf("runtime state=%q err=%v", state, err)
	}
}

func TestNativeSupervisorStopPauseFailureStillCleansLocalResources(t *testing.T) {
	dir := t.TempDir()
	edge := &nativeEdgeFake{}
	generation := testNativeGeneration(t, edge)
	generation.lifecycle = &nativeLifecycleFake{pauseErr: errors.New("pause unavailable")}
	supervisor := NewNativeRuntimeSupervisor(dir)
	supervisor.generation = generation
	supervisor.executing = true
	supervisor.intent = ExecutionRunning

	err := supervisor.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pause unavailable") {
		t.Fatalf("stop error = %v", err)
	}
	if supervisor.ExecutionRunning() {
		t.Fatal("stop did not change execution intent")
	}
	if supervisor.Status(context.Background()).RunnerRunning {
		t.Fatal("stopped runtime still reports execution")
	}
	state, readErr := os.ReadFile(filepath.Join(dir, "runtime-state"))
	if readErr != nil || strings.TrimSpace(string(state)) != "stopped" {
		t.Fatalf("runtime state=%q err=%v", state, readErr)
	}
	if generation.edgeStarted {
		t.Fatal("edge remained owned after successful local cleanup")
	}
}

func TestNativeGenerationPauseAndEdgeFailuresRemainRetryable(t *testing.T) {
	edge := &nativeEdgeFake{stopErrs: []error{errors.New("edge unavailable"), nil}}
	lifecycle := &nativeLifecycleFake{pauseErr: errors.New("pause unavailable")}
	generation := testNativeGeneration(t, edge)
	generation.lifecycle = lifecycle

	err := generation.close(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "pause unavailable") || !strings.Contains(err.Error(), "edge unavailable") {
		t.Fatalf("close error = %v", err)
	}
	if generation.localResourcesClosed() {
		t.Fatal("failed edge cleanup was reported as locally complete")
	}
	if err := generation.close(context.Background(), true); err == nil || !strings.Contains(err.Error(), "pause unavailable") {
		t.Fatalf("second close error = %v", err)
	}
	if generation.edgeStarted {
		t.Fatal("successful retry did not release edge ownership")
	}
}

func TestNativeSupervisorReconcileReleasesLocallyClosedGenerationAfterPauseFailure(t *testing.T) {
	dir := t.TempDir()
	writeNativeRuntimeTestConfig(t, dir, lifecycleFreeListenAddress(t))
	generation := testNativeGeneration(t, &nativeEdgeFake{})
	generation.lifecycle = &nativeLifecycleFake{pauseErr: errors.New("pause unavailable")}
	supervisor := NewNativeRuntimeSupervisor(dir)
	supervisor.generation = generation
	supervisor.executing = true
	supervisor.intent = ExecutionRunning

	err := supervisor.reconcileLocked(context.Background(), dashboardruntime.Values{}, false)
	if err == nil || !strings.Contains(err.Error(), "pause unavailable") {
		t.Fatalf("reconcile error = %v", err)
	}
	if supervisor.generation != nil || supervisor.Status(context.Background()).RunnerRunning {
		t.Fatal("locally closed generation remained owned as executing")
	}
	state, readErr := os.ReadFile(filepath.Join(dir, "runtime-state"))
	if readErr != nil || !strings.HasPrefix(string(state), "failed:running:") {
		t.Fatalf("runtime state=%q err=%v", state, readErr)
	}
}

func TestNativeSupervisorRetainsGenerationWhileWorkerShutdownIsIncomplete(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	generation := testNativeGeneration(t, &nativeEdgeFake{})
	worker := server.NewProcess("slow-worker", func(context.Context) error {
		<-release
		return nil
	})
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	generation.store.Add(worker)
	supervisor := NewNativeRuntimeSupervisor(dir)
	supervisor.generation = generation

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := supervisor.Close(ctx); err == nil {
		t.Fatal("close unexpectedly succeeded while worker was still unwinding")
	}
	if supervisor.generation != generation || generation.localResourcesClosed() {
		t.Fatal("supervisor released an incompletely closed generation")
	}
	close(release)
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if supervisor.generation != nil {
		t.Fatal("supervisor retained a fully closed generation")
	}
}

func TestNativeSupervisorCloseRetriesFailedGenerationCleanup(t *testing.T) {
	dir := t.TempDir()
	edge := &nativeEdgeFake{stopErrs: []error{errors.New("edge still running"), nil}}
	generation := testNativeGeneration(t, edge)
	supervisor := NewNativeRuntimeSupervisor(dir)
	supervisor.generation = generation
	if err := supervisor.Close(context.Background()); err == nil {
		t.Fatal("close unexpectedly succeeded")
	}
	if supervisor.generation != generation {
		t.Fatal("supervisor discarded generation after failed cleanup")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if supervisor.generation != nil || edge.stops != 2 {
		t.Fatalf("generation=%#v stops=%d", supervisor.generation, edge.stops)
	}
}

func TestNativeRuntimeFailedStatePreservesRequestedIntent(t *testing.T) {
	dir := t.TempDir()
	if err := writeNativeFailure(dir, ExecutionRunning, errors.New("registration failed")); err != nil {
		t.Fatal(err)
	}
	if !nativeRuntimeShouldRun(dir) {
		t.Fatal("failed running state lost execution intent")
	}
	if err := writeNativeFailure(dir, ExecutionStopped, errors.New("listener cleanup failed")); err != nil {
		t.Fatal(err)
	}
	if nativeRuntimeShouldRun(dir) {
		t.Fatal("failed stopped state incorrectly requests execution")
	}
}

func TestNativeRuntimeSupervisorRebindsRunnerListener(t *testing.T) {
	dir := t.TempDir()
	addressA := lifecycleFreeListenAddress(t)
	addressB := lifecycleFreeListenAddress(t)
	writeNativeRuntimeTestConfig(t, dir, addressA)
	supervisor := NewNativeRuntimeSupervisor(dir)
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	if err := supervisor.Reconcile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	dialNativeRuntime(t, addressA, true)
	writeNativeRuntimeTestConfig(t, dir, addressB)
	if err := supervisor.Reconcile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	dialNativeRuntime(t, addressA, false)
	dialNativeRuntime(t, addressB, true)
	if supervisor.Status(context.Background()).RunnerRunning {
		t.Fatal("stopped reconciliation started execution")
	}
}

func TestNativeRuntimeSupervisorStoppedGenerationUsesLatestConfigOnStart(t *testing.T) {
	dir := t.TempDir()
	addressA := lifecycleFreeListenAddress(t)
	addressB := lifecycleFreeListenAddress(t)
	writeNativeRuntimeTestConfig(t, dir, addressA)
	supervisor := NewNativeRuntimeSupervisor(dir)
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	if err := supervisor.Reconcile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	writeNativeRuntimeTestConfig(t, dir, addressB)
	if err := supervisor.Reconcile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	dialNativeRuntime(t, addressA, false)
	dialNativeRuntime(t, addressB, true)
}

func writeNativeRuntimeTestConfig(t *testing.T, dir, apiListen string) {
	t.Helper()
	cfg := runnerconfig.Bootstrap()
	cfg.Runner = runnerconfig.RunnerConfig{ID: "acme/runner", Name: "runner", Organization: "acme"}
	cfg.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "test"}
	cfg.Temporal = runnerconfig.TemporalConfig{Address: "temporal.example:7233"}
	cfg.Server.APIListen = apiListen
	cfg.Server.DashboardListen = lifecycleFreeListenAddress(t)
	cfg.Exposure = runnerconfig.ExposureConfig{Mode: "manual", PublicURL: "https://runner.example"}
	cfg.Devices = []runnerconfig.DeviceConfig{{
		ID: "acme/runner/no-device", Name: "No device", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "no_device"},
	}}
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
}

func dialNativeRuntime(t *testing.T, address string, wantOpen bool) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if wantOpen {
		if err != nil {
			t.Fatalf("dial %s: %v", address, err)
		}
		_ = connection.Close()
		return
	}
	if err == nil {
		_ = connection.Close()
		t.Fatalf("old listener %s remains open", address)
	}
}
