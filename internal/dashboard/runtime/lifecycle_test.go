package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/lifecyclelog"
)

type fakeRunner struct {
	runs        []CommandSpec
	starts      []CommandSpec
	startCtxErr error
	runOutput   []byte
	runErr      error
	startErr    error
}

type failOnRunRunner struct {
	fakeRunner
	failAt int
	calls  int
}

type imageVersionRunner struct {
	fakeRunner
	imageIDs    []string
	inspectCall int
}

var fakeTunnelStartedAt = time.Date(2026, 7, 14, 16, 23, 32, 123456789, time.UTC)

func (f *imageVersionRunner) Run(ctx context.Context, spec CommandSpec) ([]byte, error) {
	if len(spec.Args) >= 2 && spec.Args[0] == "image" && spec.Args[1] == "inspect" {
		f.runs = append(f.runs, spec)
		id := f.imageIDs[f.inspectCall]
		f.inspectCall++
		return []byte(id + "\n"), nil
	}
	return f.fakeRunner.Run(ctx, spec)
}

func (f *failOnRunRunner) Run(ctx context.Context, spec CommandSpec) ([]byte, error) {
	f.calls++
	if f.calls == f.failAt {
		return nil, errors.New("docker command failed")
	}
	return f.fakeRunner.Run(ctx, spec)
}

func (f *fakeRunner) Run(_ context.Context, spec CommandSpec) ([]byte, error) {
	f.runs = append(f.runs, spec)
	args := strings.Join(spec.Args, " ")
	if strings.Contains(args, " ps -q tunnel") {
		return []byte("tunnel-container\n"), nil
	}
	if strings.HasPrefix(args, "inspect --format {{.State.StartedAt}} tunnel-container") {
		return []byte(fakeTunnelStartedAt.Format(time.RFC3339Nano) + "\n"), nil
	}
	if f.runOutput != nil || f.runErr != nil {
		if spec.Stream != nil {
			for _, line := range strings.Split(strings.TrimSpace(string(f.runOutput)), "\n") {
				spec.Stream(line)
			}
		}
		return f.runOutput, f.runErr
	}
	if spec.Stream != nil {
		spec.Stream("ok")
	}
	return []byte("ok"), nil
}

func (f *fakeRunner) Start(ctx context.Context, spec CommandSpec) (*exec.Cmd, error) {
	f.startCtxErr = ctx.Err()
	f.starts = append(f.starts, spec)
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &exec.Cmd{}, nil
}

