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

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/forkbombeu/credimi-runner/pkg/workermanager"
	"github.com/stretchr/testify/require"
	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type startWorkersHTTPClient struct {
	mu        sync.Mutex
	lastKey   string
	responder func(req *http.Request) (*http.Response, error)
}

func (c *startWorkersHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.lastKey = req.Header.Get(internalAdminKeyHeader)
	c.mu.Unlock()
	return c.responder(req)
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

func installStartWorkersTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	prev := otelapi.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otelapi.SetTracerProvider(tp)
	t.Cleanup(func() {
		require.NoError(t, tp.Shutdown(context.Background()))
		otelapi.SetTracerProvider(prev)
	})

	return recorder
}

func findRecordedSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()

	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("expected span %q to be recorded", name)
	return nil
}

func findRecordedEvent(t *testing.T, span sdktrace.ReadOnlySpan, name string) sdktrace.Event {
	t.Helper()

	for _, event := range span.Events() {
		if event.Name == name {
			return event
		}
	}
	t.Fatalf("expected event %q on span %q", name, span.Name())
	return sdktrace.Event{}
}

func eventAttributeValue(event sdktrace.Event, key attribute.Key) string {
	for _, attr := range event.Attributes {
		if attr.Key == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

func TestStartExistingWorkers_Success(t *testing.T) {
	store := NewProcessStore()
	existing := NewProcess("already-running", nil)
	existing.Running = true
	store.Add(existing)

	startedCh := make(chan string, 1)
	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/organizations/namespaces", req.URL.Path)
			require.Empty(t, req.URL.RawQuery)
			return httpResp(http.StatusOK, `{"namespaces":["already-running","new-ns",""]}`), nil
		},
	}

	deps := Deps{
		HTTPClient: client,
		WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
			return func(ctx context.Context) error {
				startedCh <- namespace
				<-ctx.Done()
				return nil
			}
		},
	}
	srv := NewRunnerServiceWithDeps(store, utils.Instance{
		URL:              "http://example.local",
		InternalAdminKey: "internal-admin-key",
	}, deps)

	err := srv.StartExistingWorkers(context.Background())
	require.NoError(t, err)
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

func TestStartExistingWorkersReturnsInitialWorkerReadinessFailure(t *testing.T) {
	store := NewProcessStore()
	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/organizations/namespaces", req.URL.Path)
			return httpResp(http.StatusOK, `{"namespaces":["new-ns"]}`), nil
		},
	}
	srv := NewRunnerServiceWithDeps(store, utils.Instance{
		URL:              "http://example.local",
		InternalAdminKey: "internal-admin-key",
	}, Deps{
		HTTPClient: client,
		WorkerRunnerFactory: func(string) func(context.Context) error {
			return func(context.Context) error { return nil }
		},
		WorkerStartupCheck: func(string) error { return errors.New("Temporal unavailable") },
	})
	err := srv.StartExistingWorkers(context.Background())
	require.ErrorContains(t, err, "initialize worker for namespace new-ns")
	if _, found := store.Get("new-ns"); found {
		t.Fatal("worker was added despite failed initial readiness")
	}
}

func TestStartExistingWorkersGivesEachNamespaceWorkerLiveInventoryProvider(t *testing.T) {
	store := NewProcessStore()
	client := &startWorkersHTTPClient{responder: func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "/api/organizations/namespaces", req.URL.Path)
		return httpResp(http.StatusOK, `{"namespaces":["ns-1","ns-2"]}`), nil
	}}
	inventories := make(chan workermanager.RunnerRuntimeConfig, 2)
	deps := Deps{
		HTTPClient:    client,
		RuntimeConfig: &dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values{"CREDIMI_RUNNER_ID": "acme/lab"}, Devices: []dashboardruntime.DeviceRuntimeConfig{{ID: "acme/lab/a", Type: "android_phone"}, {ID: "acme/lab/b", Type: "android_emulator"}}},
		InventoryWorkerRunnerFactory: func(namespace string, provider workermanager.RuntimeConfigProvider) func(context.Context) error {
			return func(ctx context.Context) error {
				inventory, err := provider()
				require.NoError(t, err)
				inventories <- inventory
				<-ctx.Done()
				return nil
			}
		},
	}
	srv := NewRunnerServiceWithDeps(store, utils.Instance{URL: "http://example.local"}, deps)
	require.NoError(t, srv.StartExistingWorkers(context.Background()))
	for range 2 {
		inventory := <-inventories
		require.Equal(t, "acme/lab", inventory.RunnerID)
		require.Len(t, inventory.Devices, 2)
	}
	for _, namespace := range []string{"ns-1", "ns-2"} {
		proc, ok := store.Get(namespace)
		require.True(t, ok)
		proc.Stop()
	}
}

