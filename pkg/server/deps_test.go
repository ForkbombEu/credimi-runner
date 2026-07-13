package server

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedWorkflowRootFromEnvironment(t *testing.T) {
	t.Run("uses configured temp directory", func(t *testing.T) {
		t.Setenv("CREDIMI_DIR", "")
		t.Setenv("CREDIMI_TEMP_DIR", "/tmp/credimi-runner-tmp/")
		deps := Deps{}

		deps.WithDefaults()

		require.Equal(t, "/tmp/credimi-runner-tmp/workflows", deps.ManagedWorkflowRoot)
	})

	t.Run("matches credimi directory precedence", func(t *testing.T) {
		t.Setenv("CREDIMI_DIR", "/custom/credimi")
		t.Setenv("CREDIMI_TEMP_DIR", "/tmp/credimi-runner-tmp")

		require.Equal(t, "/custom/credimi/workflows", managedWorkflowRootFromEnvironment())
	})

	t.Run("keeps docker default", func(t *testing.T) {
		t.Setenv("CREDIMI_DIR", "")
		t.Setenv("CREDIMI_TEMP_DIR", " ")

		require.Equal(t, filepath.Join(defaultCredimiRoot, "workflows"), managedWorkflowRootFromEnvironment())
	})

	t.Run("does not override injected root", func(t *testing.T) {
		t.Setenv("CREDIMI_TEMP_DIR", "/tmp/credimi-runner-tmp")
		deps := Deps{ManagedWorkflowRoot: "test-workflows"}

		deps.WithDefaults()

		require.Equal(t, "test-workflows", deps.ManagedWorkflowRoot)
	})
}
