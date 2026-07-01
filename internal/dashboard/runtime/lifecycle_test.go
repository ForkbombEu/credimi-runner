package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
		"RUNNER_PORT":            "8050",
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
	if len(runner.runs) != 1 || runner.runs[0].Name != "docker" || !strings.Contains(strings.Join(runner.runs[0].Args, " "), "compose") {
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