func TestStartExistingWorkers_RecordsWorkerLifecycleEvents(t *testing.T) {
	recorder := installStartWorkersTracer(t)

	store := NewProcessStore()
	existing := NewProcess("already-running", nil)
	existing.Running = true
	store.Add(existing)

	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/organizations/namespaces", req.URL.Path)
			return httpResp(http.StatusOK, `{"namespaces":["already-running","new-ns"]}`), nil
		},
	}

	deps := Deps{
		HTTPClient: client,
		WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
			return func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			}
		},
	}

	srv := NewRunnerServiceWithDeps(store, utils.Instance{URL: "http://example.local"}, deps)

	require.NoError(t, srv.StartExistingWorkers(context.Background()))

	span := findRecordedSpan(t, recorder.Ended(), "start_existing_workers")
	alreadyRunning := findRecordedEvent(t, span, "worker.already_running")
	started := findRecordedEvent(t, span, "worker.started")
	require.Equal(t, "already-running", eventAttributeValue(alreadyRunning, attribute.Key("namespace")))
	require.Equal(t, "new-ns", eventAttributeValue(started, attribute.Key("namespace")))
	require.Equal(t, "", eventAttributeValue(started, attribute.Key("instance.name")))

	proc, ok := store.Get("new-ns")
	require.True(t, ok)
	proc.Stop()
}

func TestStartExistingWorkers_StartsAllAdminNamespaces(t *testing.T) {
	store := NewProcessStore()

	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/organizations/namespaces", req.URL.Path)
			return httpResp(http.StatusOK, `{"namespaces":["ns-1","ns-2"]}`), nil
		},
	}

	deps := Deps{
		HTTPClient: client,
		WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
			return func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			}
		},
	}

	srv := NewRunnerServiceWithDeps(store, utils.Instance{URL: "http://example.local"}, deps)

	err := srv.StartExistingWorkers(context.Background())
	require.NoError(t, err)

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
			require.Equal(t, "/api/organizations/namespaces", req.URL.Path)
			return httpResp(http.StatusOK, `{"namespaces":["ns-1","ns-2"]}`), nil
		},
	}

	var sleeps []time.Duration
	deps := Deps{
		HTTPClient: client,
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

	srv := NewRunnerServiceWithDeps(store, utils.Instance{URL: "http://example.local"}, deps)

	err := srv.StartExistingWorkers(context.Background())
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
			require.Equal(t, "/api/organizations/namespaces", req.URL.Path)
			return httpResp(http.StatusOK, `{"namespaces":["ns-1","ns-2"]}`), nil
		},
	}

	var sleeps []time.Duration
	deps := Deps{
		HTTPClient: client,
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

	srv := NewRunnerServiceWithDeps(store, utils.Instance{URL: "http://example.local"}, deps)

	err := srv.StartExistingWorkers(context.Background())
	require.NoError(t, err)
	require.Equal(t, []time.Duration{50 * time.Millisecond}, sleeps)

	proc1, ok := store.Get("ns-1")
	require.True(t, ok)
	proc1.Stop()
	proc2, ok := store.Get("ns-2")
	require.True(t, ok)
	proc2.Stop()
}

func TestStartExistingWorkers_ReturnsLookupFailures(t *testing.T) {
	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "broken") {
				return nil, errors.New("lookup error")
			}
			require.Equal(t, "/api/organizations/my", req.URL.Path)
			return httpResp(http.StatusOK, `{"name":"Good Org","canonified_name":"good-ns"}`), nil
		},
	}

	deps := Deps{
		HTTPClient: client,
		WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
			return func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			}
		},
	}
	srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{
		URL:        "http://broken.local",
		UserAPIKey: "bad-key",
	}, deps)

	err := srv.StartExistingWorkers(context.Background())
	require.ErrorContains(t, err, "lookup error")
}

