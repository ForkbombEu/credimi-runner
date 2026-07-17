package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestADBDeviceState(t *testing.T) {
	dir := t.TempDir()
	adb := filepath.Join(dir, "adb")
	if err := os.WriteFile(adb, []byte("#!/bin/sh\nprintf 'device\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got := adbDeviceState("serial-1"); got != "device" {
		t.Fatalf("adb device state = %q", got)
	}
	if err := os.Remove(adb); err != nil {
		t.Fatal(err)
	}
	if got := adbDeviceState("serial-1"); got != "missing" {
		t.Fatalf("missing adb state = %q", got)
	}
}

func TestReadinessReportsIdentityAndExactDevice(t *testing.T) {
	service := &ReadinessService{Environment: func(key string) string {
		return map[string]string{"CREDIMI_RUNNER_ID": "runner-1", "CREDIMI_RUNNER_BOOT_ID": "boot-1", "CREDIMI_RUNNER_VERSION": "v1", "ANDROID_SERIAL": "device-1"}[key]
	}, DeviceState: func(serial string) string {
		if serial != "device-1" {
			t.Fatalf("serial = %q", serial)
		}
		return "device"
	}}
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var got Readiness
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil || got.RunnerID != "runner-1" || got.DeviceState != "device" {
		t.Fatalf("readiness = %#v err=%v", got, err)
	}
}

func TestReadinessRejectsUnavailableConfiguredDevice(t *testing.T) {
	service := &ReadinessService{Environment: func(key string) string {
		return map[string]string{"CREDIMI_RUNNER_ID": "runner-1", "CREDIMI_RUNNER_BOOT_ID": "boot-1", "ANDROID_SERIAL": "device-1"}[key]
	}, DeviceState: func(string) string { return "offline" }}
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestReadinessUsesConfiguredSerialAndReportsMissingIdentity(t *testing.T) {
	service := &ReadinessService{Environment: func(key string) string {
		return map[string]string{"CREDIMI_RUNNER_SERIAL": "device-2"}[key]
	}, DeviceState: func(serial string) string {
		if serial != "device-2" {
			t.Fatalf("serial = %q", serial)
		}
		return "missing"
	}}
	ready := service.Check()
	if ready.Service != "credimi-runner" || ready.Version != "unknown" || ready.DeviceSerial != "device-2" || ready.DeviceState != "missing" {
		t.Fatalf("readiness = %#v", ready)
	}
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}
