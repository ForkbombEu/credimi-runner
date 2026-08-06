package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const systemMetricsLogName = "system-metrics.jsonl"

// SystemMetrics is deliberately small and JSON-friendly: each record is one
// host sample that can be inspected with standard command-line tools.
type SystemMetrics struct {
	Timestamp       int64    `json:"timestamp"`
	CPUPercent      float64  `json:"cpu_percent"`
	CPUTemperature  *float64 `json:"cpu_temperature_c,omitempty"`
	CPUUndervoltage *bool    `json:"cpu_undervoltage,omitempty"`
	RAMPercent      float64  `json:"ram_percent"`
	DiskUsedPercent float64  `json:"disk_used_percent"`
	DiskFreeBytes   uint64   `json:"disk_free_bytes"`
	DiskActivityKiB float64  `json:"disk_activity_kib_s"`
}

type systemMetricTotals struct{ total, idle, diskSectors uint64 }

type SystemMonitor struct {
	mu        sync.RWMutex
	samples   []SystemMetrics
	previous  systemMetricTotals
	lastAt    time.Time
	logPath   string
	interval  time.Duration
	logEvery  time.Duration
	lastPrune time.Time
	cancel    context.CancelFunc
}

// NewSystemMonitor starts a local host sampler. Debug-verbose intentionally
// uses the requested 500 ms cadence for both live and persisted samples.
func NewSystemMonitor(parent context.Context, configDir string, debugVerbose bool) *SystemMonitor {
	interval, logEvery := 2*time.Second, 10*time.Second
	if debugVerbose {
		interval, logEvery = 500*time.Millisecond, 500*time.Millisecond
	}
	ctx, cancel := context.WithCancel(parent)
	m := &SystemMonitor{logPath: filepath.Join(configDir, systemMetricsLogName), interval: interval, logEvery: logEvery, cancel: cancel}
	m.prune(time.Now().UTC())
	go m.run(ctx)
	return m
}

func (m *SystemMonitor) Close() {
	if m != nil && m.cancel != nil {
		m.cancel()
	}
}

