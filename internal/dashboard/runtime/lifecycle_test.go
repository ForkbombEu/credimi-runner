package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestCommandErrorPreservesDockerDiagnostics(t *testing.T) {
	err := commandError(CommandSpec{Name: "docker", Args: []string{"compose", "up"}}, []byte("Cannot connect to the Docker daemon\n"), errors.New("exit status 1"))
	if !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("command error = %v", err)
	}
}

func TestLifecycleComposeCommandsUseOneResolvedEnvironment(t *testing.T) {
	dir := t.TempDir()
	values := Values{"CREDIMI_SERVICE_MODE": "manual", "ANDROID_PULL_POLICY": "never"}
	runner := &fakeRunner{}
	manager := NewLifecycleManagerForOS("credimi-runner", dir, values, runner, "linux")
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	plan := BuildRuntimePlanForOS(dir, values, "linux")
	want := composeEnv(values, plan, "linux")
	composeCalls := 0
	for _, call := range runner.runs {
		if len(call.Args) == 0 || call.Args[0] != "compose" {
			continue
		}
		composeCalls++
		if !slices.Equal(call.Env, want) {
			t.Fatalf("Compose call %q received a different environment", strings.Join(call.Args, " "))
		}
	}
	if composeCalls < 2 {
		t.Fatalf("expected multiple Compose lifecycle calls, got %d: %#v", composeCalls, runner.runs)
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
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
		"CREDIMI_SERVICE_MODE": "manual",
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
	if got := strings.Join(runner.starts[len(runner.starts)-1].Args, " "); !strings.Contains(got, "--timestamps") {
		t.Fatalf("verbose container log follower should request timestamps: %s", got)
	}
}

func TestLifecycleManagerStartLogFollower(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_SERVICE_MODE": "manual",
	}, runner)
	manager.StartLogFollower()
	if len(runner.starts) != 1 {
		t.Fatalf("log follower starts = %#v", runner.starts)
	}
	if got := strings.Join(runner.starts[0].Args, " "); !strings.Contains(got, "logs -f --tail 80 runner") {
		t.Fatalf("log follower args = %s", got)
	}
}