func TestStartExistingWorkers_UserAPIKeyStartsOnlyResolvedNamespace(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_PUBLISHED", "false")
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
		WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
			return func(ctx context.Context) error {
				startedCh <- namespace
				<-ctx.Done()
				return nil
			}
		},
	}

	srv := NewRunnerServiceWithDeps(store, utils.Instance{
		URL:        "http://example.local",
		UserAPIKey: "user-api-key",
	}, deps)

	err := srv.StartExistingWorkers(context.Background())
	require.NoError(t, err)
	require.Equal(t, "user-api-key", client.keyHeader())

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

func TestStartExistingWorkers_PublishedUserAPIKeyStartsVisibleNamespaces(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_PUBLISHED", "true")
	store := NewProcessStore()

	client := &startWorkersHTTPClient{
		responder: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/api/organizations/visible-namespaces", req.URL.Path)
			require.Empty(t, req.URL.RawQuery)
			return httpResp(http.StatusOK, `{"namespaces":["user-ns","published-ns",""]}`), nil
		},
	}

	deps := Deps{
		HTTPClient: client,
		WorkerRunnerFactory: func(namespace string) func(ctx context.Context) error {
			return func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			}
		},
	}

	srv := NewRunnerServiceWithDeps(store, utils.Instance{
		URL:        "http://example.local",
		UserAPIKey: "user-api-key",
	}, deps)

	err := srv.StartExistingWorkers(context.Background())
	require.NoError(t, err)
	require.Equal(t, "user-api-key", client.keyHeader())

	userProc, ok := store.Get("user-ns")
	require.True(t, ok)
	require.True(t, userProc.Running)
	userProc.Stop()

	publishedProc, ok := store.Get("published-ns")
	require.True(t, ok)
	require.True(t, publishedProc.Running)
	publishedProc.Stop()
}

func TestStartExistingWorkers_UserAPIKeyReturnsLookupErrors(t *testing.T) {
	t.Run("organization lookup non-200", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{
			URL:        "http://example.local",
			UserAPIKey: "user-api-key",
		}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					require.Equal(t, "/api/organizations/my", req.URL.Path)
					return httpResp(http.StatusForbidden, ""), nil
				},
			},
		})

		err := srv.StartExistingWorkers(context.Background())
		require.ErrorContains(t, err, "failed to fetch organization for configured API key")
	})

	t.Run("organization lookup invalid JSON", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{
			URL:        "http://example.local",
			UserAPIKey: "user-api-key",
		}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					require.Equal(t, "/api/organizations/my", req.URL.Path)
					return httpResp(http.StatusOK, "{"), nil
				},
			},
		})

		err := srv.StartExistingWorkers(context.Background())
		require.ErrorContains(t, err, "failed to parse organization response")
	})

	t.Run("organization lookup empty namespace", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{
			URL:        "http://example.local",
			UserAPIKey: "user-api-key",
		}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					require.Equal(t, "/api/organizations/my", req.URL.Path)
					return httpResp(http.StatusOK, `{"name":"User Org","canonified_name":""}`), nil
				},
			},
		})

		err := srv.StartExistingWorkers(context.Background())
		require.ErrorContains(t, err, "organization namespace is empty")
	})
}

func TestStartExistingWorkers_ReturnsUpstreamErrors(t *testing.T) {
	t.Run("http request error", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: "http://example.local"}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					return nil, errors.New("dial failed")
				},
			},
		})

		err := srv.StartExistingWorkers(context.Background())
		require.ErrorContains(t, err, "failed to fetch organization namespaces")
	})

	t.Run("non-200 response", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: "http://example.local"}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					return httpResp(http.StatusUnauthorized, ""), nil
				},
			},
		})

		err := srv.StartExistingWorkers(context.Background())
		require.ErrorContains(t, err, "failed to fetch organization namespaces")
	})

	t.Run("invalid JSON", func(t *testing.T) {
		srv := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: "http://example.local"}, Deps{
			HTTPClient: &startWorkersHTTPClient{
				responder: func(req *http.Request) (*http.Response, error) {
					return httpResp(http.StatusOK, "{"), nil
				},
			},
		})

		err := srv.StartExistingWorkers(context.Background())
		require.ErrorContains(t, err, "failed to parse organization namespaces response")
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