func (m *SystemMonitor) run(ctx context.Context) {
	nextLog := time.Time{}
	for {
		m.sample(time.Now().UTC(), !time.Now().Before(nextLog))
		if nextLog.IsZero() || !time.Now().Before(nextLog) {
			nextLog = time.Now().Add(m.logEvery)
		}
		timer := time.NewTimer(m.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *SystemMonitor) sample(now time.Time, persist bool) {
	metric, totals := collectSystemMetrics(now, m.previous, m.lastAt)
	m.mu.Lock()
	m.previous, m.lastAt = totals, now
	m.samples = append(m.samples, metric)
	if len(m.samples) > 180 {
		m.samples = append([]SystemMetrics(nil), m.samples[len(m.samples)-180:]...)
	}
	m.mu.Unlock()
	if persist {
		m.append(metric)
	}
}

func (m *SystemMonitor) append(metric SystemMetrics) {
	if err := os.MkdirAll(filepath.Dir(m.logPath), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(m.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	encoded, err := json.Marshal(metric)
	if err == nil {
		_, _ = file.Write(append(encoded, '\n'))
	}
	if m.lastPrune.IsZero() || time.Since(m.lastPrune) >= time.Hour {
		m.prune(time.Now().UTC())
	}
}

func (m *SystemMonitor) prune(now time.Time) {
	m.lastPrune = now
	file, err := os.Open(m.logPath)
	if err != nil {
		return
	}
	defer file.Close()
	cutoff := now.Add(-24 * time.Hour).Unix()
	kept := make([]SystemMetrics, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var metric SystemMetrics
		if json.Unmarshal(scanner.Bytes(), &metric) == nil && metric.Timestamp >= cutoff {
			kept = append(kept, metric)
		}
	}
	temporary := m.logPath + ".tmp"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	for _, metric := range kept {
		encoded, _ := json.Marshal(metric)
		_, _ = output.Write(append(encoded, '\n'))
	}
	if output.Close() == nil {
		_ = os.Rename(temporary, m.logPath)
	}
}

func (m *SystemMonitor) Live() []SystemMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]SystemMetrics(nil), m.samples...)
}

func (m *SystemMonitor) Hourly() []SystemMetrics {
	file, err := os.Open(m.logPath)
	if err != nil {
		return nil
	}
	defer file.Close()
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	type aggregate struct {
		count                          int
		cpu, ram, disk, activity, temp float64
		temps                          int
		undervoltage                   bool
		voltageKnown                   bool
		free                           uint64
	}
	groups := map[int64]*aggregate{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var metric SystemMetrics
		if json.Unmarshal(scanner.Bytes(), &metric) != nil || metric.Timestamp < cutoff {
			continue
		}
		hour := time.Unix(metric.Timestamp, 0).UTC().Truncate(time.Hour).Unix()
		agg := groups[hour]
		if agg == nil {
			agg = &aggregate{}
			groups[hour] = agg
		}
		agg.count++
		agg.cpu += metric.CPUPercent
		agg.ram += metric.RAMPercent
		agg.disk += metric.DiskUsedPercent
		agg.activity += metric.DiskActivityKiB
		agg.free += metric.DiskFreeBytes
		if metric.CPUTemperature != nil {
			agg.temp += *metric.CPUTemperature
			agg.temps++
		}
		if metric.CPUUndervoltage != nil {
			agg.voltageKnown = true
			agg.undervoltage = agg.undervoltage || *metric.CPUUndervoltage
		}
	}
	result := make([]SystemMetrics, 0, len(groups))
	for hour, agg := range groups {
		count := float64(agg.count)
		metric := SystemMetrics{Timestamp: hour, CPUPercent: agg.cpu / count, RAMPercent: agg.ram / count, DiskUsedPercent: agg.disk / count, DiskActivityKiB: agg.activity / count, DiskFreeBytes: agg.free / uint64(agg.count)}
		if agg.temps > 0 {
			value := agg.temp / float64(agg.temps)
			metric.CPUTemperature = &value
		}
		if agg.voltageKnown {
			value := agg.undervoltage
			metric.CPUUndervoltage = &value
		}
		result = append(result, metric)
	}
	for i := range result {
		for j := i + 1; j < len(result); j++ {
			if result[j].Timestamp < result[i].Timestamp {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result
}

func (m *SystemMonitor) Interval() time.Duration { return m.interval }

func collectSystemMetrics(now time.Time, previous systemMetricTotals, lastAt time.Time) (SystemMetrics, systemMetricTotals) {
	metric := SystemMetrics{Timestamp: now.Unix()}
	totals := readSystemTotals()
	if totals.total > previous.total && totals.idle >= previous.idle {
		metric.CPUPercent = 100 * float64((totals.total-previous.total)-(totals.idle-previous.idle)) / float64(totals.total-previous.total)
	}
	if !lastAt.IsZero() && totals.diskSectors >= previous.diskSectors {
		metric.DiskActivityKiB = float64(totals.diskSectors-previous.diskSectors) / 2 / now.Sub(lastAt).Seconds()
	}
	metric.RAMPercent = readRAMPercent()
	metric.CPUTemperature = readCPUTemperature()
	metric.CPUUndervoltage = readCPUUndervoltage()
	var fs syscall.Statfs_t
	if syscall.Statfs("/", &fs) == nil && fs.Blocks > 0 {
		metric.DiskFreeBytes = fs.Bavail * uint64(fs.Bsize)
		metric.DiskUsedPercent = 100 * float64(fs.Blocks-fs.Bavail) / float64(fs.Blocks)
	}
	return metric, totals
}

func readSystemTotals() systemMetricTotals {
	var totals systemMetricTotals
	if raw, err := os.ReadFile("/proc/stat"); err == nil {
		fields := strings.Fields(strings.SplitN(string(raw), "\n", 2)[0])
		for _, value := range fields[1:] {
			parsed, _ := strconv.ParseUint(value, 10, 64)
			totals.total += parsed
		}
		if len(fields) > 4 {
			totals.idle, _ = strconv.ParseUint(fields[4], 10, 64)
		}
	}
	if raw, err := os.ReadFile("/proc/diskstats"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 || strings.HasPrefix(fields[2], "loop") || strings.HasPrefix(fields[2], "ram") {
				continue
			}
			reads, _ := strconv.ParseUint(fields[5], 10, 64)
			writes, _ := strconv.ParseUint(fields[9], 10, 64)
			totals.diskSectors += reads + writes
		}
	}
	return totals
}

func readRAMPercent() float64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var total, available float64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseFloat(fields[1], 64)
		if fields[0] == "MemTotal:" {
			total = value
		}
		if fields[0] == "MemAvailable:" {
			available = value
		}
	}
	if total == 0 {
		return 0
	}
	return 100 * (total - available) / total
}

func readCPUTemperature() *float64 {
	paths, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil {
			continue
		}
		if value > 1000 {
			value /= 1000
		}
		if value > 0 && value < 150 {
			return &value
		}
	}
	return nil
}

func readCPUUndervoltage() *bool {
	for _, path := range []string{"/sys/devices/platform/soc/soc:firmware/get_throttled", "/sys/devices/platform/soc/firmware/get_throttled"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(string(raw), "0x")), 16, 64)
		if err != nil {
			continue
		}
		result := value&(1<<0|1<<16) != 0
		return &result
	}
	return nil
}

func (m SystemMetrics) String() string {
	return fmt.Sprintf("cpu=%.1f%% ram=%.1f%%", m.CPUPercent, m.RAMPercent)
}
