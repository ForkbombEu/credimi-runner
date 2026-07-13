package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
)

type recordedLifecycleRequest struct {
	Path   string
	Header http.Header
	Body   lifecyclePayload
}

type lifecycleRecorder struct {
	mu   sync.Mutex
	reqs []recordedLifecycleRequest
}

func (r *lifecycleRecorder) append(req recordedLifecycleRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
}

func (r *lifecycleRecorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func (r *lifecycleRecorder) get(index int) recordedLifecycleRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[index]
}

type fakeLifecycleTicker struct {
	ch chan time.Time
}

func TestTimeTickerDelegatesToStandardTicker(t *testing.T) {
	ticker := timeTicker{ticker: time.NewTicker(time.Hour)}
	if ticker.Chan() == nil {
		t.Fatal("ticker channel is nil")
	}
	ticker.Stop()
}

func newFakeLifecycleTicker() *fakeLifecycleTicker {
	return &fakeLifecycleTicker{ch: make(chan time.Time, 8)}
}

func (t *fakeLifecycleTicker) Stop() {}

func (t *fakeLifecycleTicker) Chan() <-chan time.Time {
	return t.ch
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestLoadRunnerLifecycleConfigDefaults(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_ID", "/owner/runner")
	t.Setenv(lifecycleEnabledEnvName, "")
	t.Setenv(lifecycleHeartbeatIntervalEnvName, "")

	cfg := LoadRunnerLifecycleConfig(utils.Instance{
		URL:              "https://credimi.example",
		UserAPIKey:       "user-key",
		InternalAdminKey: "admin-key",
	})

	require.True(t, cfg.Enabled)
	require.Equal(t, "/owner/runner", cfg.RunnerID)
	require.Equal(t, "https://credimi.example", cfg.CredimiURL)
	require.Equal(t, "user-key", cfg.APIKey)
	require.Equal(t, defaultHeartbeatInterval, cfg.HeartbeatInterval)
	require.Equal(t, defaultLifecycleRequestTimeout, cfg.RequestTimeout)
}

func TestLoadRunnerLifecycleConfigDisabled(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_ID", "/owner/runner")
	t.Setenv(lifecycleEnabledEnvName, "false")

	cfg := LoadRunnerLifecycleConfig(utils.Instance{URL: "https://credimi.example"})

	require.False(t, cfg.Enabled)
}

func TestLoadRunnerLifecycleConfigParsesDurations(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_ID", "/owner/runner")
	t.Setenv(lifecycleHeartbeatIntervalEnvName, "45s")

	cfg := LoadRunnerLifecycleConfig(utils.Instance{URL: "https://credimi.example"})

	require.Equal(t, 45*time.Second, cfg.HeartbeatInterval)
	require.Equal(t, defaultLifecycleRequestTimeout, cfg.RequestTimeout)
}

func TestLoadRunnerLifecycleConfigPrefersUserAPIKey(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_ID", "/owner/runner")

	cfg := LoadRunnerLifecycleConfig(utils.Instance{
		URL:              "https://credimi.example",
		UserAPIKey:       "user-key",
		InternalAdminKey: "admin-key",
	})

	require.Equal(t, "user-key", cfg.APIKey)
}

func TestLoadRunnerLifecycleConfigIgnoresBackendPolicyEnvs(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_ID", "/owner/runner")
	t.Setenv("CREDIMI_RUNNER_HEARTBEAT_TIMEOUT", "99s")
	t.Setenv("CREDIMI_RUNNER_SHUTDOWN_AFTER_TIMEOUT", "77s")
	t.Setenv("CREDIMI_RUNNER_CANCEL_RUNNING", "true")

	cfg := LoadRunnerLifecycleConfig(utils.Instance{URL: "https://credimi.example"})

	require.Equal(t, defaultHeartbeatInterval, cfg.HeartbeatInterval)
	require.Equal(t, defaultLifecycleRequestTimeout, cfg.RequestTimeout)
}

func TestRunnerLifecycleResumePostsExpectedPayload(t *testing.T) {
	recorder, client := newLifecycleClientForServer(t, http.StatusAccepted, nil)

	err := client.Resume(context.Background(), "runner_startup")

	require.NoError(t, err)
	require.Equal(t, 1, recorder.len())
	req := recorder.get(0)
	require.Equal(t, "/api/mobile-runner/lifecycle/resume", req.Path)
	require.Equal(t, "runner-key", req.Header.Get(internalAdminKeyHeader))
	require.Equal(t, "/owner/runner", req.Body.RunnerID)
	require.Equal(t, "runner_startup", req.Body.Reason)
}

func TestRunnerLifecycleHeartbeatPostsExpectedPayload(t *testing.T) {
	recorder, client := newLifecycleClientForServer(t, http.StatusOK, nil)

	err := client.Heartbeat(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, recorder.len())
	req := recorder.get(0)
	require.Equal(t, "/api/mobile-runner/lifecycle/heartbeat", req.Path)
	require.Equal(t, "heartbeat", req.Body.Reason)
}

func TestRunnerLifecyclePausePostsExpectedPayload(t *testing.T) {
	recorder, client := newLifecycleClientForServer(t, http.StatusOK, nil)

	err := client.Pause(context.Background(), "runner_shutdown")

	require.NoError(t, err)
	require.Equal(t, 1, recorder.len())
	req := recorder.get(0)
	require.Equal(t, "/api/mobile-runner/lifecycle/pause", req.Path)
	require.Equal(t, "runner_shutdown", req.Body.Reason)
}

func TestRunnerLifecyclePayloadDoesNotContainBackendPolicyFields(t *testing.T) {
	var rawBody []byte
	_, client := newLifecycleClientForServer(t, http.StatusOK, func(_ *testing.T, body []byte) {
		rawBody = append([]byte(nil), body...)
	})

	err := client.Heartbeat(context.Background())

	require.NoError(t, err)
	require.NotContains(t, string(rawBody), "heartbeat_timeout_seconds")
	require.NotContains(t, string(rawBody), "shutdown_after_seconds")
	require.NotContains(t, string(rawBody), "cancel_running")
}

func TestRunnerLifecycleNon2xxReturnsError(t *testing.T) {
	_, client := newLifecycleClientForServer(t, http.StatusBadGateway, nil)

	err := client.Resume(context.Background(), "runner_startup")

	require.Error(t, err)
	require.ErrorContains(t, err, "502 Bad Gateway")
	require.ErrorContains(t, err, "bad gateway")
}

func TestRunnerLifecycleDisabledDoesNotMakeHTTPCalls(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewRunnerLifecycleClient(RunnerLifecycleConfig{
		Enabled:           false,
		RunnerID:          "/owner/runner",
		CredimiURL:        srv.URL,
		APIKey:            "runner-key",
		HeartbeatInterval: time.Second,
		RequestTimeout:    time.Second,
	}, srv.Client(), NewProcessStore())

	require.NoError(t, client.Resume(context.Background(), "runner_startup"))
	require.Equal(t, 0, calls)
}

func TestRunnerLifecycleMissingCredentialsDoesNotMakeHTTPCalls(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewRunnerLifecycleClient(RunnerLifecycleConfig{
		Enabled:           true,
		RunnerID:          "/owner/runner",
		CredimiURL:        srv.URL,
		HeartbeatInterval: time.Second,
		RequestTimeout:    time.Second,
	}, srv.Client(), NewProcessStore())
	client.warnf = func(string, ...any) {}

	require.NoError(t, client.Resume(context.Background(), "runner_startup"))
	require.Equal(t, 0, calls)
}

func TestRunnerLifecycleHeartbeatLoopSendsHeartbeat(t *testing.T) {
	recorder, client := newLifecycleClientForServer(t, http.StatusOK, nil)
	ticker := newFakeLifecycleTicker()
	client.newTicker = func(time.Duration) lifecycleTicker { return ticker }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopLoop := client.StartHeartbeatLoop(ctx)
	defer stopLoop()

	ticker.ch <- time.Now()
	require.Eventually(t, func() bool {
		return recorder.len() == 1
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, "/api/mobile-runner/lifecycle/heartbeat", recorder.get(0).Path)
}

func TestRunnerLifecycleHeartbeatLoopStopPreventsFurtherHeartbeats(t *testing.T) {
	recorder, client := newLifecycleClientForServer(t, http.StatusOK, nil)
	ticker := newFakeLifecycleTicker()
	client.newTicker = func(time.Duration) lifecycleTicker { return ticker }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopLoop := client.StartHeartbeatLoop(ctx)
	ticker.ch <- time.Now()
	require.Eventually(t, func() bool {
		return recorder.len() == 1
	}, time.Second, 10*time.Millisecond)

	stopLoop()
	time.Sleep(50 * time.Millisecond)
	ticker.ch <- time.Now()
	time.Sleep(100 * time.Millisecond)

	require.Equal(t, 1, recorder.len())
}

func TestRunnerLifecycleHeartbeatLoopContinuesAfterFailure(t *testing.T) {
	recorder := &lifecycleRecorder{}
	ticker := newFakeLifecycleTicker()
	calls := 0
	var mu sync.Mutex

	client := NewRunnerLifecycleClient(RunnerLifecycleConfig{
		Enabled:           true,
		RunnerID:          "/owner/runner",
		CredimiURL:        "https://credimi.example",
		APIKey:            "runner-key",
		HeartbeatInterval: time.Second,
		RequestTimeout:    time.Second,
	}, nil, NewProcessStore())
	client.newTicker = func(time.Duration) lifecycleTicker { return ticker }
	client.warnf = func(string, ...any) {}
	client.httpClient = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		calls++
		callNumber := calls
		mu.Unlock()

		if callNumber == 1 {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Status:     "502 Bad Gateway",
				Body:       io.NopCloser(strings.NewReader("temporary failure")),
				Header:     make(http.Header),
			}, nil
		}

		recorder.append(recordedLifecycleRequest{
			Path:   req.URL.Path,
			Header: req.Header.Clone(),
			Body: lifecyclePayload{
				RunnerID: "/owner/runner",
				Reason:   "heartbeat",
			},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopLoop := client.StartHeartbeatLoop(ctx)
	defer stopLoop()

	ticker.ch <- time.Now()
	ticker.ch <- time.Now()
	require.Eventually(t, func() bool {
		return recorder.len() == 1
	}, time.Second, 10*time.Millisecond)
}

func TestRunnerLifecycleStopBeforePausePreventsHeartbeatAfterPause(t *testing.T) {
	var mu sync.Mutex
	heartbeatCalls := 0
	pauseCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/mobile-runner/lifecycle/heartbeat":
			heartbeatCalls++
		case "/api/mobile-runner/lifecycle/pause":
			pauseCalls++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewRunnerLifecycleClient(RunnerLifecycleConfig{
		Enabled:           true,
		RunnerID:          "/owner/runner",
		CredimiURL:        srv.URL,
		APIKey:            "runner-key",
		HeartbeatInterval: time.Second,
		RequestTimeout:    time.Second,
	}, srv.Client(), NewProcessStore())
	ticker := newFakeLifecycleTicker()
	client.newTicker = func(time.Duration) lifecycleTicker { return ticker }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopLoop := client.StartHeartbeatLoop(ctx)
	stopLoop()
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, client.Pause(context.Background(), "runner_shutdown"))
	ticker.ch <- time.Now()
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 0, heartbeatCalls)
	require.Equal(t, 1, pauseCalls)
}

func newLifecycleClientForServer(
	t *testing.T,
	status int,
	onBody func(*testing.T, []byte),
) (*lifecycleRecorder, *RunnerLifecycleClient) {
	t.Helper()

	recorder := &lifecycleRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		if onBody != nil {
			onBody(t, body)
		}

		var payload lifecyclePayload
		require.NoError(t, json.Unmarshal(body, &payload))

		recorder.append(recordedLifecycleRequest{
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   payload,
		})

		if status >= 400 {
			http.Error(w, "bad gateway", status)
			return
		}

		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	client := NewRunnerLifecycleClient(RunnerLifecycleConfig{
		Enabled:           true,
		RunnerID:          "/owner/runner",
		CredimiURL:        srv.URL,
		APIKey:            "runner-key",
		HeartbeatInterval: time.Second,
		RequestTimeout:    time.Second,
	}, srv.Client(), NewProcessStore())
	client.warnf = func(string, ...any) {}

	return recorder, client
}
