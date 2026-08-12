package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestReadinessFailureExplainsADBUnavailable(t *testing.T) {
	err := ReadinessFailure(dashboardruntime.Values{"CREDIMI_DEVICE_COUNT": "1"}, "127.0.0.1:8050", ErrADBUnavailable, context.DeadlineExceeded)
	if !strings.Contains(err.Error(), "ADB is unavailable") || !errors.Is(err, ErrADBUnavailable) {
		t.Fatalf("ADB readiness error = %v", err)
	}
}

func TestWaitForRunnerReadyReportsMissingPhoneBeforeListenerDeadline(t *testing.T) {
	adbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(adbDir, "adb"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", adbDir)
	previous := hostADBDevices
	hostADBDevices = func(context.Context) (string, error) {
		return "List of devices attached\n", nil
	}
	t.Cleanup(func() { hostADBDevices = previous })

	started := time.Now()
	err := waitForRunnerReady(context.Background(), &http.Client{}, dashboardruntime.Values{
		"CREDIMI_RUNNER_ID":       "acme/runner",
		"CREDIMI_DEVICE_COUNT":    "1",
		"CREDIMI_DEVICE_1_ID":     "acme/runner/phone",
		"CREDIMI_DEVICE_1_TYPE":   "android_phone",
		"CREDIMI_DEVICE_1_MODE":   "usb",
		"CREDIMI_DEVICE_1_SERIAL": "usb-1",
		"RUNNER_HOST":             "127.0.0.1",
		"RUNNER_PORT":             "1",
	}, nil)
	if !errors.Is(err, ErrDeviceMissing) || !strings.Contains(err.Error(), "not available") || !strings.Contains(err.Error(), "acme/runner/phone") || !strings.Contains(err.Error(), "usb-1") {
		t.Fatalf("missing phone readiness error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("missing phone took too long to report: %s", elapsed)
	}
}

func TestWaitForRunnerReadyReportsADBInventoryFailure(t *testing.T) {
	adbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(adbDir, "adb"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", adbDir)
	previous := hostADBDevices
	hostADBDevices = func(context.Context) (string, error) {
		return "", errors.New("adb server unavailable")
	}
	t.Cleanup(func() { hostADBDevices = previous })

	err := waitForRunnerReady(context.Background(), &http.Client{}, dashboardruntime.Values{
		"CREDIMI_RUNNER_ID":       "acme/runner",
		"CREDIMI_DEVICE_COUNT":    "1",
		"CREDIMI_DEVICE_1_ID":     "acme/runner/phone",
		"CREDIMI_DEVICE_1_TYPE":   "android_phone",
		"CREDIMI_DEVICE_1_MODE":   "usb",
		"CREDIMI_DEVICE_1_SERIAL": "usb-1",
		"RUNNER_HOST":             "127.0.0.1",
		"RUNNER_PORT":             "1",
	}, nil)
	if err == nil || !errors.Is(err, ErrADBUnavailable) || !strings.Contains(err.Error(), "ADB is unavailable") || !strings.Contains(err.Error(), "adb server unavailable") {
		t.Fatalf("ADB inventory readiness error = %v", err)
	}
}

func TestReadinessFailureExplainsDeviceStates(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		err        error
	}{
		{"missing", "not available", ErrDeviceMissing},
		{"offline", "offline", ErrDeviceOffline},
		{"unauthorized", "unauthorized", ErrDeviceUnauthorized},
		{"deadline", "inspect runner logs", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := dashboardruntime.Values{}
			if tc.err != nil {
				values = dashboardruntime.Values{"CREDIMI_DEVICE_COUNT": "1", "CREDIMI_RUNNER_ID": "acme/runner", "CREDIMI_DEVICE_1_ID": "acme/runner/device"}
			}
			got := ReadinessFailure(values, "127.0.0.1:8050", tc.err, context.DeadlineExceeded)
			if !strings.Contains(got.Error(), tc.want) {
				t.Fatalf("ReadinessFailure = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRuntimeLifecycleStartKeepsRuntimeWhenReadinessFails(t *testing.T) {
	manager := &lifecycleManager{}
	lifecycle := RuntimeLifecycle{
		Manager: manager,
		Values: dashboardruntime.Values{
			"CREDIMI_RUNNER_ID":     "acme/runner",
			"CREDIMI_DEVICE_COUNT":  "1",
			"CREDIMI_DEVICE_1_ID":   "acme/runner/device",
			"CREDIMI_DEVICE_1_TYPE": "android_phone",
			"CREDIMI_DEVICE_1_MODE": "usb",
			"CREDIMI_SERVICE_MODE":  "manual",
			"RUNNER_PUBLIC_URL":     "https://runner.example",
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

func TestRuntimeLifecycleStartReportsRunnerExitDuringReadiness(t *testing.T) {
	manager := &exitedLifecycleManager{lifecycleManager: lifecycleManager{logs: []dashboardruntime.LogLine{
		{Message: "runner-1 | error: protocol fault (couldn't read status): Connection reset by peer"},
	}}}
	lifecycle := RuntimeLifecycle{
		Manager: manager,
		Values: dashboardruntime.Values{
			"CREDIMI_SERVICE_MODE": "auto",
		},
		GOOS: "linux",
	}

	err := lifecycle.Start(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "runner container exited during startup") || !strings.Contains(err.Error(), "protocol fault") {
		t.Fatalf("Start() error = %v", err)
	}
	if got := manager.status.PublicURL; got != "" {
		t.Fatalf("public URL = %q, want empty", got)
	}
}

type lifecycleManager struct {
	status   dashboardruntime.RuntimeStatus
	logs     []dashboardruntime.LogLine
	quickURL string
	starts   int
	stops    int
}

type failingLifecycleManager struct{ lifecycleManager }

func (m *failingLifecycleManager) Start(context.Context) error { return errors.New("start failed") }

type exitedLifecycleManager struct{ lifecycleManager }

func (m *exitedLifecycleManager) Start(context.Context) error {
	m.starts++
	m.status.Observed = true
	m.status.RunnerRunning = false
	return nil
}

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
func (m *lifecycleManager) QuickTunnelURL(context.Context) (string, error) {
	if m.quickURL != "" {
		return m.quickURL, nil
	}
	return "", errors.New("quick tunnel URL unavailable")
}

func TestRuntimeLifecycleStartRegistersFreshAutoURL(t *testing.T) {
	manager := &lifecycleManager{
		status:   dashboardruntime.RuntimeStatus{PublicURL: "https://stale.trycloudflare.com"},
		quickURL: "https://fresh.trycloudflare.com",
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
			"CREDIMI_URL":          api.URL,
			"CREDIMI_USER_API_KEY": "key",
			"CREDIMI_RUNNER_ID":    "acme/runner",
			"CREDIMI_RUNNER_NAME":  "runner",
			"CREDIMI_SERVICE_MODE": "manual",
			"RUNNER_PUBLIC_URL":    "https://runner.example",
			"CREDIMI_RUNNER_TYPE":  "android_phone",
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

func TestRuntimeLifecycleWaitsForQuickTunnelURL(t *testing.T) {
	attempts := 0
	lifecycle := RuntimeLifecycle{
		Values: dashboardruntime.Values{"CREDIMI_SERVICE_MODE": "auto"},
		QuickTunnelURL: func(context.Context) (string, error) {
			attempts++
			if attempts < 2 {
				return "", errors.New("quick tunnel hostname is not available yet")
			}
			return "https://fresh.trycloudflare.com", nil
		},
	}
	url, port, err := lifecycle.registrationEndpoint(context.Background())
	if err != nil || url != "https://fresh.trycloudflare.com" || port != "" || attempts != 2 {
		t.Fatalf("quick tunnel endpoint = %q %q err=%v attempts=%d", url, port, err, attempts)
	}
}

func TestRuntimeLifecycleQuickTunnelUsesManagerStateAndReportsMissingResolver(t *testing.T) {
	manager := &lifecycleManager{status: dashboardruntime.RuntimeStatus{PublicURL: "https://current.trycloudflare.com"}}
	lifecycle := RuntimeLifecycle{Manager: manager, Values: dashboardruntime.Values{"CREDIMI_SERVICE_MODE": "auto"}}
	url, _, err := lifecycle.registrationEndpoint(context.Background())
	if err != nil || url != "https://current.trycloudflare.com" {
		t.Fatalf("manager quick tunnel endpoint = %q, err=%v", url, err)
	}

	if _, _, err := (RuntimeLifecycle{Values: dashboardruntime.Values{"CREDIMI_SERVICE_MODE": "auto"}}).registrationEndpoint(context.Background()); err == nil || !strings.Contains(err.Error(), "discovery is unavailable") {
		t.Fatalf("missing quick tunnel resolver error = %v", err)
	}
}

func TestRuntimeLifecycleVerifiesQuickTunnelBeforeReturningEndpoint(t *testing.T) {
	attempts := 0
	verified := 0
	lifecycle := RuntimeLifecycle{
		Values: dashboardruntime.Values{"CREDIMI_SERVICE_MODE": "auto", "CREDIMI_RUNNER_ID": "acme/runner"},
		QuickTunnelURL: func(context.Context) (string, error) {
			attempts++
			return "https://same.trycloudflare.com", nil
		},
		VerifyPublicURL: func(context.Context, string) error {
			verified++
			if verified == 1 {
				return errors.New("public proxy is still starting")
			}
			return nil
		},
	}
	url, _, err := lifecycle.registrationEndpoint(context.Background())
	if err != nil || url != "https://same.trycloudflare.com" {
		t.Fatalf("verified quick tunnel endpoint = %q, err=%v", url, err)
	}
	if attempts != 2 || verified != 2 {
		t.Fatalf("quick tunnel verification attempts=%d verified=%d", attempts, verified)
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
			"CREDIMI_URL":           api.URL,
			"CREDIMI_USER_API_KEY":  "key",
			"CREDIMI_RUNNER_ID":     "acme/runner",
			"CREDIMI_RUNNER_NAME":   "runner",
			"CREDIMI_SERVICE_MODE":  "manual",
			"CREDIMI_DEVICE_COUNT":  "1",
			"CREDIMI_DEVICE_1_ID":   "acme/runner/ios",
			"CREDIMI_DEVICE_1_TYPE": "ios_simulator",
			"CREDIMI_DEVICE_1_MODE": "no_device",
			"RUNNER_PUBLIC_URL":     "https://runner.example",
			"RUNNER_HOST":           host,
			"RUNNER_PORT":           port,
		},
		GOOS: "darwin",
	}
	if err := lifecycle.RegisterRunning(context.Background()); err != nil {
		t.Fatalf("RegisterRunning() error = %v", err)
	}
}

func TestRuntimeLifecycleReadinessAndRestartFailuresRemainExplicit(t *testing.T) {
	readinessErr := errors.New("runner is not ready")
	lifecycle := RuntimeLifecycle{
		Manager:   &lifecycleManager{},
		WaitReady: func(context.Context, dashboardruntime.Values) error { return readinessErr },
	}
	if err := lifecycle.RegisterRunning(context.Background()); !errors.Is(err, readinessErr) {
		t.Fatalf("RegisterRunning error = %v", err)
	}
	if err := lifecycle.Restart(context.Background(), nil); !errors.Is(err, readinessErr) || !strings.Contains(err.Error(), "runtime remains running") {
		t.Fatalf("Restart error = %v", err)
	}

	stopErr := errors.New("stop failed")
	manager := &lifecycleManagerWithStopError{lifecycleManager: lifecycleManager{}, err: stopErr}
	if err := (RuntimeLifecycle{Manager: manager}).Restart(context.Background(), nil); !errors.Is(err, stopErr) {
		t.Fatalf("restart stop error = %v", err)
	}
}

type lifecycleManagerWithStopError struct {
	lifecycleManager
	err error
}

func (m *lifecycleManagerWithStopError) Stop(context.Context) error {
	m.stops++
	return m.err
}

func TestRuntimeLifecycleRegistersRunnerBeforeDevices(t *testing.T) {
	var paths []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	lifecycle := RuntimeLifecycle{
		Values: dashboardruntime.Values{
			"CREDIMI_URL":                 api.URL,
			"CREDIMI_USER_API_KEY":        "key",
			"CREDIMI_RUNNER_ID":           "acme/runner",
			"CREDIMI_RUNNER_NAME":         "runner",
			"CREDIMI_RUNNER_ORGANIZATION": "acme",
			"CREDIMI_SERVICE_MODE":        "manual",
			"RUNNER_PUBLIC_URL":           "https://runner.example",
			"CREDIMI_DEVICE_COUNT":        "1",
			"CREDIMI_DEVICE_1_ID":         "acme/runner/phone",
			"CREDIMI_DEVICE_1_NAME":       "Phone",
			"CREDIMI_DEVICE_1_TYPE":       "android_phone",
			"CREDIMI_DEVICE_1_MODE":       "usb",
			"CREDIMI_DEVICE_1_SERIAL":     "usb-1",
		},
		GOOS:      "darwin",
		WaitReady: func(context.Context, dashboardruntime.Values) error { return nil },
	}
	if err := lifecycle.RegisterRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(paths, ","), "/api/mobile-runner,/api/mobile-device,/api/mobile-device/reconcile"; got != want {
		t.Fatalf("registration order = %q, want %q", got, want)
	}
}

func TestRuntimeLifecycleRunnerRegistrationFailureStopsDeviceRegistration(t *testing.T) {
	var paths []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		http.Error(w, "runner registration failed", http.StatusBadGateway)
	}))
	defer api.Close()
	lifecycle := RuntimeLifecycle{
		Values: dashboardruntime.Values{
			"CREDIMI_URL":          api.URL,
			"CREDIMI_USER_API_KEY": "key",
			"CREDIMI_RUNNER_ID":    "acme/runner",
			"CREDIMI_RUNNER_NAME":  "runner",
			"CREDIMI_SERVICE_MODE": "manual",
			"RUNNER_PUBLIC_URL":    "https://runner.example",
			"CREDIMI_DEVICE_COUNT": "1",
			"CREDIMI_DEVICE_1_ID":  "acme/runner/phone",
		},
		GOOS:      "darwin",
		WaitReady: func(context.Context, dashboardruntime.Values) error { return nil },
	}
	if err := lifecycle.RegisterRunning(context.Background()); err == nil {
		t.Fatal("runner registration unexpectedly succeeded")
	}
	if got, want := strings.Join(paths, ","), "/api/mobile-runner"; got != want {
		t.Fatalf("registration after runner failure = %q, want %q", got, want)
	}
}

func TestRuntimeLifecycleRegisterReportsInvalidAndFailedDeviceRegistration(t *testing.T) {
	baseValues := dashboardruntime.Values{
		"CREDIMI_USER_API_KEY":        "key",
		"CREDIMI_RUNNER_ID":           "acme/runner",
		"CREDIMI_RUNNER_NAME":         "runner",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
		"CREDIMI_SERVICE_MODE":        "manual",
		"RUNNER_PUBLIC_URL":           "https://runner.example",
		"CREDIMI_DEVICE_COUNT":        "1",
		"CREDIMI_DEVICE_1_NAME":       "Phone",
		"CREDIMI_DEVICE_1_TYPE":       "android_phone",
		"CREDIMI_DEVICE_1_MODE":       "usb",
		"CREDIMI_DEVICE_1_SERIAL":     "usb-1",
	}
	t.Run("invalid ID", func(t *testing.T) {
		var paths []string
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}))
		defer api.Close()
		values := dashboardruntime.Values{}
		for key, value := range baseValues {
			values[key] = value
		}
		values["CREDIMI_URL"] = api.URL
		if err := (RuntimeLifecycle{Values: values, GOOS: "darwin", WaitReady: func(context.Context, dashboardruntime.Values) error { return nil }}).Register(context.Background()); err == nil || !strings.Contains(err.Error(), "CREDIMI_DEVICE_1_ID is required") {
			t.Fatalf("invalid device ID error = %v", err)
		}
		if got, want := strings.Join(paths, ","), "/api/mobile-runner"; got != want {
			t.Fatalf("invalid ID calls = %q, want %q", got, want)
		}
	})

	t.Run("device API failure", func(t *testing.T) {
		var paths []string
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			if r.URL.Path == "/api/mobile-device" {
				http.Error(w, "device unavailable", http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer api.Close()
		values := dashboardruntime.Values{}
		for key, value := range baseValues {
			values[key] = value
		}
		values["CREDIMI_URL"] = api.URL
		values["CREDIMI_DEVICE_1_ID"] = "acme/runner/phone"
		err := (RuntimeLifecycle{Values: values, GOOS: "darwin", WaitReady: func(context.Context, dashboardruntime.Values) error { return nil }}).Register(context.Background())
		if err == nil || !strings.Contains(err.Error(), "register device") || !strings.Contains(err.Error(), "device unavailable") {
			t.Fatalf("device registration error = %v", err)
		}
		if got, want := strings.Join(paths, ","), "/api/mobile-runner,/api/mobile-device"; got != want {
			t.Fatalf("device failure calls = %q, want %q", got, want)
		}
	})
}

func TestRuntimeLifecycleRegisterReportsDeviceReconcileFailure(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mobile-device/reconcile" {
			http.Error(w, "reconcile unavailable", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	values := dashboardruntime.Values{
		"CREDIMI_URL":                 api.URL,
		"CREDIMI_USER_API_KEY":        "key",
		"CREDIMI_RUNNER_ID":           "acme/runner",
		"CREDIMI_RUNNER_NAME":         "runner",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
		"CREDIMI_SERVICE_MODE":        "manual",
		"RUNNER_PUBLIC_URL":           "https://runner.example",
		"CREDIMI_DEVICE_COUNT":        "1",
		"CREDIMI_DEVICE_1_ID":         "acme/runner/phone",
		"CREDIMI_DEVICE_1_NAME":       "Phone",
		"CREDIMI_DEVICE_1_TYPE":       "android_phone",
		"CREDIMI_DEVICE_1_MODE":       "usb",
		"CREDIMI_DEVICE_1_SERIAL":     "usb-1",
	}
	err := (RuntimeLifecycle{Values: values, GOOS: "darwin", WaitReady: func(context.Context, dashboardruntime.Values) error { return nil }}).Register(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reconcile configured devices") || !strings.Contains(err.Error(), "reconcile unavailable") {
		t.Fatalf("device reconcile error = %v", err)
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
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForRunnerReadyUsesDefaultHTTPClient(t *testing.T) {
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"connected","devices":[]}`))
		case "/readyz":
			_, _ = w.Write([]byte(`{"service":"credimi-runner","runner_id":"runner-1","boot_id":"boot-1"}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer runner.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(runner.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WaitForRunnerReady(context.Background(), dashboardruntime.Values{"CREDIMI_RUNNER_ID": "runner-1", "RUNNER_HOST": host, "RUNNER_PORT": port}); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForRunnerReadyDoesNotBlockOnNewEmulator(t *testing.T) {
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			http.Error(w, "ADB is not available until the emulator starts", http.StatusServiceUnavailable)
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
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForRunnerReadyReportsMissingPhysicalDeviceImmediately(t *testing.T) {
	// Keep this test independent of the host running the suite. The readiness
	// path intentionally checks host ADB before waiting for the runner listener,
	// so provide the smallest deterministic ADB inventory for the missing-phone
	// scenario instead of relying on a developer's installed adb binary.
	adbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(adbDir, "adb"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", adbDir)
	previous := hostADBDevices
	hostADBDevices = func(context.Context) (string, error) {
		return "List of devices attached\n", nil
	}
	t.Cleanup(func() { hostADBDevices = previous })

	err := waitForRunnerReady(context.Background(), &http.Client{}, dashboardruntime.Values{
		"CREDIMI_RUNNER_ID":       "runner-1",
		"CREDIMI_DEVICE_COUNT":    "1",
		"CREDIMI_DEVICE_1_ID":     "runner-1/phone",
		"CREDIMI_DEVICE_1_TYPE":   "android_phone",
		"CREDIMI_DEVICE_1_MODE":   "usb",
		"CREDIMI_DEVICE_1_SERIAL": "usb-1",
		"RUNNER_HOST":             "127.0.0.1",
		"RUNNER_PORT":             "1",
	}, nil)
	if !errors.Is(err, ErrDeviceMissing) || !strings.Contains(err.Error(), "configured device is not available") || !strings.Contains(err.Error(), "runner-1/phone") {
		t.Fatalf("missing physical device error = %v", err)
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
		case "/api/mobile-device/reconcile":
			var reconcile dashboardruntime.ReconcileDevicesRequest
			if err := json.NewDecoder(request.Body).Decode(&reconcile); err != nil {
				t.Fatal(err)
			}
			if len(reconcile.DeviceIDs) != 2 {
				t.Fatalf("reconcile request = %#v", reconcile)
			}
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
	if err := (RuntimeLifecycle{}).Register(context.Background()); err == nil || !strings.Contains(err.Error(), "missing Credimi API key") {
		t.Fatalf("nil manager registration error = %v", err)
	}

	failing := RuntimeLifecycle{Manager: &failingLifecycleManager{}, Values: dashboardruntime.Values{}, GOOS: "linux"}
	if err := failing.Start(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "start failed") {
		t.Fatalf("failed manager start error = %v", err)
	}

	progressManager := &progressLifecycleManager{}
	progressLifecycle := RuntimeLifecycle{
		Manager: progressManager,
		Values: dashboardruntime.Values{
			"CREDIMI_URL": "https://credimi.example", "CREDIMI_USER_API_KEY": "key", "CREDIMI_RUNNER_ID": "acme/runner",
			"CREDIMI_SERVICE_MODE": "manual", "RUNNER_PUBLIC_URL": "https://runner.example",
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
