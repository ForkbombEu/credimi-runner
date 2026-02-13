package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithCORSPreflight(t *testing.T) {
	called := false
	h := withCORS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodOptions, "/workers", nil)
	req.Header.Set("Origin", "https://puria.credimi.io")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.False(t, called)
	require.Equal(t, "https://puria.credimi.io", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "GET, POST, PUT, PATCH, DELETE, OPTIONS", rec.Header().Get("Access-Control-Allow-Methods"))
	require.Equal(t, "content-type", rec.Header().Get("Access-Control-Allow-Headers"))
	require.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Private-Network"))
}

func TestWithCORSPassThrough(t *testing.T) {
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/workers", nil)
	req.Header.Set("Origin", "https://puria.credimi.io")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "https://puria.credimi.io", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "Content-Type, Authorization", rec.Header().Get("Access-Control-Allow-Headers"))
}

