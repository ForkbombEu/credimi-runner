package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

func TestReadinessFailureExplainsUnreachableAndroidPhone(t *testing.T) {
	cause := errors.New("dial tcp 127.0.0.1:8050: connect: connection refused")
	err := ReadinessFailure(dashboardruntime.Values{
		"CREDIMI_RUNNER_ID":       "acme/runner",
		"CREDIMI_DEVICE_COUNT":    "1",
		"CREDIMI_DEVICE_1_ID":     "acme/runner/device-1",
		"CREDIMI_DEVICE_1_TYPE":   "android_phone",
		"CREDIMI_DEVICE_1_MODE":   "usb",
		"CREDIMI_DEVICE_1_SERIAL": "device-1",
	}, "127.0.0.1:8050", cause, context.DeadlineExceeded)

	message := err.Error()
	for _, want := range []string{
		"runner never opened its listener",
		"configured device",
		"connection refused",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("ReadinessFailure() = %q, want %q", message, want)
		}
	}
	if !errors.Is(err, cause) {
		t.Fatalf("ReadinessFailure() does not retain cause %v", cause)
	}
}

func TestReadinessFailureExplainsUnauthorizedDevice(t *testing.T) {
	err := ReadinessFailure(dashboardruntime.Values{
		"CREDIMI_RUNNER_ID":       "acme/runner",
		"CREDIMI_DEVICE_COUNT":    "1",
		"CREDIMI_DEVICE_1_ID":     "acme/runner/device-1",
		"CREDIMI_DEVICE_1_TYPE":   "android_phone",
		"CREDIMI_DEVICE_1_MODE":   "usb",
		"CREDIMI_DEVICE_1_SERIAL": "device-1",
	}, "127.0.0.1:8050", ErrDeviceUnauthorized, context.DeadlineExceeded)

	if !strings.Contains(err.Error(), "USB debugging prompt") {
		t.Fatalf("ReadinessFailure() = %q", err)
	}
	if !errors.Is(err, ErrDeviceUnauthorized) {
		t.Fatalf("ReadinessFailure() does not retain ErrDeviceUnauthorized")
	}
}

func TestRuntimeLifecycleStartKeepsRuntimeWhenReadinessFails(t *testing.T) {
	manager := &lifecycleManager{}
	lifecycle := RuntimeLifecycle{
		Manager: manager,
		Values: dashboardruntime.Values{
			"CREDIMI_RUNNER_TYPE":    "android_phone",
			"CREDIMI_SERVICE_MODE":   "manual",
			"RUNNER_PUBLIC_URL":      "https://runner.example",
			"CREDIMI_RUNNER_BACKEND": "container",
		},
		GOOS: "linux",
		WaitReady: func(context.Context, dashboardruntime.Values) error {
			return errors.New("runner listener did not open")
		},
	}

	err := lifecycle.Start(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "runner listener did not open") {
		t.Fatalf("Start() error = %v", err)
	}
	if manager.starts != 1 || manager.stops != 0 || !manager.status.RunnerRunning {
		t.Fatalf("failed start should retain manager runtime state %#v", manager)
	}
	if !strings.Contains(err.Error(), "runtime remains running for inspection") {
		t.Fatalf("Start() error = %v", err)
	}
}

type lifecycleManager struct {
	status dashboardruntime.RuntimeStatus
	logs   []dashboardruntime.LogLine
	starts int
	stops  int
}

func (m *lifecycleManager) Start(context.Context) error {
	m.starts++
	m.status.RunnerRunning = true
	m.status.LastStartedAt = time.Now()
	return nil
}

func (m *lifecycleManager) Stop(context.Context) error {
	m.stops++
	m.status.RunnerRunning = false
	return nil
}

func (m *lifecycleManager) Restart(context.Context) error     { return nil }
func (m *lifecycleManager) UpdateImage(context.Context) error { return nil }
func (m *lifecycleManager) Configure(dashboardruntime.Values) {}
func (m *lifecycleManager) SetPublicURL(url string)           { m.status.PublicURL = url }
func (m *lifecycleManager) Status(context.Context) dashboardruntime.RuntimeStatus {
	return m.status
}
func (m *lifecycleManager) Logs(context.Context, int) ([]dashboardruntime.LogLine, error) {
	return m.logs, nil
}
func (m *lifecycleManager) TunnelLogs(context.Context, int) ([]dashboardruntime.LogLine, error) {
	return m.logs, nil
}

func TestRuntimeLifecycleStartRegistersFreshAutoURL(t *testing.T) {
	manager := &lifecycleManager{
		status: dashboardruntime.RuntimeStatus{PublicURL: "https://stale.trycloudflare.com"},
		logs:   []dashboardruntime.LogLine{{Message: "ready https://fresh.trycloudflare.com"}},
	}
	var request dashboardruntime.RegisterRunnerRequest
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()

	lifecycle := RuntimeLifecycle{
		Manager: manager,
		Values: dashboardruntime.Values{
			"CREDIMI_URL":                 api.URL,
			"CREDIMI_USER_API_KEY":        "key",
			"CREDIMI_RUNNER_ID":           "acme/runner",
			"CREDIMI_RUNNER_NAME":         "runner",
			"CREDIMI_RUNNER_ORGANIZATION": "acme",
			"CREDIMI_RUNNER_TYPE":         "android_phone",
			"CREDIMI_RUNNER_PUBLISHED":    "true",
			"CREDIMI_SERVICE_MODE":        "auto",
			"CREDIMI_RUNNER_BACKEND":      "container",
		},
		GOOS:      "linux",
		WaitReady: func(context.Context, dashboardruntime.Values) error { return nil },
	}
	if err := lifecycle.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if manager.starts != 1 || manager.status.PublicURL != "https://fresh.trycloudflare.com" {
		t.Fatalf("manager after start = %#v", manager)
	}
	if request.IP != "https://fresh.trycloudflare.com" || request.Published == nil || !*request.Published {
		t.Fatalf("registration request = %#v", request)
	}
}

