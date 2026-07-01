package server

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestDocsMethods(t *testing.T) {
	srv := NewRunnerService(NewProcessStore(), utils.Instance{})

	html, err := srv.Docs(context.Background())
	require.NoError(t, err)
	require.Contains(t, html, "<elements-api")

	openapi, err := srv.DocsOpenapi(context.Background())
	require.NoError(t, err)
	require.Contains(t, openapi, "swagger:")
}

func TestDocsMethodsOutsideRepositoryWorkingDirectory(t *testing.T) {
	srv := NewRunnerService(NewProcessStore(), utils.Instance{})
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

func TestDocsAssetEndpoints(t *testing.T) {
	srv := NewRunnerService(NewProcessStore(), utils.Instance{})

	openapiRes, openapiBody, err := srv.Openapi(context.Background())
	require.NoError(t, err)
	require.Equal(t, "application/yaml; charset=utf-8", openapiRes.Encoding)
	defer openapiBody.Close()
	openapiContent, err := io.ReadAll(openapiBody)
	require.NoError(t, err)
	require.NotEmpty(t, openapiContent)

	openapi3Res, openapi3Body, err := srv.Openapi3(context.Background())
	require.NoError(t, err)
	require.Equal(t, "application/yaml; charset=utf-8", openapi3Res.Encoding)
	defer openapi3Body.Close()
	openapi3Content, err := io.ReadAll(openapi3Body)
	require.NoError(t, err)
	require.NotEmpty(t, openapi3Content)

	openapi3PublicRes, openapi3PublicBody, err := srv.Openapi3Public(context.Background())
	require.NoError(t, err)
	require.Equal(t, "application/json; charset=utf-8", openapi3PublicRes.Encoding)
	defer openapi3PublicBody.Close()
	openapi3PublicContent, err := io.ReadAll(openapi3PublicBody)
	require.NoError(t, err)
	require.NotEmpty(t, openapi3PublicContent)

	indexRes, indexBody, err := srv.Index(context.Background())
	require.NoError(t, err)
	require.Equal(t, "text/html; charset=utf-8", indexRes.Encoding)
	defer indexBody.Close()
	indexContent, err := io.ReadAll(indexBody)
	require.NoError(t, err)
	require.Contains(t, string(indexContent), "<elements-api")
}
