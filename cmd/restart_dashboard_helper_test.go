package cmd

import (
	"errors"
	"testing"
)

func TestRestartDashboardHelperInstallsExplicitStagedBinary(t *testing.T) {
	originalPID, originalTarget, originalStaged, originalArgs := restartWaitPID, restartTarget, restartStaged, restartArgs
	originalRename, originalStart := restartRename, restartStart
	t.Cleanup(func() {
		restartWaitPID, restartTarget, restartStaged, restartArgs = originalPID, originalTarget, originalStaged, originalArgs
		restartRename, restartStart = originalRename, originalStart
	})
	restartWaitPID = 999999999
	restartTarget = "/installed/credimi-runner"
	restartStaged = "/tmp/credimi-runner.upgrade"
	restartArgs = []string{"--config-dir", "/tmp/config"}
	var renamedFrom, renamedTo, started string
	restartRename = func(from, to string) error { renamedFrom, renamedTo = from, to; return nil }
	restartStart = func(target string, _ []string) error { started = target; return nil }
	if err := runRestartDashboardHelper(nil, nil); err != nil {
		t.Fatal(err)
	}
	if renamedFrom != restartStaged || renamedTo != restartTarget || started != restartTarget {
		t.Fatalf("rename %q -> %q, start %q", renamedFrom, renamedTo, started)
	}
}

func TestRestartDashboardHelperReportsInvalidInputsAndInstallFailures(t *testing.T) {
	originalPID, originalTarget, originalStaged := restartWaitPID, restartTarget, restartStaged
	originalRename, originalStart := restartRename, restartStart
	t.Cleanup(func() {
		restartWaitPID, restartTarget, restartStaged = originalPID, originalTarget, originalStaged
		restartRename, restartStart = originalRename, originalStart
	})

	restartWaitPID, restartTarget, restartStaged = 0, "", ""
	if err := runRestartDashboardHelper(nil, nil); err == nil {
		t.Fatal("invalid helper inputs were accepted")
	}

	restartWaitPID, restartTarget, restartStaged = 999999999, "/installed/credimi-runner", "/tmp/credimi-runner.upgrade"
	restartRename = func(string, string) error { return errors.New("rename failed") }
	if err := runRestartDashboardHelper(nil, nil); err == nil {
		t.Fatal("rename failure was not reported")
	}

	restartRename = func(string, string) error { return nil }
	restartStart = func(string, []string) error { return errors.New("start failed") }
	if err := runRestartDashboardHelper(nil, nil); err == nil {
		t.Fatal("restart failure was not reported")
	}
}
