package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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

	path := filepath.Join(projectRootDirForFS(), "pkg", "gen", "http", "openapi3-public.json")
	orig, err := os.ReadFile(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.WriteFile(path, orig, 0o644)
	})

	err = os.WriteFile(path, []byte(`{"openapi":"3.0.3","servers":[{"url":"/"}]}`), 0o644)
	require.NoError(t, err)

	h := withPublicOpenAPIServerURL(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodGet, "/docs/openapi3-public.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"url": "https://api.example.com"`)

	req = httptest.NewRequest(http.MethodGet, "/workers", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusTeapot, rec.Code)
}

func TestOpenAPIPublicNoOverrideWhenDomainDefault(t *testing.T) {
	t.Setenv("RUNNER_DOMAIN", ":80")

	h := withPublicOpenAPIServerURL(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/docs/openapi3-public.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}
