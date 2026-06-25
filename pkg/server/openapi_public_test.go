package server

import (
	"context"
	"io"
	"testing"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestResolvePublicServerURL(t *testing.T) {
	t.Setenv("RUNNER_DOMAIN", "")
	_, ok := resolvePublicServerURL()
	require.False(t, ok)

	t.Setenv("RUNNER_DOMAIN", ":80")
	_, ok = resolvePublicServerURL()
	require.False(t, ok)

	t.Setenv("RUNNER_DOMAIN", "api.example.com")
	url, ok := resolvePublicServerURL()
	require.True(t, ok)
	require.Equal(t, "https://api.example.com", url)

	t.Setenv("RUNNER_DOMAIN", "http://127.0.0.1:8050")
	url, ok = resolvePublicServerURL()
	require.True(t, ok)
	require.Equal(t, "http://127.0.0.1:8050", url)
}

func TestOpenAPIPublicServersOverride(t *testing.T) {
	t.Setenv("RUNNER_DOMAIN", "api.example.com")

	content, err := readOpenAPI3PublicContent()
	require.NoError(t, err)
	require.Contains(t, string(content), `"servers": [`)
	require.Contains(t, string(content), `"url": "https://api.example.com"`)
}

func TestOpenAPIPublicNoOverrideWhenDomainDefault(t *testing.T) {
	t.Setenv("RUNNER_DOMAIN", ":80")

	content, err := readOpenAPI3PublicContent()
	require.NoError(t, err)
	require.Contains(t, string(content), `"url": "/"`)
}

func TestOpenapi3Public_Method(t *testing.T) {
	t.Setenv("RUNNER_DOMAIN", "api.example.com")

	srv := NewRunnerService(NewProcessStore(), utils.Instance{})
	result, body, err := srv.Openapi3Public(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = body.Close() })

	content, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "application/json; charset=utf-8", result.Encoding)
	require.Equal(t, int64(len(content)), result.Length)
	require.Contains(t, string(content), `"url": "https://api.example.com"`)
}
