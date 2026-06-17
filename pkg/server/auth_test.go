package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthConfiguredUserKey(t *testing.T) {
	srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{
		URL:        "http://example.local",
		UserAPIKey: "configured-user-key",
	}, Deps{})

	_, err := srv.APIKeyAuth(context.Background(), "configured-user-key", nil)
	require.NoError(t, err)
}

func TestAPIKeyAuthConfiguredInternalAdminKey(t *testing.T) {
	introspectionCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		introspectionCalled = true
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{
		URL:              upstream.URL,
		InternalAdminKey: "configured-internal-admin-key",
	}, Deps{})

	_, err := srv.APIKeyAuth(context.Background(), "configured-internal-admin-key", nil)
	require.NoError(t, err)
	require.False(t, introspectionCalled)
}

func TestAPIKeyAuthRejectsMissingAndUnknownKeys(t *testing.T) {
	srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{}, Deps{})

	for _, key := range []string{"", "unknown-key"} {
		_, err := srv.APIKeyAuth(context.Background(), key, nil)
		require.Error(t, err)
		var apiErr *runner.APIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, http.StatusUnauthorized, apiErr.Code)
	}
}

func TestAPIKeyAuthInternalAdminIntrospection(t *testing.T) {
	introspectionCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/apikey/authenticate-internal-admin", r.URL.Path)
		introspectionCalls++
		if r.Header.Get("Credimi-Api-Key") == "internal-admin-key" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: upstream.URL}, Deps{})

	_, err := srv.APIKeyAuth(context.Background(), "internal-admin-key", nil)
	require.NoError(t, err)

	_, err = srv.APIKeyAuth(context.Background(), "internal-admin-key", nil)
	require.NoError(t, err)
	require.Equal(t, 1, introspectionCalls)

	_, err = srv.APIKeyAuth(context.Background(), "user-scoped-key", nil)
	require.Error(t, err)
	require.Equal(t, 2, introspectionCalls)
}
