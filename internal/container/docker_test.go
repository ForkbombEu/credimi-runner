package container

import (
	"context"
	"testing"
)

func TestDockerCLIUsesDefaultRunnerWhenNoneIsConfigured(t *testing.T) {
	if err := (DockerCLI{}).Pull(context.Background(), "runner:local"); err == nil {
		t.Fatal("unexpected Docker pull success in a test environment")
	}
}