func TestLifecycleManagerStartStop(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "auto",
		"RUNNER_HOST":            "127.0.0.1",
		"RUNNER_PORT":            "1",
	}, runner)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.starts) != 2 {
		t.Fatalf("starts = %v", runner.starts)
	}
	if got := runner.starts[0]; got.Dir != manager.configDir || !strings.Contains(strings.Join(got.Env, " "), "CREDIMI_RUNNER_CONFIG_DIR="+manager.configDir) {
		t.Fatalf("start spec = %#v", got)
	}
	if got := runner.starts[0]; !got.Detached || !strings.HasSuffix(got.LogPath, "runner.log") {
		t.Fatalf("host runner should be detached with runner log path, got %#v", got)
	}
	if got := runner.starts[1]; got.Name != "docker" || !strings.Contains(strings.Join(got.Args, " "), "logs -f") {
		t.Fatalf("log follower spec = %#v", got)
	}
	if len(runner.runs) != 5 || runner.runs[0].Name != "docker" || !strings.Contains(strings.Join(runner.runs[0].Args, " "), "rm -f -s runner tunnel_named") {
		t.Fatalf("stale cleanup runs = %v", runner.runs)
	}
	if runner.runs[1].Name != "docker" || !strings.Contains(strings.Join(runner.runs[1].Args, " "), "pull runner_host caddy tunnel") {
		t.Fatalf("pull run = %v", runner.runs)
	}
	if runner.runs[2].Name != "docker" || !strings.Contains(strings.Join(runner.runs[2].Args, " "), "up -d --pull never runner_host caddy tunnel") {
		t.Fatalf("runs = %v", runner.runs)
	}
	if status := manager.Status(context.Background()); !status.LastStartedAt.Equal(fakeTunnelStartedAt) {
		t.Fatalf("tunnel start time = %v, want %v", status.LastStartedAt, fakeTunnelStartedAt)
	}
	if _, err := os.Stat(filepath.Join(manager.configDir, "docker-compose.yaml")); err != nil {
		t.Fatalf("Start should write docker-compose.yaml: %v", err)
	}
	lifecycle, err := os.ReadFile(filepath.Join(manager.configDir, "lifecycle.jsonl"))
	if err != nil {
		t.Fatalf("Start should write lifecycle.jsonl: %v", err)
	}
	if !strings.Contains(string(lifecycle), `"event":"operation.started"`) || !strings.Contains(string(lifecycle), `"event":"operation.succeeded"`) {
		t.Fatalf("lifecycle log missing start events: %s", lifecycle)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleManagerLifecycleLogCanBeEmittedAndClosed(t *testing.T) {
	if (*LifecycleManager)(nil).Close() != nil {
		t.Fatal("nil manager close should succeed")
	}
	(*LifecycleManager)(nil).EmitLifecycle(lifecyclelog.Event{Event: "ignored"})
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{}, &fakeRunner{})
	manager.EmitLifecycle(lifecyclelog.Event{Level: lifecyclelog.LevelInfo, Event: "controller.observed", Message: "observed"})
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := os.ReadFile(filepath.Join(manager.configDir, "lifecycle.jsonl"))
	if err != nil || !strings.Contains(string(lifecycle), "controller.observed") {
		t.Fatalf("lifecycle=%q err=%v", lifecycle, err)
	}
}

func TestLifecycleManagerVerboseLogCapturesLifecycleAndDockerProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "123-verbose.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(verboseLogPathEnv, path)
	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_TYPE":    "android_phone",
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "manual",
	}, runner)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output := string(raw)
	for _, want := range []string{"runtime start requested", "docker: ok"} {
		if !strings.Contains(output, want) {
			t.Fatalf("verbose log missing %q:\n%s", want, output)
		}
	}
	if len(runner.starts) == 0 || runner.starts[len(runner.starts)-1].Output == nil {
		t.Fatalf("container log follower should write verbose output: %#v", runner.starts)
	}
}

func TestLifecycleManagerStartDetachesHostRunnerFromCallerContext(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
		"RUNNER_PORT":            "1",
	}, runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if runner.startCtxErr != nil {
		t.Fatalf("host runner start context should be detached from caller cancellation, got %v", runner.startCtxErr)
	}
	if len(runner.starts) != 1 {
		t.Fatalf("starts = %v", runner.starts)
	}
}

func TestLifecycleManagerStartReusesReachableHostRunner(t *testing.T) {
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" {
			http.NotFound(w, request)
			return
		}
		_, _ = w.Write([]byte(`{"service":"credimi-runner","runner_id":"acme/runner","boot_id":"test-boot"}`))
	}))
	defer listener.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(listener.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
		"RUNNER_HOST":            host,
		"RUNNER_PORT":            port,
	}, runner)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.starts) != 0 {
		t.Fatalf("existing reachable runner should be reused, starts = %#v", runner.starts)
	}
	if status := manager.Status(context.Background()); !status.RunnerRunning {
		t.Fatalf("status = %#v", status)
	}
}

