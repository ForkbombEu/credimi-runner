package server

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDocsMethods(t *testing.T) {
	srv := NewRunnerService(NewProcessStore(), nil)

	html, err := srv.Docs(context.Background())
	require.NoError(t, err)
	require.Contains(t, html, "<elements-api")

	openapi, err := srv.DocsOpenapi(context.Background())
	require.NoError(t, err)
	require.Contains(t, openapi, "swagger:")
}

func TestDocsMethodsOutsideRepositoryWorkingDirectory(t *testing.T) {
	srv := NewRunnerService(NewProcessStore(), nil)
	tmpDir := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	html, err := srv.Docs(context.Background())
	require.NoError(t, err)
	require.Contains(t, html, "<elements-api")

	openapi, err := srv.DocsOpenapi(context.Background())
	require.NoError(t, err)
	require.Contains(t, openapi, "swagger:")
}
