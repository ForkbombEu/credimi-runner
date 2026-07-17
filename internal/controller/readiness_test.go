package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

func TestValidateReadinessRejectsUnrelatedHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not a runner")) }))
	defer server.Close()
	_, err := ValidateReadiness(context.Background(), server.Client(), server.URL, dashboardruntime.Values{})
	if !errors.Is(err, ErrListenerConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateReadinessRequiresExactConfiguredIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"service":"credimi-runner","runner_id":"runner-1","boot_id":"boot-1","device_serial":"serial-1","device_state":"device"}`))
	}))
	defer server.Close()
	_, err := ValidateReadiness(context.Background(), server.Client(), server.URL, dashboardruntime.Values{"CREDIMI_RUNNER_ID": "runner-1", "CREDIMI_RUNNER_SERIAL": "serial-1"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateReadinessClassifiesDeviceFailures(t *testing.T) {
	for _, test := range []struct {
		state string
		want  error
	}{{"missing", ErrDeviceMissing}, {"offline", ErrDeviceOffline}, {"unauthorized", ErrDeviceUnauthorized}, {"other", ErrRunnerNotReady}} {
		t.Run(test.state, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"service":"credimi-runner","runner_id":"runner-1","boot_id":"boot-1","device_serial":"serial-1","device_state":"` + test.state + `"}`))
			}))
			defer server.Close()
			_, err := ValidateReadiness(context.Background(), server.Client(), server.URL, dashboardruntime.Values{"CREDIMI_RUNNER_ID": "runner-1", "CREDIMI_RUNNER_SERIAL": "serial-1"})
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
		})
	}
}

func TestValidateReadinessRejectsMismatchedOrUnavailableRunner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"service":"other","runner_id":"wrong","boot_id":"","device_state":""}`))
	}))
	defer server.Close()
	_, err := ValidateReadiness(context.Background(), server.Client(), server.URL, dashboardruntime.Values{"CREDIMI_RUNNER_ID": "runner-1"})
	if !errors.Is(err, ErrRunnerIdentityMismatch) {
		t.Fatalf("err = %v", err)
	}

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"service":"credimi-runner","runner_id":"runner-2","boot_id":"boot"}`))
	}))
	defer server.Close()
	_, err = ValidateReadiness(context.Background(), server.Client(), server.URL, dashboardruntime.Values{"CREDIMI_RUNNER_ID": "runner-1"})
	if !errors.Is(err, ErrRunnerIdentityMismatch) {
		t.Fatalf("err = %v", err)
	}
}
