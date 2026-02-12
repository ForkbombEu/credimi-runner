package server

import (
	"context"
	"os"
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

func TestDocsMethodsErrors(t *testing.T) {
	srv := NewRunnerService(NewProcessStore(), nil)

	t.Run("docs returns error when index is missing", func(t *testing.T) {
		root := projectRootDir()
		indexPath := filepath.Join(root, "pkg", "server", "docs", "index.html")
		backupPath := indexPath + ".bak-test"

		require.NoError(t, os.Rename(indexPath, backupPath))
		t.Cleanup(func() {
			require.NoError(t, os.Rename(backupPath, indexPath))
		})

		_, err := srv.Docs(context.Background())
		require.Error(t, err)
	})

	t.Run("docs openapi returns error when spec is missing", func(t *testing.T) {
		root := projectRootDir()
		openapiPath := filepath.Join(root, "pkg", "gen", "http", "openapi.yaml")
		backupPath := openapiPath + ".bak-test"

		require.NoError(t, os.Rename(openapiPath, backupPath))
		t.Cleanup(func() {
			require.NoError(t, os.Rename(backupPath, openapiPath))
		})

		_, err := srv.DocsOpenapi(context.Background())
		require.Error(t, err)
	})
}
