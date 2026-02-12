package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/stretchr/testify/require"
	cluelog "goa.design/clue/log"
)

func TestProcessStartEndpoint(t *testing.T) {
	t.Run("missing namespace", func(t *testing.T) {
		srv := NewRunnerService(NewProcessStore(), nil)

		result, apiErr := srv.processStart("", "")

		require.Nil(t, result)
		require.Equal(t, &runner.APIError{
			Code:    http.StatusBadRequest,
			Domain:  "Server",
			Reason:  "NamespaceMissing",
			Message: "namespace is required",
		}, apiErr)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		srv := NewRunnerService(NewProcessStore(), nil)
		ctx := cluelog.Context(context.Background(), cluelog.WithFormat(cluelog.FormatJSON))
		handler := NewHTTPHandler(ctx, srv, false)
		req := httptest.NewRequest(http.MethodPost, "/worker/example", strings.NewReader("{"))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		var apiErr runner.APIError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
		require.Equal(t, runner.APIError{
			Name:    "bad_request",
			Code:    http.StatusBadRequest,
			Domain:  "server",
			Reason:  "invalid JSON",
			Message: "invalid request body",
		}, apiErr)
	})

	t.Run("starts new worker", func(t *testing.T) {
		store := NewProcessStore()
		deps := Deps{
			WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
				return func(ctx context.Context) error { return nil }
			},
		}
		srv := NewRunnerServiceWithDeps(store, nil, deps)

		result, apiErr := srv.processStart("alpha", "")

		require.Nil(t, apiErr)
		require.Equal(t, "started", result.Status)
		require.Equal(t, "alpha", result.Namespace)
	})

	t.Run("already running", func(t *testing.T) {
		store := NewProcessStore()
		proc := NewProcess("beta", nil)
		proc.Running = true
		store.Add(proc)
		srv := NewRunnerService(store, nil)

		result, apiErr := srv.processStart("beta", "")

		require.Nil(t, apiErr)
		require.Equal(t, "already running", result.Status)
		require.Equal(t, "beta", result.Namespace)
	})

	t.Run("old namespace stops old worker", func(t *testing.T) {
		store := NewProcessStore()
		stopped := false
		oldProc := NewProcess("old", nil)
		oldProc.Running = true
		oldProc.CancelFunc = func() { stopped = true }
		store.Add(oldProc)

		deps := Deps{
			WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
				return func(ctx context.Context) error { return nil }
			},
		}
		srv := NewRunnerServiceWithDeps(store, nil, deps)

		_, apiErr := srv.processStart("new", "old")

		require.Nil(t, apiErr)
		require.True(t, stopped)
	})
}