func TestRuntimeLifecycleRestartUsesStopStartAndRegister(t *testing.T) {
	manager := &lifecycleManager{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer api.Close()
	lifecycle := RuntimeLifecycle{
		Manager: manager,
		Values: dashboardruntime.Values{
			"CREDIMI_URL":            api.URL,
			"CREDIMI_USER_API_KEY":   "key",
			"CREDIMI_RUNNER_ID":      "acme/runner",
			"CREDIMI_RUNNER_NAME":    "runner",
			"CREDIMI_SERVICE_MODE":   "manual",
			"RUNNER_PUBLIC_URL":      "https://runner.example",
			"CREDIMI_RUNNER_BACKEND": "container",
			"CREDIMI_RUNNER_TYPE":    "android_phone",
		},
		GOOS:      "linux",
		WaitReady: func(context.Context, dashboardruntime.Values) error { return nil },
	}
	if err := lifecycle.Restart(context.Background(), nil); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if manager.stops != 1 || manager.starts != 1 {
		t.Fatalf("restart calls stop=%d start=%d", manager.stops, manager.starts)
	}
}

func TestRuntimeLifecycleRegisterRequiresAPIKey(t *testing.T) {
	lifecycle := RuntimeLifecycle{Manager: &lifecycleManager{}, Values: dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "manual",
		"RUNNER_PUBLIC_URL":    "https://runner.example",
	}}
	if err := lifecycle.Register(context.Background()); err == nil {
		t.Fatal("Register() succeeded without an API key")
	}
}

func TestRuntimeLifecycleEndpointsAndStop(t *testing.T) {
	manager := &lifecycleManager{status: dashboardruntime.RuntimeStatus{PublicURL: "https://stale.trycloudflare.com"}}
	lifecycle := RuntimeLifecycle{Manager: manager, Values: dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "manual",
		"RUNNER_PUBLIC_URL":    "https://manual.example",
		"RUNNER_PUBLIC_PORT":   "443",
	}}
	url, port, err := lifecycle.registrationEndpoint(context.Background())
	if err != nil || url != "https://manual.example" || port != "443" {
		t.Fatalf("manual endpoint = %q %q %v", url, port, err)
	}
	lifecycle.Values = dashboardruntime.Values{
		"CREDIMI_SERVICE_MODE": "cloudflare-managed",
		"RUNNER_DOMAIN":        "runner.example",
	}
	url, port, err = lifecycle.registrationEndpoint(context.Background())
	if err != nil || url != "https://runner.example" || port != "" {
		t.Fatalf("managed endpoint = %q %q %v", url, port, err)
	}
	lifecycle.Values = dashboardruntime.Values{"CREDIMI_SERVICE_MODE": "auto"}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if manager.status.PublicURL != "" || manager.stops != 1 {
		t.Fatalf("auto stop did not clear URL: %#v", manager)
	}
}

func TestRuntimeLifecycleRegisterRunningWaitsForRunnerReadiness(t *testing.T) {
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"connected","devices":[]}`))
		case "/readyz":
			_, _ = w.Write([]byte(`{"service":"credimi-runner","runner_id":"acme/runner","boot_id":"boot-1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer runner.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(runner.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer api.Close()
	lifecycle := RuntimeLifecycle{
		Manager: &lifecycleManager{},
		Values: dashboardruntime.Values{
			"CREDIMI_URL":            api.URL,
			"CREDIMI_USER_API_KEY":   "key",
			"CREDIMI_RUNNER_ID":      "acme/runner",
			"CREDIMI_RUNNER_NAME":    "runner",
			"CREDIMI_SERVICE_MODE":   "manual",
			"CREDIMI_DEVICE_COUNT":   "1",
			"CREDIMI_DEVICE_1_ID":    "acme/runner/ios",
			"CREDIMI_DEVICE_1_TYPE":  "ios_simulator",
			"CREDIMI_DEVICE_1_MODE":  "no_device",
			"RUNNER_PUBLIC_URL":      "https://runner.example",
			"RUNNER_HOST":            host,
			"RUNNER_PORT":            port,
			"CREDIMI_RUNNER_BACKEND": "host",
		},
		GOOS: "darwin",
	}
	if err := lifecycle.RegisterRunning(context.Background()); err != nil {
		t.Fatalf("RegisterRunning() error = %v", err)
	}
}

func TestWaitForRunnerReadyIgnoresDeferredManagedDevice(t *testing.T) {
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"connected","devices":[]}`))
		case "/readyz":
			_, _ = w.Write([]byte(`{"service":"credimi-runner","runner_id":"runner-1","boot_id":"boot-1","device_serial":"192.168.0.241:5555","device_state":"missing"}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer runner.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(runner.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForRunnerReady(context.Background(), runner.Client(), dashboardruntime.Values{
		"CREDIMI_RUNNER_ID":     "runner-1",
		"CREDIMI_RUNNER_SERIAL": "192.168.0.241:5555",
		"CREDIMI_RUNNER_TYPE":   "redroid",
		"RUNNER_HOST":           host,
		"RUNNER_PORT":           port,
	}); err != nil {
		t.Fatal(err)
	}
}
