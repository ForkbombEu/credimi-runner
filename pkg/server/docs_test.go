package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectRootDir(t *testing.T) {
	root := projectRootDir()
	require.NotEmpty(t, root)
	require.FileExists(t, filepath.Join(root, "pkg", "server", "docs", "index.html"))
	require.FileExists(t, filepath.Join(root, "pkg", "gen", "http", "openapi.yaml"))
}

func TestDocsMethods(t *testing.T) {
	srv := NewRunnerService(NewProcessStore(), nil)

	html, err := srv.Docs(context.Background())
	require.NoError(t, err)
	require.Contains(t, html, "<elements-api")

	openapi, err := srv.DocsOpenapi(context.Background())
	require.NoError(t, err)
	require.Contains(t, openapi, "swagger:")
}