func TestExecRunnerStreamsCommandOutputByLine(t *testing.T) {
	var streamed []string
	output, err := (ExecRunner{}).Run(context.Background(), CommandSpec{
		Name:   "sh",
		Args:   []string{"-c", "printf 'pulling\\rfinished\\n'"},
		Stream: func(line string) { streamed = append(streamed, line) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := streamed, []string{"pulling", "finished"}; !slices.Equal(got, want) {
		t.Fatalf("streamed lines = %q, want %q", got, want)
	}
	if got, want := string(output), "pulling\nfinished\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestLifecycleManagerStopLogFollowerWithoutProcess(t *testing.T) {
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{}, &fakeRunner{})
	manager.logCmd = &exec.Cmd{}
	manager.logDone = make(chan struct{})
	manager.stopComposeLogFollowerLocked()
	if manager.logCmd != nil || manager.logDone != nil {
		t.Fatalf("log follower was not cleared: %#v", manager)
	}
}

func TestObserveRuntimeReportsNativeApplicationAndComposeFailures(t *testing.T) {
	native := observeRuntimeForOS(context.Background(), &fakeRunner{}, t.TempDir(), Values{
		"CREDIMI_RUNNER_TYPE":  "ios_simulator",
		"CREDIMI_SERVICE_MODE": "manual",
	}, "darwin")
	if native.runnerRunning || native.err != nil || !native.deviceReady {
		t.Fatalf("native observation = %#v", native)
	}
	composeFailure := observeRuntime(context.Background(), &fakeRunner{runErr: errors.New("docker unavailable")}, t.TempDir(), Values{
		"CREDIMI_SERVICE_MODE": "manual",
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
	values := Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner/device-1", "CREDIMI_DEVICE_1_TYPE": "android_phone", "CREDIMI_DEVICE_1_MODE": "usb", "CREDIMI_DEVICE_1_SERIAL": "device-1"}
	if !configuredDeviceReady(context.Background(), values) {
		t.Fatal("expected connected device")
	}
	if err := os.Remove(adb); err != nil {
		t.Fatal(err)
	}
	if configuredDeviceReady(context.Background(), values) {
		t.Fatal("missing adb should not be ready")
	}
}

func TestObserveRuntimeUsesComposeProjectAndRunnerService(t *testing.T) {
	runner := &fakeRunner{runOutput: []byte(`{"Service":"runner","State":"running"}` + "\n")}
	values := Values{
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_SERVICE_MODE": "manual",
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
	manager := NewLifecycleManagerForOS("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_RUNNER_TYPE":  "ios_simulator",
		"CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
		"CREDIMI_SERVICE_MODE": "auto",
	}, runner, "darwin")
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

func TestLifecycleManagerNativeStartsOnlyEdgeServices(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewLifecycleManagerForOS("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
		"CREDIMI_SERVICE_MODE": "auto",
		"ANDROID_RUNNER_IMAGE": "credimi-runner:local",
		"ANDROID_PULL_POLICY":  "never",
	}, runner, "darwin")
	var progress []string
	if err := manager.StartWithProgress(context.Background(), func(line string) {
		progress = append(progress, line)
	}); err != nil {
		t.Fatal(err)
	}
	commands := commandArgs(runner.runs)
	if strings.Contains(commands, " up -d --pull never runner") || !strings.Contains(commands, "pull caddy tunnel") {
		t.Fatalf("pull commands =\n%s", commands)
	}
	if !strings.Contains(commands, "up -d --pull never caddy tunnel") {
		t.Fatalf("up command =\n%s", commands)
	}
	_ = progress
}

func TestLifecycleManagerUpgradeRunnerImageStreamsOrderedCycle(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
		"CREDIMI_SERVICE_MODE": "manual",
		"ANDROID_RUNNER_IMAGE": "example.test/runner:latest",
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
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
		"CREDIMI_SERVICE_MODE": "auto",
		"ANDROID_RUNNER_IMAGE": "example.test/runner:latest",
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
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
		"CREDIMI_SERVICE_MODE": "manual",
		"ANDROID_RUNNER_IMAGE": "example.test/runner:latest",
	}, runner)
	if err := manager.UpgradeRunnerImage(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	commands := commandArgs(runner.runs)
	if !strings.Contains(commands, "image rm -f sha256:old") || strings.Contains(commands, "image rm -f example.test/runner:latest") {
		t.Fatalf("commands = %s", commands)
	}
}

func TestLifecycleManagerUpgradeRunnerImagePullsSharedDeviceImage(t *testing.T) {
	runner := &imageVersionRunner{imageIDs: []string{"sha256:old", "sha256:new"}}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_SERVICE_MODE": "manual", "CREDIMI_DEVICE_COUNT": "2",
		"CREDIMI_DEVICE_1_ID": "acme/runner/phone", "CREDIMI_DEVICE_2_ID": "acme/runner/emulator", "ANDROID_RUNNER_IMAGE": "example.test/shared:latest",
	}, runner)
	if err := manager.UpgradeRunnerImage(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	commands := commandArgs(runner.runs)
	for _, expected := range []string{"pull example.test/shared:latest", "image rm -f sha256:old"} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("missing %q in %s", expected, commands)
		}
	}
}

func TestLifecycleManagerUpgradeRunnerImageRejectsLocalPolicy(t *testing.T) {
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
		"CREDIMI_SERVICE_MODE": "manual",
		"ANDROID_RUNNER_IMAGE": "credimi-runner:local",
		"ANDROID_PULL_POLICY":  "never",
	}, &fakeRunner{})
	if err := manager.UpgradeRunnerImage(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("error = %v", err)
	}
}

func TestLifecycleManagerUpgradeRunnerImageReportsDockerStageErrors(t *testing.T) {
	for _, failAt := range []int{2, 3, 4} {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			runner := &failOnRunRunner{failAt: failAt}
			manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
				"CREDIMI_RUNNER_ID":    "acme/runner",
				"CREDIMI_DEVICE_COUNT": "1",
				"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
				"CREDIMI_SERVICE_MODE": "manual",
				"ANDROID_RUNNER_IMAGE": "example.test/runner:latest",
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

func TestLifecycleManagerUpgradeRunnerImageUsesDefaultSharedImage(t *testing.T) {
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_SERVICE_MODE": "manual",
		"CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
	}, &fakeRunner{})
	if err := manager.UpgradeRunnerImage(context.Background(), nil); err != nil {
		t.Fatalf("upgrade default image: %v", err)
	}
	manager.setLastError(nil)
}