func TestLifecycleManagerStartRejectsForeignReachableHostListener(t *testing.T) {
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"service":"other-service","boot_id":"test-boot"}`))
	}))
	defer listener.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(listener.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
		"RUNNER_HOST":            host,
		"RUNNER_PORT":            port,
	}, runner)
	err = manager.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot adopt existing host runner") {
		t.Fatalf("Start error = %v", err)
	}
	if len(runner.starts) != 0 {
		t.Fatalf("foreign listener must not start or adopt a runner, starts = %#v", runner.starts)
	}
}

func TestValidateReachableHostRunnerRejectsMismatchedReadiness(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		body    string
		values  Values
		message string
	}{
		{name: "not ready", status: http.StatusServiceUnavailable, body: `{"service":"credimi-runner","runner_id":"acme/runner","boot_id":"boot"}`, values: Values{"CREDIMI_RUNNER_ID": "acme/runner"}, message: "HTTP 503"},
		{name: "runner mismatch", status: http.StatusOK, body: `{"service":"credimi-runner","runner_id":"other","boot_id":"boot"}`, values: Values{"CREDIMI_RUNNER_ID": "acme/runner"}, message: "does not match"},
		{name: "serial mismatch", status: http.StatusOK, body: `{"service":"credimi-runner","boot_id":"boot","device_serial":"other"}`, values: Values{"CREDIMI_RUNNER_SERIAL": "device-1"}, message: "does not match"},
		{name: "offline device", status: http.StatusOK, body: `{"service":"credimi-runner","boot_id":"boot","device_state":"offline"}`, values: Values{}, message: "offline"},
		{name: "invalid JSON", status: http.StatusOK, body: `not JSON`, values: Values{}, message: "decode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/readyz" {
					http.NotFound(w, request)
					return
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			test.values["RUNNER_HOST"] = host
			test.values["RUNNER_PORT"] = port
			err = validateReachableHostRunner(context.Background(), test.values)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("validateReachableHostRunner error = %v", err)
			}
		})
	}
}

func TestObserveRuntimeReportsHostAndComposeFailures(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	host := observeRuntime(context.Background(), &fakeRunner{}, t.TempDir(), Values{
		"CREDIMI_RUNNER_BACKEND": DefaultHostBackend,
		"CREDIMI_SERVICE_MODE":   "manual",
		"RUNNER_HOST":            "127.0.0.1",
		"RUNNER_PORT":            port,
	})
	if !host.runnerRunning || host.err != nil || !host.deviceReady {
		t.Fatalf("host observation = %#v", host)
	}
	composeFailure := observeRuntime(context.Background(), &fakeRunner{runErr: errors.New("docker unavailable")}, t.TempDir(), Values{
		"CREDIMI_RUNNER_BACKEND": DefaultContainerBackend,
		"CREDIMI_SERVICE_MODE":   "manual",
	})
	if composeFailure.err == nil || !strings.Contains(composeFailure.err.Error(), "docker unavailable") {
		t.Fatalf("compose failure = %#v", composeFailure)
	}
}

func TestConfiguredDeviceReadyUsesADBState(t *testing.T) {
	dir := t.TempDir()
	adb := filepath.Join(dir, "adb")
	if err := os.WriteFile(adb, []byte("#!/bin/sh\nprintf 'device\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if !configuredDeviceReady(context.Background(), Values{"CREDIMI_RUNNER_SERIAL": "device-1"}) {
		t.Fatal("expected connected device")
	}
	if err := os.Remove(adb); err != nil {
		t.Fatal(err)
	}
	if configuredDeviceReady(context.Background(), Values{"CREDIMI_RUNNER_SERIAL": "device-1"}) {
		t.Fatal("missing adb should not be ready")
	}
}

func TestObserveRuntimeUsesComposeProjectAndRunnerService(t *testing.T) {
	runner := &fakeRunner{runOutput: []byte(`{"Service":"runner","State":"running"}` + "\n")}
	values := Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "manual",
	}
	observed := observeRuntime(context.Background(), runner, t.TempDir(), values)
	if !observed.composeRunning || !observed.runnerRunning || observed.err != nil {
		t.Fatalf("observed=%#v", observed)
	}
	if len(runner.runs) != 1 || !strings.Contains(strings.Join(runner.runs[0].Args, " "), "--project-name credimi-runner-") {
		t.Fatalf("compose observation args=%#v", runner.runs)
	}
}

func TestLifecycleManagerStartWithProgressStreamsComposePull(t *testing.T) {
	runner := &fakeRunner{runOutput: []byte("runner Pulling fs layer\nrunner Downloading 128MB\nrunner Pull complete\n")}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "auto",
	}, runner)
	var progress []string
	if err := manager.StartWithProgress(context.Background(), func(line string) {
		progress = append(progress, line)
	}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(progress, "\n")
	if !strings.Contains(joined, "Pulling Docker images") || !strings.Contains(joined, "runner Downloading 128MB") || !strings.Contains(joined, "Starting Docker services") {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestLifecycleManagerStartSkipsLocalRunnerPull(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":        "acme/runner",
		"CREDIMI_RUNNER_BACKEND":   "container",
		"CREDIMI_SERVICE_MODE":     "auto",
		"RUNNER_IMAGE":             "credimi-runner-phone:latest",
		"RUNNER_IMAGE_PULL_POLICY": "never",
	}, runner)
	var progress []string
	if err := manager.StartWithProgress(context.Background(), func(line string) {
		progress = append(progress, line)
	}); err != nil {
		t.Fatal(err)
	}
	commands := commandArgs(runner.runs)
	if strings.Contains(commands, "pull runner caddy tunnel") || !strings.Contains(commands, "pull caddy tunnel") {
		t.Fatalf("pull commands =\n%s", commands)
	}
	if !strings.Contains(commands, "up -d --pull never runner caddy tunnel") {
		t.Fatalf("up command =\n%s", commands)
	}
	if !strings.Contains(strings.Join(progress, "\n"), "local runner image without pulling") {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestLifecycleManagerUpgradeRunnerImageStreamsOrderedCycle(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "manual",
		"RUNNER_IMAGE":           "example.test/runner:latest",
	}, runner)
	var progress []string
	if err := manager.UpgradeRunnerImage(context.Background(), func(line string) {
		progress = append(progress, line)
	}); err != nil {
		t.Fatal(err)
	}
	commands := commandArgs(runner.runs)
	if strings.Contains(commands, "stop runner caddy") || strings.Contains(commands, "stop runner tunnel") {
		t.Fatalf("upgrade must keep network services online:\n%s", commands)
	}
	ordered := []string{
		"pull example.test/runner:latest",
		"stop runner",
		"rm -f -s runner",
		"up -d --pull never runner",
	}
	position := -1
	for _, command := range ordered {
		next := strings.Index(commands[position+1:], command)
		if next < 0 {
			t.Fatalf("command %q missing or out of order in:\n%s", command, commands)
		}
		position += next + 1
	}
	if joined := strings.Join(progress, "\n"); !strings.Contains(joined, "already current") || !strings.Contains(joined, "upgrade complete") {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestLifecycleManagerUpgradeRunnerImageRestartsQuickTunnel(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "auto",
		"RUNNER_IMAGE":           "example.test/runner:latest",
	}, runner)

	if err := manager.UpgradeRunnerImage(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	commands := commandArgs(runner.runs)
	if !strings.Contains(commands, "stop runner tunnel") {
		t.Fatalf("upgrade must restart the quick tunnel with the runner:\n%s", commands)
	}
	if strings.Contains(commands, "stop runner tunnel caddy") {
		t.Fatalf("upgrade must keep the reverse proxy online:\n%s", commands)
	}
	if got := manager.Status(context.Background()).PublicURL; got != "" {
		t.Fatalf("public URL = %q, want fresh tunnel discovery", got)
	}
}

func TestLifecycleManagerUpgradeRunnerImageDeletesOnlySupersededImage(t *testing.T) {
	runner := &imageVersionRunner{imageIDs: []string{"sha256:old", "sha256:new"}}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "manual",
		"RUNNER_IMAGE":           "example.test/runner:latest",
	}, runner)
	if err := manager.UpgradeRunnerImage(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	commands := commandArgs(runner.runs)
	if !strings.Contains(commands, "image rm -f sha256:old") || strings.Contains(commands, "image rm -f example.test/runner:latest") {
		t.Fatalf("commands = %s", commands)
	}
}

func TestLifecycleManagerUpgradeRunnerImageRejectsHostBackend(t *testing.T) {
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
		"RUNNER_IMAGE":           "example.test/runner:latest",
	}, &fakeRunner{})
	if err := manager.UpgradeRunnerImage(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "container backend") {
		t.Fatalf("error = %v", err)
	}
}

func TestLifecycleManagerUpgradeRunnerImageRejectsLocalPolicy(t *testing.T) {
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":        "acme/runner",
		"CREDIMI_RUNNER_BACKEND":   "container",
		"CREDIMI_SERVICE_MODE":     "manual",
		"RUNNER_IMAGE":             "credimi-runner-phone:latest",
		"RUNNER_IMAGE_PULL_POLICY": "never",
	}, &fakeRunner{})
	if err := manager.UpgradeRunnerImage(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "RUNNER_IMAGE_PULL_POLICY=never") {
		t.Fatalf("error = %v", err)
	}
}

func TestLifecycleManagerUpgradeRunnerImageReportsDockerStageErrors(t *testing.T) {
	for _, failAt := range []int{2, 3, 4} {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			runner := &failOnRunRunner{failAt: failAt}
			manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
				"CREDIMI_RUNNER_ID":      "acme/runner",
				"CREDIMI_RUNNER_BACKEND": "container",
				"CREDIMI_SERVICE_MODE":   "manual",
				"RUNNER_IMAGE":           "example.test/runner:latest",
			}, runner)
			err := manager.UpgradeRunnerImage(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), "docker command failed") {
				t.Fatalf("error = %v", err)
			}
			if status := manager.Status(context.Background()); !strings.Contains(status.LastError, "docker command failed") {
				t.Fatalf("status = %#v", status)
			}
		})
	}
}

func TestLifecycleManagerUpgradeRunnerImageRequiresImage(t *testing.T) {
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "manual",
	}, &fakeRunner{})
	if err := manager.UpgradeRunnerImage(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "RUNNER_IMAGE") {
		t.Fatalf("error = %v", err)
	}
	manager.setLastError(nil)
}

func TestExecRunnerRunStreamsProgress(t *testing.T) {
	if os.Getenv("CREDIMI_RUNNER_STREAM_HELPER") == "1" {
		fmt.Fprint(os.Stdout, "runner Pulling fs layer\rrunner Downloading 128MB\n")
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestExecRunnerRunStreamsProgress")
	cmd.Env = append(os.Environ(), "CREDIMI_RUNNER_STREAM_HELPER=1")
	var lines []string
	output, err := runStreaming(cmd, func(line string) {
		lines = append(lines, line)
	})
	if err != nil {
		t.Fatal(err)
	}
	joinedLines := strings.Join(lines, "\n")
	if !strings.Contains(joinedLines, "runner Pulling fs layer") ||
		!strings.Contains(joinedLines, "runner Downloading 128MB") {
		t.Fatalf("lines = %#v", lines)
	}
	if !strings.Contains(string(output), "runner Downloading 128MB") {
		t.Fatalf("output = %q", string(output))
	}
}

func TestStaleComposeServices(t *testing.T) {
	got := strings.Join(staleComposeServices([]string{"runner", "caddy", "tunnel"}), ",")
	if got != "runner_host,tunnel_named" {
		t.Fatalf("staleComposeServices = %q", got)
	}
	if got := staleComposeServices([]string{"runner", "runner_host", "caddy", "tunnel", "tunnel_named"}); len(got) != 0 {
		t.Fatalf("staleComposeServices all active = %#v", got)
	}
}

func TestRunnerReachabilityHelpers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	values := Values{"RUNNER_HOST": "0.0.0.0", "RUNNER_PORT": port}
	host, gotPort := runnerListenTarget(values)
	if host != "127.0.0.1" || gotPort != port {
		t.Fatalf("runnerListenTarget = %s:%s", host, gotPort)
	}
	if !runnerAddressReachable(values, time.Second) {
		t.Fatal("listener should be reachable")
	}
	if runnerAddressReachable(Values{"RUNNER_HOST": "127.0.0.1", "RUNNER_PORT": "1"}, 10*time.Millisecond) {
		t.Fatal("closed port should not be reachable")
	}
}

func TestLinuxSocketDiscoveryHelpers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("proc socket discovery is linux-only")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	inodes, err := listeningSocketInodes(uint16(portNumber))
	if err != nil {
		t.Fatal(err)
	}
	if len(inodes) == 0 {
		t.Fatal("expected listener socket inode")
	}
	for inode := range inodes {
		if _, err := pidForSocketInode(inode); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pidForSocketInode should ignore non-runner test process, got %v", err)
		}
		break
	}
	if _, err := listeningSocketInodes(1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed port inodes error = %v", err)
	}
}

func TestRunnerProcessDiscoveryBranches(t *testing.T) {
	if processRunning(os.Getpid()) != true {
		t.Fatal("current process should be running")
	}
	if processCommandMatches(os.Getpid()) {
		t.Fatal("test process should not match runner serve")
	}
	if _, err := processCommandLine(os.Getpid()); err != nil {
		t.Fatalf("processCommandLine current process error = %v", err)
	}
	if _, err := processCommandLineFromPS(os.Getpid()); err != nil {
		t.Fatalf("processCommandLineFromPS current process error = %v", err)
	}
	if err := stopPID(context.Background(), os.Getpid()); err == nil || !strings.Contains(err.Error(), "refusing to stop PID") {
		t.Fatalf("stopPID current process error = %v", err)
	}
	for _, port := range []string{"abc", "70000"} {
		if _, err := discoverRunnerPID(Values{"RUNNER_PORT": port}); err == nil {
			t.Fatalf("discoverRunnerPID(%s) should fail", port)
		}
	}
	if _, err := discoverRunnerPID(Values{"RUNNER_PORT": "1"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discoverRunnerPID missing error = %v", err)
	}
}

func TestStopStartedCommandTerminatesProcess(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	if err := stopStartedCommand(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if processRunning(cmd.Process.Pid) {
		t.Fatalf("process %d should have stopped", cmd.Process.Pid)
	}
}

func TestLifecycleManagerStopUsesBackendSourceOfTruth(t *testing.T) {
	hostRunner := &fakeRunner{}
	hostManager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
		"RUNNER_PORT":            "1",
	}, hostRunner)
	if err := hostManager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(hostRunner.runs) != 0 {
		t.Fatalf("host manual stop should not call docker: %#v", hostRunner.runs)
	}

	containerRunner := &fakeRunner{}
	containerManager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_BACKEND": DefaultContainerBackend,
		"CREDIMI_SERVICE_MODE":   "manual",
	}, containerRunner)
	if err := containerManager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(containerRunner.runs) != 2 || !strings.Contains(strings.Join(containerRunner.runs[0].Args, " "), "down --remove-orphans") || !strings.Contains(strings.Join(containerRunner.runs[1].Args, " "), "ps -q") {
		t.Fatalf("container stop runs = %#v", containerRunner.runs)
	}
}

func TestVerifyComposeStoppedRejectsRemainingContainer(t *testing.T) {
	runner := &fakeRunner{runOutput: []byte("0123456789abcdef\n")}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_BACKEND": DefaultContainerBackend,
		"CREDIMI_SERVICE_MODE":   "manual",
	}, runner)
	if err := manager.Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("Stop error = %v", err)
	}
}

func TestExecRunnerRunAndStart(t *testing.T) {
	runner := ExecRunner{}
	output, err := runner.Run(context.Background(), CommandSpec{
		Name: "sh",
		Args: []string{"-c", "printf ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "ok" {
		t.Fatalf("output = %q", output)
	}

	cmd, err := runner.Start(context.Background(), CommandSpec{
		Name:     "sh",
		Args:     []string{"-c", "printf detached"},
		Detached: true,
		LogPath:  filepath.Join(t.TempDir(), "runner.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleManagerHelpers(t *testing.T) {
	runner := &fakeRunner{runOutput: []byte("line-1\nline-2\n")}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID": "acme/runner",
		"RUNNER_IMAGE":      "ghcr.io/forkbombeu/credimi-runner-phone:latest",
	}, runner)

	manager.Configure(Values{"CREDIMI_RUNNER_ID": "acme/runner-2", "RUNNER_IMAGE": "ghcr.io/forkbombeu/credimi-runner-phone:latest"})
	status := manager.Status(context.Background())
	if !status.Configured {
		t.Fatal("expected configured status after Configure")
	}

	manager.SetPublicURL("https://runner.example")
	status = manager.Status(context.Background())
	if status.PublicURL != "https://runner.example" {
		t.Fatalf("public URL = %q", status.PublicURL)
	}

	lines, err := manager.Logs(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Message != "line-1" || lines[1].Message != "line-2" {
		t.Fatalf("logs = %#v", lines)
	}
	manager.status.LastStartedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := manager.TunnelLogs(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if args := runner.runs[len(runner.runs)-1].Args; len(args) == 0 || args[len(args)-1] != "tunnel" || !strings.Contains(strings.Join(args, " "), "--since 2026-01-02T03:04:05Z") {
		t.Fatalf("TunnelLogs args = %#v", args)
	}
	manager.Configure(Values{
		"CREDIMI_RUNNER_ID":      "acme/runner-2",
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "manual",
		"RUNNER_IMAGE":           "ghcr.io/forkbombeu/credimi-runner-phone:latest",
	})
	if err := manager.UpdateImage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if joined := commandArgs(runner.runs); !strings.Contains(joined, "pull ghcr.io/forkbombeu/credimi-runner-phone:latest") ||
		!strings.Contains(joined, "up -d --pull never runner") {
		t.Fatalf("runs = %#v", runner.runs)
	}

	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = manager.Status(context.Background())
	if status.PublicURL != "" {
		t.Fatalf("public URL should be cleared on stop, got %q", status.PublicURL)
	}
}

func TestMergeObservedRuntimeStatusPreservesRegisteredURL(t *testing.T) {
	observedAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	status := mergeObservedRuntimeStatus(RuntimeStatus{
		PublicURL: "https://fresh.trycloudflare.com",
		LastError: "previous error",
	}, observedRuntime{
		runnerRunning:  true,
		composeRunning: true,
		deviceReady:    true,
	}, observedAt)
	if status.PublicURL != "https://fresh.trycloudflare.com" {
		t.Fatalf("PublicURL = %q", status.PublicURL)
	}
	if !status.Observed || !status.ObservedAt.Equal(observedAt) || !status.RunnerRunning || !status.ComposeRunning || !status.DeviceReady {
		t.Fatalf("observed status = %#v", status)
	}
}

func commandArgs(specs []CommandSpec) string {
	var commands []string
	for _, spec := range specs {
		commands = append(commands, strings.Join(spec.Args, " "))
	}
	return strings.Join(commands, "\n")
}

func TestLifecycleManagerRestartAndStopWithoutCompose(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":      "acme/runner",
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
		"RUNNER_PORT":            "1",
	}, runner)
	if err := manager.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.starts) != 1 {
		t.Fatalf("starts = %#v", runner.starts)
	}
	runner.runs = nil
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.runs) != 0 {
		t.Fatalf("stop should skip docker compose for no-compose plan: %#v", runner.runs)
	}
}

func TestLifecycleManagerStopComposeFollowerNilDone(t *testing.T) {
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{}, &fakeRunner{})
	manager.logCmd = &exec.Cmd{}
	manager.stopComposeLogFollowerLocked()
	if manager.logCmd != nil || manager.logDone != nil {
		t.Fatalf("log follower not cleared: cmd=%v done=%v", manager.logCmd, manager.logDone)
	}
}

func TestComposeLogArgsIncludesSince(t *testing.T) {
	plan := RuntimePlan{EnvPath: "/tmp/.env", ComposePath: "/tmp/docker-compose.yaml"}
	args := composeLogArgs(plan, 50, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if !strings.Contains(strings.Join(args, " "), "--since 2026-01-02T03:04:05Z") {
		t.Fatalf("composeLogArgs missing --since: %#v", args)
	}
}

func TestComposeLogArgsCanIncludeHistoricalLogs(t *testing.T) {
	plan := RuntimePlan{EnvPath: "/tmp/.env", ComposePath: "/tmp/docker-compose.yaml"}
	args := composeLogArgs(plan, -1000, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--since") {
		t.Fatalf("historical composeLogArgs should omit --since: %#v", args)
	}
	if !strings.Contains(joined, "--tail 1000") {
		t.Fatalf("historical composeLogArgs tail = %#v", args)
	}
}
