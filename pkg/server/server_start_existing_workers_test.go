package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
)

type startWorkersHTTPClient struct {
	mu        sync.Mutex
	lastAuth  string
	responder func(req *http.Request) (*http.Response, error)
}

func (c *startWorkersHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.lastAuth = req.Header.Get("Authorization")
	c.mu.Unlock()
	return c.responder(req)
}

func (c *startWorkersHTTPClient) authHeader() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastAuth
}

func httpResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestStartExistingWorkers_Success(t *testing.T) {
	store := NewProcessStore()
	existing := NewProcess("already-running", nil)
	existing.Running = true
	store.Add(existing)

	startedCh := make(chan string, 1)
	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/collections/organizations/records", req.URL.Path)
			return httpResp(http.StatusOK, `{"items":[{"name":"A","canonified_name":"already-running"},{"name":"B","canonified_name":"new-ns"},{"name":"C","canonified_name":""}]}`), nil
		},
	}

	deps := Deps{
		HTTPClient: client,
		TokenProvider: func(instance utils.Instance) (string, error) {
			return "token-123", nil
		},
		WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
			return func(ctx context.Context) error {
				startedCh <- namespace
				<-ctx.Done()
				return nil
			}
		},
	}
	srv := NewRunnerServiceWithDeps(store, map[string]utils.Instance{
		"prod": {URL: "http://example.local"},
	}, deps)

	err := srv.StartExistingWorkers()
	require.NoError(t, err)
	require.Equal(t, "Bearer token-123", client.authHeader())
	select {
	case ns := <-startedCh:
		require.Equal(t, "new-ns", ns)
	case <-time.After(2 * time.Second):
		t.Fatal("worker run function was not started")
	}

	proc, ok := store.Get("new-ns")
	require.True(t, ok)
	require.True(t, proc.Running)
	proc.Stop()
}

func TestStartExistingWorkers_SkipsTokenFailures(t *testing.T) {
	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			return httpResp(http.StatusOK, `{"items":[]}`), nil
		},
	}

	deps := Deps{
		HTTPClient: client,
		TokenProvider: func(instance utils.Instance) (string, error) {
			if strings.Contains(instance.URL, "broken") {
				return "", errors.New("token error")
			}
			return "ok-token", nil
		},
	}
	srv := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
		"bad":  {URL: "http://broken.local"},
		"good": {URL: "http://good.local"},
	}, deps)

	err := srv.StartExistingWorkers()
	require.NoError(t, err)
	require.Equal(t, "Bearer ok-token", client.authHeader())
}

func TestStartExistingWorkers_ReturnsUpstreamErrors(t *testing.T) {
	t.Run("http request error", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
			"prod": {URL: "http://example.local"},
		}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					return nil, errors.New("dial failed")
				},
			},
			TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		})

		err := srv.StartExistingWorkers()
		require.ErrorContains(t, err, "failed to fetch organizations")
	})

	t.Run("non-200 response", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
			"prod": {URL: "http://example.local"},
		}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					return httpResp(http.StatusUnauthorized, ""), nil
				},
			},
			TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		})

		err := srv.StartExistingWorkers()
		require.ErrorContains(t, err, "failed to fetch organizations")
	})

	t.Run("invalid JSON", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
			"prod": {URL: "http://example.local"},
		}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					return httpResp(http.StatusOK, "{"), nil
				},
			},
			TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		})

		err := srv.StartExistingWorkers()
		require.ErrorContains(t, err, "failed to parse organizations response")
	})
}