func TestConfiguredRuntimeImagesUsesOneSharedImage(t *testing.T) {
	values := Values{
		"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "2",
		"CREDIMI_DEVICE_1_ID": "acme/runner/phone", "ANDROID_RUNNER_IMAGE": "shared:v1", "ANDROID_PULL_POLICY": "always",
		"CREDIMI_DEVICE_2_ID": "acme/runner/emulator",
	}
	if got := configuredRuntimeImages(values, "linux"); !slices.Equal(got, []string{"shared:v1"}) {
		t.Fatalf("configuredRuntimeImages = %v", got)
	}
	values["ANDROID_PULL_POLICY"] = "never"
	if got := configuredRuntimeImages(values, "linux"); len(got) != 0 {
		t.Fatalf("local shared image = %v", got)
	}
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
	if got != "tunnel_named" {
		t.Fatalf("staleComposeServices = %q", got)
	}
	if got := staleComposeServices([]string{"runner", "caddy", "tunnel", "tunnel_named"}); len(got) != 0 {
		t.Fatalf("staleComposeServices all active = %#v", got)
	}
}

func TestLifecycleManagerStatusObservesComposeRuntimeWithExecRunner(t *testing.T) {
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nprintf '%s\\n' '{\"Service\":\"runner\",\"State\":\"running\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_SERVICE_MODE": "manual",
	}, ExecRunner{})
	manager.SetPublicURL("https://runner.example")

	status := manager.Status(context.Background())
	if !status.Observed || !status.ComposeRunning || !status.RunnerRunning || !status.DeviceReady {
		t.Fatalf("observed status = %#v", status)
	}
	if status.ObservedAt.IsZero() || status.PublicURL != "https://runner.example" {
		t.Fatalf("observation lost metadata: %#v", status)
	}
}

func TestLifecycleManagerStartReportsDockerStageFailures(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			runner := &failOnRunRunner{failAt: failAt}
			manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
				"CREDIMI_RUNNER_ID":    "acme/runner",
				"CREDIMI_DEVICE_COUNT": "1",
				"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
				"CREDIMI_SERVICE_MODE": "manual",
			}, runner)
			err := manager.Start(context.Background())
			if err == nil || !strings.Contains(err.Error(), "docker command failed") {
				t.Fatalf("Start error = %v", err)
			}
			if status := manager.Status(context.Background()); !strings.Contains(status.LastError, "docker command failed") {
				t.Fatalf("failure status = %#v", status)
			}
		})
	}
}

