package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemMonitorHourlyAveragesPersistedSamples(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, systemMetricsLogName)
	now := time.Now().UTC().Truncate(time.Hour)
	samples := []SystemMetrics{
		{Timestamp: now.Unix(), CPUPercent: 20, RAMPercent: 30, DiskUsedPercent: 40, DiskFreeBytes: 400, DiskActivityKiB: 10},
		{Timestamp: now.Add(10 * time.Second).Unix(), CPUPercent: 40, RAMPercent: 50, DiskUsedPercent: 60, DiskFreeBytes: 200, DiskActivityKiB: 30},
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
	monitor.prune(now.Add(time.Minute))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(string(contents)), "\n") != 1 || strings.Contains(string(contents), `"cpu_percent":99`) {
		t.Fatalf("pruned metrics = %s", contents)
	}
}

func TestSystemMonitorDebugCadence(t *testing.T) {
	monitor := NewSystemMonitor(t.Context(), t.TempDir(), true)
	t.Cleanup(monitor.Close)
	if monitor.Interval() != 500*time.Millisecond || monitor.logEvery != 500*time.Millisecond {
		t.Fatalf("debug cadence = live %s log %s", monitor.Interval(), monitor.logEvery)
	}
}
