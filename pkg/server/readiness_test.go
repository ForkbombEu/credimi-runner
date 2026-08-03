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
		return map[string]string{"CREDIMI_RUNNER_ID": "runner-1", "CREDIMI_RUNNER_BOOT_ID": "boot-1", "CREDIMI_RUNNER_VERSION": "v1", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "runner-1/device-1", "CREDIMI_DEVICE_1_SERIAL": "device-1"}[key]
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
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil || got.RunnerID != "runner-1" || got.Devices["runner-1/device-1"].State != "device" {
		t.Fatalf("readiness = %#v err=%v", got, err)
	}
}

func TestReadinessRejectsUnavailableConfiguredDevice(t *testing.T) {
	service := &ReadinessService{Environment: func(key string) string {
		return map[string]string{"CREDIMI_RUNNER_ID": "runner-1", "CREDIMI_RUNNER_BOOT_ID": "boot-1", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "runner-1/device-1", "CREDIMI_DEVICE_1_SERIAL": "device-1"}[key]
	}, DeviceState: func(string) string { return "offline" }}
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestReadinessAcceptsDeferredManagedDevice(t *testing.T) {
	deviceStateCalled := false
	service := &ReadinessService{Environment: func(key string) string {
		return map[string]string{
			"CREDIMI_RUNNER_ID":       "runner-1",
			"CREDIMI_RUNNER_BOOT_ID":  "boot-1",
			"CREDIMI_CONTAINER_MODE":  "no_device",
			"CREDIMI_DEVICE_COUNT":    "1",
			"CREDIMI_DEVICE_1_ID":     "runner-1/device-1",
			"CREDIMI_DEVICE_1_SERIAL": "192.168.0.241:5555",
		}[key]
	}, DeviceState: func(string) string {
		deviceStateCalled = true
		return "missing"
	}}
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if deviceStateCalled {
		t.Fatal("no-device readiness must not query ADB")
	}
}

func TestReadinessUsesIndexedSerialAndReportsMissingIdentity(t *testing.T) {
	service := &ReadinessService{Environment: func(key string) string {
		return map[string]string{"CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "runner-1/device-2", "CREDIMI_DEVICE_1_SERIAL": "device-2"}[key]
	}, DeviceState: func(serial string) string {
		if serial != "device-2" {
			t.Fatalf("serial = %q", serial)
		}
		return "missing"
	}}
	ready := service.Check()
	if ready.Service != "credimi-runner" || ready.Version != "unknown" || ready.Devices["runner-1/device-2"].Serial != "device-2" || ready.Devices["runner-1/device-2"].State != "missing" {
		t.Fatalf("readiness = %#v", ready)
	}
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}
