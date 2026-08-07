package driver

import (
	"context"
	"testing"
)

func TestExecRunnerRunsCommand(t *testing.T) {
	if output, err := (ExecRunner{}).Run(context.Background(), "true"); err != nil || len(output) != 0 {
		t.Fatalf("ExecRunner output=%q err=%v", output, err)
	}
}
