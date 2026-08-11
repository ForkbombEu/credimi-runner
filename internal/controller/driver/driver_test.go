package driver

import (
	"context"
	"os"
	"testing"
)

func TestExecRunnerRunsCommand(t *testing.T) {
	if output, err := (ExecRunner{}).Run(context.Background(), "true"); err != nil || len(output) != 0 {
		t.Fatalf("ExecRunner output=%q err=%v", output, err)
	}
}

func TestExecRunnerRunsCommandWithExplicitEnvironment(t *testing.T) {
	coverageDir := t.TempDir()
	output, err := (ExecRunner{}).RunWithEnv(
		context.Background(),
		[]string{"GO_WANT_HELPER_PROCESS=1", "CREDIMI_DRIVER_TEST=expected", "GOCOVERDIR=" + coverageDir},
		os.Args[0],
		"-test.run=TestExecRunnerEnvironmentHelper",
		"--",
	)
	if err != nil {
		t.Fatalf("ExecRunner output=%q err=%v", output, err)
	}
	if string(output) != "expected" {
		t.Fatalf("ExecRunner output=%q, want %q", output, "expected")
	}
}

func TestExecRunnerEnvironmentHelper(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	if got := os.Getenv("CREDIMI_DRIVER_TEST"); got != "expected" {
		t.Fatalf("CREDIMI_DRIVER_TEST=%q, want %q", got, "expected")
	}
	_, _ = os.Stdout.WriteString(os.Getenv("CREDIMI_DRIVER_TEST"))
	os.Exit(0)
}
