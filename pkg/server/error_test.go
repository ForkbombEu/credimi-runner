package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/forkbombeu/credimi-runner/pkg/gen/credimi"
	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/stretchr/testify/require"
	goa "goa.design/goa/v3/pkg"
)

func TestNormalizeAPIError(t *testing.T) {
	t.Run("nil becomes internal error", func(t *testing.T) {
		err := normalizeAPIError(nil)
		require.Equal(t, http.StatusInternalServerError, err.Code)
		require.Equal(t, "server", err.Domain)
		require.Equal(t, "InternalError", err.Reason)
		require.Equal(t, "internal server error", err.Message)
	})

	t.Run("zero status is normalized", func(t *testing.T) {
		err := normalizeAPIError(&runner.APIError{
			Code:    0,
			Domain:  "credimi",
			Reason:  "bad",
			Message: "broken",
		})
		require.Equal(t, http.StatusInternalServerError, err.Code)
		require.Equal(t, "credimi", err.Domain)
	})
}

func TestWrapWorkerAPIError(t *testing.T) {
	t.Run("maps bad request", func(t *testing.T) {
		err := wrapWorkerAPIError(&runner.APIError{Code: http.StatusBadRequest, Domain: "d", Reason: "r", Message: "m"})
		var svcErr *goa.ServiceError
		require.ErrorAs(t, err, &svcErr)
		require.Equal(t, "bad_request", svcErr.Name)
	})

	t.Run("maps nil to internal error", func(t *testing.T) {
		err := wrapWorkerAPIError(nil)
		var svcErr *goa.ServiceError
		require.ErrorAs(t, err, &svcErr)
		require.Equal(t, "internal_error", svcErr.Name)

		var apiErr *runner.APIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, http.StatusInternalServerError, apiErr.Code)
	})
}

func TestWrapCredimiAPIError(t *testing.T) {
	testCases := []struct {
		name string
		code int
		want string
	}{
		{name: "bad request", code: http.StatusBadRequest, want: "bad_request"},
		{name: "unauthorized", code: http.StatusUnauthorized, want: "unauthorized"},
		{name: "bad gateway", code: http.StatusBadGateway, want: "bad_gateway"},
		{name: "default to internal", code: http.StatusTeapot, want: "internal_error"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := wrapCredimiAPIError(&runner.APIError{Code: tc.code, Domain: "d", Reason: "r", Message: "m"})
			var svcErr *goa.ServiceError
			require.ErrorAs(t, err, &svcErr)
			require.Equal(t, tc.want, svcErr.Name)
		})
	}
}

func TestWrapMobileAPIError(t *testing.T) {
	err := wrapMobileAPIError(&runner.APIError{Code: http.StatusInternalServerError, Domain: "d", Reason: "r", Message: "m"})
	var svcErr *goa.ServiceError
	require.ErrorAs(t, err, &svcErr)
	require.Equal(t, "internal_error", svcErr.Name)
}

func TestWireFromRunnerAPIError(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		wire := wireFromrunnerAPIError(nil)
		require.Equal(t, http.StatusInternalServerError, wire.StatusCode())
		require.Equal(t, "server", wire.Domain)
		require.Equal(t, "internal error", wire.Reason)
	})

	t.Run("zero status normalized", func(t *testing.T) {
		wire := wireFromrunnerAPIError(&runner.APIError{
			Code:    0,
			Domain:  "credimi",
			Reason:  "boom",
			Message: "failed",
		})
		require.Equal(t, http.StatusInternalServerError, wire.StatusCode())
		require.Equal(t, "credimi", wire.Domain)
		require.Equal(t, "boom", wire.Reason)
		require.Equal(t, "failed", wire.Message)
	})
}

func TestGoaErrorFormatter(t *testing.T) {
	t.Run("runner API error", func(t *testing.T) {
		wire := GoaErrorFormatter(context.Background(), &runner.APIError{
			Code:    http.StatusUnauthorized,
			Domain:  "credimi",
			Reason:  "nope",
			Message: "denied",
		})
		w, ok := wire.(*apiErrorWire)
		require.True(t, ok)
		require.Equal(t, http.StatusUnauthorized, w.StatusCode())
		require.Equal(t, "credimi", w.Domain)
	})

	t.Run("wrapped service error", func(t *testing.T) {
		wire := GoaErrorFormatter(context.Background(), credimi.MakeBadRequest(&runner.APIError{
			Code:    http.StatusBadRequest,
			Domain:  "credimi",
			Reason:  "invalid",
			Message: "bad payload",
		}))
		w, ok := wire.(*apiErrorWire)
		require.True(t, ok)
		require.Equal(t, http.StatusBadRequest, w.StatusCode())
		require.Equal(t, "credimi", w.Domain)
		require.Equal(t, "invalid", w.Reason)
	})

	t.Run("decode payload maps to bad request", func(t *testing.T) {
		wire := GoaErrorFormatter(context.Background(), goa.NewServiceError(errors.New("decode"), goa.DecodePayload, false, false, false))
		w, ok := wire.(*apiErrorWire)
		require.True(t, ok)
		require.Equal(t, http.StatusBadRequest, w.StatusCode())
		require.Equal(t, "invalid JSON", w.Reason)
		require.Equal(t, "invalid request body", w.Message)
	})

	t.Run("service error by name fallback", func(t *testing.T) {
		wire := GoaErrorFormatter(context.Background(), goa.NewServiceError(errors.New("bad gateway"), "bad_gateway", false, false, false))
		w, ok := wire.(*apiErrorWire)
		require.True(t, ok)
		require.Equal(t, http.StatusBadGateway, w.StatusCode())
		require.Equal(t, "bad gateway", w.Reason)
	})

	t.Run("generic fallback", func(t *testing.T) {
		wire := GoaErrorFormatter(context.Background(), errors.New("boom"))
		w, ok := wire.(*apiErrorWire)
		require.True(t, ok)
		require.Equal(t, http.StatusInternalServerError, w.StatusCode())
		require.Equal(t, "internal error", w.Reason)
	})
}