func TestVerifyComposeStoppedRejectsRemainingContainer(t *testing.T) {
	runner := &fakeRunner{runOutput: []byte("0123456789abcdef\n")}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{
		"CREDIMI_SERVICE_MODE": "manual",
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
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID":  "acme/runner/device",
		"ANDROID_RUNNER_IMAGE": "ghcr.io/forkbombeu/credimi-runner:local",
	}, runner)

	manager.Configure(Values{"CREDIMI_RUNNER_ID": "acme/runner-2", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner-2/device", "ANDROID_RUNNER_IMAGE": "ghcr.io/forkbombeu/credimi-runner:local"})
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
	manager.Configure(Values{
		"CREDIMI_RUNNER_ID":    "acme/runner-2",
		"CREDIMI_DEVICE_COUNT": "1",
		"CREDIMI_DEVICE_1_ID":  "acme/runner-2/device",
		"CREDIMI_SERVICE_MODE": "manual",
		"ANDROID_RUNNER_IMAGE": "ghcr.io/forkbombeu/credimi-runner:local",
	})
	if err := manager.UpdateImage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if joined := commandArgs(runner.runs); !strings.Contains(joined, "pull ghcr.io/forkbombeu/credimi-runner:local") ||
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
	manager := NewLifecycleManagerForOS("credimi-runner", t.TempDir(), Values{
		"CREDIMI_RUNNER_ID":    "acme/runner",
		"CREDIMI_RUNNER_TYPE":  "ios_simulator",
		"CREDIMI_SERVICE_MODE": "manual",
		"RUNNER_PORT":          "1",
	}, runner, "darwin")
	if err := manager.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.starts) != 0 {
		t.Fatalf("native application must not be started by lifecycle manager: %#v", runner.starts)
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
	plan := RuntimePlan{EnvPath: "/tmp/config.toml", ComposePath: "/tmp/docker-compose.yaml"}
	args := composeLogArgs(plan, 50, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if !strings.Contains(strings.Join(args, " "), "--since 2026-01-02T03:04:05Z") {
		t.Fatalf("composeLogArgs missing --since: %#v", args)
	}
}

func TestComposeLogArgsCanIncludeHistoricalLogs(t *testing.T) {
	plan := RuntimePlan{EnvPath: "/tmp/config.toml", ComposePath: "/tmp/docker-compose.yaml"}
	args := composeLogArgs(plan, -1000, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--since") {
		t.Fatalf("historical composeLogArgs should omit --since: %#v", args)
	}
	if !strings.Contains(joined, "--tail 1000") {
		t.Fatalf("historical composeLogArgs tail = %#v", args)
	}
}

func TestLifecycleCommandParsingAndContainerIDValidation(t *testing.T) {
	for _, value := range []string{"0123456789abcdef", strings.Repeat("a", 64)} {
		if !looksLikeContainerID(value) {
			t.Fatalf("valid container ID rejected: %q", value)
		}
	}
	for _, value := range []string{"", "short", strings.Repeat("g", 64), strings.Repeat("a", 65)} {
		if looksLikeContainerID(value) {
			t.Fatalf("invalid container ID accepted: %q", value)
		}
	}
	if got, _, err := scanDockerProgress([]byte("pulling\rnext"), false); got != 8 || err != nil {
		t.Fatalf("scanDockerProgress carriage return = %d, %v", got, err)
	}
	if got, token, err := scanDockerProgress([]byte("done"), true); got != 4 || string(token) != "done" || err != nil {
		t.Fatalf("scanDockerProgress EOF = %d %q, %v", got, token, err)
	}
}

func TestLifecycleManagerConfigurationStatusAndLogs(t *testing.T) {
	runner := &fakeRunner{runOutput: []byte("first\n\nsecond\n")}
	manager := NewLifecycleManager("credimi-runner", t.TempDir(), Values{"CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner/device"}, runner)
	manager.Configure(Values{"CREDIMI_RUNNER_ID": "acme/runner-2", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "acme/runner-2/device"})
	manager.SetPublicURL("https://runner.example")
	if status := manager.Status(context.Background()); !status.Configured || status.PublicURL != "https://runner.example" {
		t.Fatalf("configured status = %#v", status)
	}
	logs, err := manager.Logs(context.Background(), 20)
	if err != nil || len(logs) != 2 || logs[1].Message != "second" {
		t.Fatalf("logs = %#v err=%v", logs, err)
	}
	manager.EmitLifecycle(lifecyclelog.Event{Event: "test"})
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestParseQuickTunnelHostname(t *testing.T) {
	if got, err := ParseQuickTunnelHostname("abc.trycloudflare.com"); err != nil || got != "https://abc.trycloudflare.com" {
		t.Fatalf("quick tunnel URL = %q, err=%v", got, err)
	}
	for _, hostname := range []string{"", "https://abc.trycloudflare.com", "abc/path"} {
		if _, err := ParseQuickTunnelHostname(hostname); err == nil {
			t.Fatalf("invalid hostname %q was accepted", hostname)
		}
	}
}

func TestLifecycleManagerReadsStructuredQuickTunnelEndpoint(t *testing.T) {
	previous := quickTunnelHTTPClient
	quickTunnelHTTPClient = httpClientFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"hostname":"abc.trycloudflare.com"}`)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { quickTunnelHTTPClient = previous })
	manager := NewLifecycleManagerForOS("", t.TempDir(), Values{"CREDIMI_SERVICE_MODE": "auto"}, &fakeRunner{}, "linux")
	got, err := manager.QuickTunnelURL(context.Background())
	if err != nil || got != "https://abc.trycloudflare.com" {
		t.Fatalf("structured quick tunnel URL = %q, err=%v", got, err)
	}
}

func TestLifecycleManagerReportsStructuredQuickTunnelFailures(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		status     string
		body       string
		clientErr  error
		want       string
	}{
		{name: "transport", clientErr: errors.New("metrics endpoint unavailable"), want: "query quick tunnel diagnostics"},
		{name: "http status", statusCode: http.StatusServiceUnavailable, status: "503 Service Unavailable", body: `{}`, want: "503 Service Unavailable"},
		{name: "malformed json", statusCode: http.StatusOK, status: "200 OK", body: `{`, want: "decode quick tunnel diagnostics"},
		{name: "hostname not ready", statusCode: http.StatusOK, status: "200 OK", body: `{"hostname":""}`, want: "hostname is not available yet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previous := quickTunnelHTTPClient
			quickTunnelHTTPClient = httpClientFunc(func(*http.Request) (*http.Response, error) {
				if tc.clientErr != nil {
					return nil, tc.clientErr
				}
				return &http.Response{
					StatusCode: tc.statusCode,
					Status:     tc.status,
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Header:     make(http.Header),
				}, nil
			})
			t.Cleanup(func() { quickTunnelHTTPClient = previous })
			manager := NewLifecycleManagerForOS("", t.TempDir(), Values{"CREDIMI_SERVICE_MODE": "auto"}, &fakeRunner{}, "linux")
			_, err := manager.QuickTunnelURL(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("quick tunnel error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (f httpClientFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }
