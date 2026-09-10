package lifecycle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/device"
	"github.com/stretchr/testify/require"
)

type recordedRequest struct {
	path string
	body map[string]any
}
type recordingClient struct {
	mu       sync.Mutex
	requests []recordedRequest
	failed   map[string]int
}

func (client *recordingClient) Do(request *http.Request) (*http.Response, error) {
	defer request.Body.Close()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	client.mu.Lock()
	client.requests = append(client.requests, recordedRequest{path: request.URL.Path, body: decoded})
	status := http.StatusOK
	if client.failed[request.URL.Path] > 0 {
		status = http.StatusBadGateway
		client.failed[request.URL.Path]--
	}
	client.mu.Unlock()
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader("failure")), Header: make(http.Header)}, nil
}

func testRegistry(t *testing.T) *device.Registry {
	t.Helper()
	registry, err := device.NewRegistry([]config.DeviceConfig{
		{ID: "acme/runner/phone", Name: "Phone", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Serial: "phone"}},
		{ID: "acme/runner/disabled", Name: "Disabled", Type: config.DeviceRedroid, Enabled: false, Redroid: &config.RedroidConfig{Host: "redroid", ADBPort: 5555}},
	})
	require.NoError(t, err)
	return registry
}

func newTestService(t *testing.T, client *recordingClient, probe Probe) *Service {
	t.Helper()
	service, err := New(Config{CredimiURL: "https://credimi.example", APIKey: "key", Host: Host{ID: "acme/runner", Name: "Runner", Organization: "acme"}}, testRegistry(t), client, probe)
	require.NoError(t, err)
	return service
}

func TestRegisterContinuesAfterIndependentDeviceFailure(t *testing.T) {
	client := &recordingClient{failed: map[string]int{"/api/mobile-device": 1}}
	service := newTestService(t, client, nil)
	err := service.Register(context.Background())
	require.ErrorContains(t, err, "register device")
	require.Len(t, client.requests, 4)
	require.Equal(t, "/api/mobile-runner", client.requests[0].path)
	require.Equal(t, "/api/mobile-device/reconcile", client.requests[3].path)
}

func TestHeartbeatReportsEveryDeviceIndependently(t *testing.T) {
	client := &recordingClient{}
	service := newTestService(t, client, func(_ context.Context, target device.Device) device.Status {
		return device.Status{DeviceID: target.ID, Enabled: target.Enabled, Online: true, Ready: target.ID == "acme/runner/phone"}
	})
	require.NoError(t, service.Heartbeat(context.Background()))
	require.Len(t, client.requests, 1)
	states, ok := client.requests[0].body["devices"].([]any)
	require.True(t, ok)
	require.Len(t, states, 2)
	first := states[0].(map[string]any)
	second := states[1].(map[string]any)
	require.Equal(t, "acme/runner/disabled", first["device_id"])
	require.Equal(t, false, first["online"])
	require.Equal(t, "disabled", first["reason"])
	require.Equal(t, "acme/runner/phone", second["device_id"])
	require.Equal(t, true, second["online"])
}

func TestDeviceStatesPreserveOnlineAndReadyIndependently(t *testing.T) {
	client := &recordingClient{}
	service := newTestService(t, client, func(_ context.Context, target device.Device) device.Status {
		return device.Status{DeviceID: target.ID, Enabled: true, Online: true, Ready: false, Busy: true, ObservedAt: time.Unix(42, 0)}
	})
	states := service.DeviceStates(t.Context())
	phone := states[1]
	require.True(t, phone.Online)
	require.False(t, phone.Ready)
	require.True(t, phone.Busy)
	require.Equal(t, time.Unix(42, 0), phone.ObservedAt)

	require.NoError(t, service.Heartbeat(t.Context()))
	encoded := client.requests[0].body["devices"].([]any)[1].(map[string]any)
	_, hasReady := encoded["ready"]
	_, hasBusy := encoded["busy"]
	require.False(t, hasReady)
	require.False(t, hasBusy)
}

func TestNewRejectsIncompleteLifecycleConfiguration(t *testing.T) {
	_, err := New(Config{}, testRegistry(t), nil, nil)
	require.Error(t, err)
}

func TestResumeAndPauseCarryOnlyTheirExplicitReason(t *testing.T) {
	client := &recordingClient{}
	service := newTestService(t, client, nil)
	require.NoError(t, service.Resume(context.Background(), "startup"))
	require.NoError(t, service.Pause(context.Background(), "shutdown"))
	require.Len(t, client.requests, 2)
	require.Equal(t, "/api/mobile-runner/lifecycle/resume", client.requests[0].path)
	require.Equal(t, "startup", client.requests[0].body["reason"])
	require.Equal(t, "/api/mobile-runner/lifecycle/pause", client.requests[1].path)
	require.Equal(t, "shutdown", client.requests[1].body["reason"])
}

func TestLifecycleRequestFailureIsReturned(t *testing.T) {
	client := &recordingClient{failed: map[string]int{"/api/mobile-runner/lifecycle/heartbeat": 1}}
	service := newTestService(t, client, nil)
	require.ErrorContains(t, service.Heartbeat(context.Background()), "unexpected status")
}

func TestNewAppliesLifecycleDefaultsAndRejectsMissingDependencies(t *testing.T) {
	registry := testRegistry(t)
	_, err := New(Config{CredimiURL: "https://credimi.example", APIKey: "key"}, registry, nil, nil)
	require.ErrorContains(t, err, "runner ID")
	_, err = New(Config{CredimiURL: "https://credimi.example", APIKey: "key", Host: Host{ID: "acme/runner"}}, nil, nil, nil)
	require.ErrorContains(t, err, "device registry")
	service, err := New(Config{CredimiURL: "https://credimi.example", APIKey: "key", Host: Host{ID: "acme/runner"}}, registry, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, service.config.HeartbeatInterval)
	require.NotNil(t, service.client)
	states := service.DeviceStates(context.Background())
	require.Equal(t, "probe is not configured", states[1].Reason)
}

func TestStartSendsImmediateHeartbeat(t *testing.T) {
	client := &recordingClient{}
	service := newTestService(t, client, nil)
	ctx, cancel := context.WithCancel(context.Background())
	stop, err := service.Start(ctx)
	require.NoError(t, err)
	require.Len(t, client.requests, 1)
	stop()
	cancel()
}

func TestStartReturnsInitialHeartbeatFailure(t *testing.T) {
	client := &recordingClient{failed: map[string]int{"/api/mobile-runner/lifecycle/heartbeat": 1}}
	service := newTestService(t, client, nil)
	stop, err := service.Start(context.Background())
	if stop != nil {
		t.Fatal("failed start returned a stop function")
	}
	require.ErrorContains(t, err, "unexpected status")
}

func TestStartSendsPeriodicHeartbeatsUntilStopped(t *testing.T) {
	client := &recordingClient{}
	service := newTestService(t, client, nil)
	service.config.HeartbeatInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := service.Start(ctx)
	require.NoError(t, err)
	deadline := time.After(time.Second)
	for {
		client.mu.Lock()
		count := len(client.requests)
		client.mu.Unlock()
		if count >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("periodic heartbeat was not sent")
		case <-time.After(time.Millisecond):
		}
	}
	stop()
}

func TestDefaultProbeTreatsIdleRedroidAsReady(t *testing.T) {
	registry, err := device.NewRegistry([]config.DeviceConfig{{
		ID: "acme/runner/redroid", Name: "Redroid", Type: config.DeviceRedroid, Enabled: true,
		Redroid: &config.RedroidConfig{Host: "10.0.0.4", ADBPort: 5555},
	}})
	require.NoError(t, err)
	service, err := New(Config{CredimiURL: "https://credimi.example", APIKey: "key", Host: Host{ID: "acme/runner"}}, registry, nil, nil)
	require.NoError(t, err)
	states := service.DeviceStates(context.Background())
	require.Len(t, states, 1)
	require.True(t, states[0].Online)
	require.True(t, states[0].Ready)
	require.Equal(t, "configured; idle runtime", states[0].Reason)
}
