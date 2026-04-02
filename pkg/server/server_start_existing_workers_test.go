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
	lastKey   string
	responder func(req *http.Request) (*http.Response, error)
}

func (c *startWorkersHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.lastAuth = req.Header.Get("Authorization")
	c.lastKey = req.Header.Get(internalAdminKeyHeader)
	c.mu.Unlock()
	return c.responder(req)
}

func (c *startWorkersHTTPClient) authHeader() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastAuth
}

func (c *startWorkersHTTPClient) keyHeader() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastKey
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
	t.Setenv("CREDIMI_INTERNAL_ADMIN_KEY", "internal-admin-key")

	store := NewProcessStore()
	existing := NewProcess("already-running", nil)
	existing.Running = true
	store.Add(existing)

	startedCh := make(chan string, 1)
	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/collections/organizations/records", req.URL.Path)
			require.Equal(t, "1", req.URL.Query().Get("page"))
			require.Equal(t, "200", req.URL.Query().Get("perPage"))
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
	require.Equal(t, "internal-admin-key", client.keyHeader())
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

func TestStartExistingWorkers_PaginatesAllOrganizations(t *testing.T) {
	store := NewProcessStore()

	var pages []string
	var startedMu sync.Mutex
	started := make(map[string]bool)

	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/collections/organizations/records", req.URL.Path)
			page := req.URL.Query().Get("page")
			require.Equal(t, "200", req.URL.Query().Get("perPage"))
			pages = append(pages, page)

			switch page {
			case "1":
				return httpResp(http.StatusOK, `{"page":1,"perPage":200,"totalPages":2,"items":[{"name":"A","canonified_name":"ns-1"}]}`), nil
			case "2":
				return httpResp(http.StatusOK, `{"page":2,"perPage":200,"totalPages":2,"items":[{"name":"B","canonified_name":"ns-2"}]}`), nil
			default:
				t.Fatalf("unexpected page %q", page)
				return nil, nil
			}
		},
	}

	deps := Deps{
		HTTPClient: client,
		TokenProvider: func(instance utils.Instance) (string, error) {
			return "token-123", nil
		},
		WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
			return func(ctx context.Context) error {
				startedMu.Lock()
				started[namespace] = true
				startedMu.Unlock()
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
	require.ElementsMatch(t, []string{"1", "2"}, pages)

	proc1, ok := store.Get("ns-1")
	require.True(t, ok)
	require.True(t, proc1.Running)
	proc1.Stop()

	proc2, ok := store.Get("ns-2")
	require.True(t, ok)
	require.True(t, proc2.Running)
	proc2.Stop()
}

func TestStartExistingWorkers_AppliesStartupDelayBetweenStarts(t *testing.T) {
	t.Setenv("CREDIMI_WORKER_START_DELAY_MS", "25")

	store := NewProcessStore()
	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/collections/organizations/records", req.URL.Path)
			return httpResp(http.StatusOK, `{"items":[{"name":"A","canonified_name":"ns-1"},{"name":"B","canonified_name":"ns-2"}]}`), nil
		},
	}

	var sleeps []time.Duration
	deps := Deps{
		HTTPClient: client,
		TokenProvider: func(instance utils.Instance) (string, error) {
			return "token-123", nil
		},
		Sleeper: func(d time.Duration) {
			sleeps = append(sleeps, d)
		},
		WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
			return func(ctx context.Context) error {
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
	require.Equal(t, []time.Duration{25 * time.Millisecond}, sleeps)

	proc1, ok := store.Get("ns-1")
	require.True(t, ok)
	proc1.Stop()
	proc2, ok := store.Get("ns-2")
	require.True(t, ok)
	proc2.Stop()
}

func TestStartExistingWorkers_DefaultStartupDelayBetweenStarts(t *testing.T) {
	t.Setenv("CREDIMI_WORKER_START_DELAY_MS", "")

	store := NewProcessStore()
	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/collections/organizations/records", req.URL.Path)
			return httpResp(http.StatusOK, `{"items":[{"name":"A","canonified_name":"ns-1"},{"name":"B","canonified_name":"ns-2"}]}`), nil
		},
	}

	var sleeps []time.Duration
	deps := Deps{
		HTTPClient: client,
		TokenProvider: func(instance utils.Instance) (string, error) {
			return "token-123", nil
		},
		Sleeper: func(d time.Duration) {
			sleeps = append(sleeps, d)
		},
		WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
			return func(ctx context.Context) error {
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
	require.Equal(t, []time.Duration{50 * time.Millisecond}, sleeps)

	proc1, ok := store.Get("ns-1")
	require.True(t, ok)
	proc1.Stop()
	proc2, ok := store.Get("ns-2")
	require.True(t, ok)
	proc2.Stop()
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

func TestStartExistingWorkers_UserAPIKeyStartsOnlyResolvedNamespace(t *testing.T) {
	store := NewProcessStore()
	startedCh := make(chan string, 1)

	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/organizations/my", req.URL.Path)
			require.Empty(t, req.URL.RawQuery)
			return httpResp(http.StatusOK, `{"name":"User Org","canonified_name":"user-ns"}`), nil
		},
	}

	deps := Deps{
		HTTPClient: client,
		TokenProvider: func(instance utils.Instance) (string, error) {
			require.Equal(t, "user-api-key", instance.UserAPIKey)
			return "user-token", nil
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
		"prod": {URL: "http://example.local", UserAPIKey: "user-api-key"},
	}, deps)

	err := srv.StartExistingWorkers()
	require.NoError(t, err)
	require.Equal(t, "Bearer user-token", client.authHeader())
	require.Empty(t, client.keyHeader())

	select {
	case ns := <-startedCh:
		require.Equal(t, "user-ns", ns)
	case <-time.After(2 * time.Second):
		t.Fatal("worker run function was not started")
	}

	proc, ok := store.Get("user-ns")
	require.True(t, ok)
	require.True(t, proc.Running)
	proc.Stop()
}

