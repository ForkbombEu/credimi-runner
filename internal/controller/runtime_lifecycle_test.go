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

type lifecycleRoundTripFunc func(*http.Request) (*http.Response, error)

func (f lifecycleRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

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
			"CREDIMI_RUNNER_ID":      "acme/runner",
			"CREDIMI_DEVICE_COUNT":   "1",
			"CREDIMI_DEVICE_1_ID":    "acme/runner/device",
			"CREDIMI_DEVICE_1_TYPE":  "android_phone",
			"CREDIMI_DEVICE_1_MODE":  "usb",
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

type failingLifecycleManager struct{ lifecycleManager }

func (m *failingLifecycleManager) Start(context.Context) error { return errors.New("start failed") }

type progressLifecycleManager struct {
	lifecycleManager
	progressCalls int
}

func (m *progressLifecycleManager) StartWithProgress(ctx context.Context, progress func(string)) error {
	m.progressCalls++
	if progress != nil {
		progress("pulling runtime")
	}
	return m.Start(ctx)
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
			_, _ = w.Write([]byte(`{"service":"credimi-runner","runner_id":"runner-1","boot_id":"boot-1","devices":{"runner-1/redroid":{"serial":"192.168.0.241:5555","state":"missing","ready":false}}}`))
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
		"CREDIMI_RUNNER_ID":       "runner-1",
		"CREDIMI_DEVICE_COUNT":    "1",
		"CREDIMI_DEVICE_1_ID":     "runner-1/redroid",
		"CREDIMI_DEVICE_1_TYPE":   "redroid",
		"CREDIMI_DEVICE_1_MODE":   "no_device",
		"CREDIMI_DEVICE_1_SERIAL": "192.168.0.241:5555",
		"RUNNER_HOST":             host,
		"RUNNER_PORT":             port,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForRunnerReadyDoesNotBlockOnNewEmulator(t *testing.T) {
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"connected","devices":[]}`))
		case "/readyz":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"service":"credimi-runner","runner_id":"runner-1","boot_id":"boot-1","devices":{"runner-1/emulator":{"state":"missing","ready":false}}}`))
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
		"CREDIMI_RUNNER_ID": "runner-1", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "runner-1/emulator", "CREDIMI_DEVICE_1_TYPE": "android_emulator", "CREDIMI_DEVICE_1_MODE": "emulator", "RUNNER_HOST": host, "RUNNER_PORT": port,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeLifecycleRegisterRegistersConfiguredDevices(t *testing.T) {
	manager := &lifecycleManager{}
	var runner dashboardruntime.RegisterRunnerRequest
	var devices []dashboardruntime.RegisterDeviceRequest
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/mobile-runner":
			if err := json.NewDecoder(request.Body).Decode(&runner); err != nil {
				t.Fatal(err)
			}
		case "/api/mobile-device":
			var device dashboardruntime.RegisterDeviceRequest
			if err := json.NewDecoder(request.Body).Decode(&device); err != nil {
				t.Fatal(err)
			}
			devices = append(devices, device)
		default:
			http.NotFound(w, request)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer api.Close()

	lifecycle := RuntimeLifecycle{
		Manager: manager,
		Values: dashboardruntime.Values{
			"CREDIMI_URL":                 api.URL,
			"CREDIMI_USER_API_KEY":        "key",
			"CREDIMI_RUNNER_ID":           "acme/runner",
			"CREDIMI_RUNNER_NAME":         "Lab Runner",
			"CREDIMI_RUNNER_ORGANIZATION": "acme",
			"CREDIMI_SERVICE_MODE":        "manual",
			"RUNNER_PUBLIC_URL":           "https://runner.example",
			"RUNNER_PUBLIC_PORT":          "443",
			"CREDIMI_DEVICE_COUNT":        "2",
			"CREDIMI_DEVICE_1_ID":         "acme/runner/pixel",
			"CREDIMI_DEVICE_1_NAME":       "Pixel",
			"CREDIMI_DEVICE_1_TYPE":       "android_phone",
			"CREDIMI_DEVICE_1_MODE":       "usb",
			"CREDIMI_DEVICE_1_SERIAL":     "usb-1",
			"CREDIMI_DEVICE_2_ID":         "acme/runner/simulator",
			"CREDIMI_DEVICE_2_NAME":       "Simulator",
			"CREDIMI_DEVICE_2_TYPE":       "ios_simulator",
			"CREDIMI_DEVICE_2_MODE":       "no_device",
		},
	}
	if err := lifecycle.Register(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.IP != "https://runner.example" || runner.Port != "443" || runner.Name != "Lab Runner" {
		t.Fatalf("runner registration = %#v", runner)
	}
	if manager.status.PublicURL != "https://runner.example" {
		t.Fatalf("public URL = %q", manager.status.PublicURL)
	}
	if len(devices) != 2 || devices[0].DeviceID != "acme/runner/pixel" || devices[0].Serial != "usb-1" || devices[1].DeviceID != "acme/runner/simulator" {
		t.Fatalf("device registrations = %#v", devices)
	}
}

func TestRuntimeLifecycleGuardsManagerFailuresAndEndpoints(t *testing.T) {
	if err := (RuntimeLifecycle{}).Start(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "manager unavailable") {
		t.Fatalf("nil manager start error = %v", err)
	}
	if err := (RuntimeLifecycle{}).Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "manager unavailable") {
		t.Fatalf("nil manager stop error = %v", err)
	}
	if err := (RuntimeLifecycle{}).Register(context.Background()); err == nil || !strings.Contains(err.Error(), "manager unavailable") {
		t.Fatalf("nil manager registration error = %v", err)
	}

	failing := RuntimeLifecycle{Manager: &failingLifecycleManager{}, Values: dashboardruntime.Values{"CREDIMI_RUNNER_BACKEND": "container"}, GOOS: "linux"}
	if err := failing.Start(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "start failed") {
		t.Fatalf("failed manager start error = %v", err)
	}

	progressManager := &progressLifecycleManager{}
	progressLifecycle := RuntimeLifecycle{
		Manager: progressManager,
		Values: dashboardruntime.Values{
			"CREDIMI_URL": "https://credimi.example", "CREDIMI_USER_API_KEY": "key", "CREDIMI_RUNNER_ID": "acme/runner",
			"CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://runner.example", "CREDIMI_RUNNER_BACKEND": "container",
		},
		GOOS: "linux", WaitReady: func(context.Context, dashboardruntime.Values) error { return nil },
		HTTPClient: &http.Client{Transport: lifecycleRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/api/mobile-runner" {
				return nil, errors.New("unexpected path: " + request.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusNoContent, Status: "204 No Content", Header: make(http.Header), Body: http.NoBody}, nil
		})},
	}
	var progress []string
	if err := progressLifecycle.Start(context.Background(), func(message string) { progress = append(progress, message) }); err != nil {
		t.Fatal(err)
	}
	if progressManager.progressCalls != 1 || progressManager.starts != 1 || len(progress) != 1 {
		t.Fatalf("progress start manager=%#v progress=%v", progressManager, progress)
	}

	manager := &lifecycleManager{}
	for name, values := range map[string]dashboardruntime.Values{
		"manual":             {"CREDIMI_SERVICE_MODE": "manual"},
		"cloudflare-managed": {"CREDIMI_SERVICE_MODE": "cloudflare-managed"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := (RuntimeLifecycle{Manager: manager, Values: values}).registrationEndpoint(context.Background())
			if err == nil {
				t.Fatal("expected missing endpoint configuration error")
			}
		})
	}
}
