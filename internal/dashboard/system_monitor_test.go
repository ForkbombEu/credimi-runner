package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSystemMonitorHourlyAveragesPersistedSamples(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, systemMetricsLogName)
	now := time.Now().UTC().Truncate(time.Hour)
	temp := 51.0
	voltage := false
	samples := []SystemMetrics{
		{Timestamp: now.Unix(), CPUPercent: 20, CPUTemperature: &temp, CPUUndervoltage: &voltage, RAMPercent: 30, DiskUsedPercent: 40, DiskFreeBytes: 400, DiskActivityKiB: 10},
		{Timestamp: now.Add(10 * time.Second).Unix(), CPUPercent: 40, CPUTemperature: &temp, CPUUndervoltage: &voltage, RAMPercent: 50, DiskUsedPercent: 60, DiskFreeBytes: 200, DiskActivityKiB: 30},
		{Timestamp: now.Add(-25 * time.Hour).Unix(), CPUPercent: 99},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range samples {
		encoded, err := json.Marshal(sample)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	monitor := &SystemMonitor{logPath: path}
	averages := monitor.Hourly()
	if len(averages) != 1 {
		t.Fatalf("hourly sample count = %d", len(averages))
	}
	got := averages[0]
	if got.CPUPercent != 30 || got.RAMPercent != 40 || got.DiskUsedPercent != 50 || got.DiskFreeBytes != 300 || got.DiskActivityKiB != 20 {
		t.Fatalf("hourly average = %#v", got)
	}
	if got.CPUTemperature == nil || *got.CPUTemperature != 51 || got.CPUUndervoltage == nil || *got.CPUUndervoltage {
		t.Fatalf("optional values = %#v", got)
	}
}

func TestSystemMetricsEndpointReturnsLiveAndHourlySamples(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour)
	monitor := &SystemMonitor{interval: 2 * time.Second, samples: []SystemMetrics{{Timestamp: now.Unix(), CPUPercent: 12}}}
	server := &Server{systemMonitor: monitor}
	for _, target := range []string{"/api/system/metrics", "/api/system/metrics?range=hourly"} {
		recorder := httptest.NewRecorder()
		server.systemMetrics(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d", target, recorder.Code)
		}
		var payload struct {
			Samples    []SystemMetrics `json:"samples"`
			IntervalMS int64           `json:"interval_ms"`
		}
		if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.IntervalMS != 2000 {
			t.Fatalf("interval = %d", payload.IntervalMS)
		}
		if target == "/api/system/metrics" && len(payload.Samples) != 1 {
			t.Fatalf("live samples = %#v", payload.Samples)
		}
	}
}

func TestSystemMonitorDebugCadence(t *testing.T) {
	monitor := NewSystemMonitor(t.Context(), t.TempDir(), true)
	t.Cleanup(monitor.Close)
	if monitor.Interval() != 500*time.Millisecond || monitor.logEvery != 500*time.Millisecond {
		t.Fatalf("debug cadence = live %s log %s", monitor.Interval(), monitor.logEvery)
	}
}
