package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	runs        []CommandSpec
	starts      []CommandSpec
	startCtxErr error
	runOutput   []byte
	runErr      error
	startErr    error
}

func (f *fakeRunner) Run(_ context.Context, spec CommandSpec) ([]byte, error) {
	f.runs = append(f.runs, spec)
	if f.runOutput != nil || f.runErr != nil {
		return f.runOutput, f.runErr
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
	if len(runner.runs) != 2 || runner.runs[0].Name != "docker" || !strings.Contains(strings.Join(runner.runs[0].Args, " "), "rm -f -s runner tunnel_named") {
		t.Fatalf("stale cleanup runs = %v", runner.runs)
	}
	if runner.runs[1].Name != "docker" || !strings.Contains(strings.Join(runner.runs[1].Args, " "), "compose") {
		t.Fatalf("runs = %v", runner.runs)
	}
	if _, err := os.Stat(filepath.Join(manager.configDir, "docker-compose.yaml")); err != nil {
		t.Fatalf("Start should write docker-compose.yaml: %v", err)
	}
	if err := manager.Down(context.Background()); err != nil {
		t.Fatal(err)
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, port, ok := strings.Cut(listener.Addr().String(), ":")
	if !ok {
		t.Fatalf("listener address = %q", listener.Addr().String())
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
	if len(containerRunner.runs) != 1 || !strings.Contains(strings.Join(containerRunner.runs[0].Args, " "), "stop runner") {
		t.Fatalf("container stop runs = %#v", containerRunner.runs)
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
	if err := manager.UpdateImage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.runs) == 0 || runner.runs[len(runner.runs)-1].Args[0] != "pull" {
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

func TestLifecycleManagerRestartAndDownWithoutCompose(t *testing.T) {
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
	if err := manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.runs) != 0 {
		t.Fatalf("down should skip docker compose for no-compose plan: %#v", runner.runs)
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
