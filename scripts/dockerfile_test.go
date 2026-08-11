package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDockerfileBuildsOnlyTheRunnerImage(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	require.NoError(t, err)
	dockerfile := string(content)
	require.Contains(t, dockerfile, "FROM ubuntu:24.04")
	require.Contains(t, dockerfile, "ENTRYPOINT [\"/usr/local/bin/credimi-runner\"]")
	require.NotContains(t, dockerfile, "AS agent")
	require.NotContains(t, dockerfile, "agent-config.json")
	require.Contains(t, dockerfile, `"build-tools;35.0.0"`)
	require.Contains(t, dockerfile, "/usr/local/bin/aapt2")
}
