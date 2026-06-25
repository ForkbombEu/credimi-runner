package runtime

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

type fakeRunner struct {
	runs   []CommandSpec
	starts []CommandSpec
}

func (f *fakeRunner) Run(_ context.Context, spec CommandSpec) ([]byte, error) {
	f.runs = append(f.runs, spec)
	return []byte("ok"), nil
}

func (f *fakeRunner) Start(_ context.Context, spec CommandSpec) (*exec.Cmd, error) {
	f.starts = append(f.starts, spec)
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
	if len(runner.starts) != 1 {
		t.Fatalf("starts = %v", runner.starts)
	}
	if got := runner.starts[0]; got.Dir != manager.configDir || !strings.Contains(strings.Join(got.Env, " "), "CREDIMI_RUNNER_CONFIG_DIR="+manager.configDir) {
		t.Fatalf("start spec = %#v", got)
	}
	if len(runner.runs) != 1 || runner.runs[0].Name != "docker" || !strings.Contains(strings.Join(runner.runs[0].Args, " "), "compose") {
		t.Fatalf("runs = %v", runner.runs)
	}
	if err := manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
}