func TestStartExistingWorkers_UserAPIKeyReturnsLookupErrors(t *testing.T) {
	t.Run("organization lookup non-200", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
			"prod": {URL: "http://example.local", UserAPIKey: "user-api-key"},
		}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					require.Equal(t, "/api/organizations/my", req.URL.Path)
					return httpResp(http.StatusForbidden, ""), nil
				},
			},
			TokenProvider: func(instance utils.Instance) (string, error) { return "user-token", nil },
		})

		err := srv.StartExistingWorkers()
		require.ErrorContains(t, err, "failed to fetch organization for configured API key")
	})

	t.Run("organization lookup invalid JSON", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
			"prod": {URL: "http://example.local", UserAPIKey: "user-api-key"},
		}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					require.Equal(t, "/api/organizations/my", req.URL.Path)
					return httpResp(http.StatusOK, "{"), nil
				},
			},
			TokenProvider: func(instance utils.Instance) (string, error) { return "user-token", nil },
		})

		err := srv.StartExistingWorkers()
		require.ErrorContains(t, err, "failed to parse organization response")
	})

	t.Run("organization lookup empty namespace", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
			"prod": {URL: "http://example.local", UserAPIKey: "user-api-key"},
		}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					require.Equal(t, "/api/organizations/my", req.URL.Path)
					return httpResp(http.StatusOK, `{"name":"User Org","canonified_name":""}`), nil
				},
			},
			TokenProvider: func(instance utils.Instance) (string, error) { return "user-token", nil },
		})

		err := srv.StartExistingWorkers()
		require.ErrorContains(t, err, "organization namespace is empty")
	})
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

func TestStartupWorkerDelay_InvalidValuesFallBackToDefault(t *testing.T) {
	t.Run("invalid integer", func(t *testing.T) {
		t.Setenv("CREDIMI_WORKER_START_DELAY_MS", "not-a-number")
		require.Equal(t, 50*time.Millisecond, startupWorkerDelay())
	})

	t.Run("negative integer", func(t *testing.T) {
		t.Setenv("CREDIMI_WORKER_START_DELAY_MS", "-10")
		require.Equal(t, 50*time.Millisecond, startupWorkerDelay())
	})
}
