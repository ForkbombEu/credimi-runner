package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Real host probes. Every probe is best-effort: if the underlying binary is not
// installed or errors, the panel renders an "unavailable"/"offline" state rather
// than crashing. Keep timeouts short — these run on a 2s poll loop.
// ─────────────────────────────────────────────────────────────────────────────

type Status string

const (
	Online   Status = "online"
	Degraded Status = "degraded"
	Offline  Status = "offline"
	Idle     Status = "idle"
)

type Device struct {
	Serial  string `json:"serial"`
	Name    string `json:"name"`
	Type    string `json:"type"` // android_phone | android_emulator | ios_simulator
	Mode    string `json:"mode"` // wifi | usb | emulator | simulator
	OS      string `json:"os"`
	Status  Status `json:"status"`
	Battery int    `json:"battery"`
	CPU     int    `json:"cpu"`
	Mem     int    `json:"mem"`
}

type Service struct {
	ID       string
	Name     string
	Role     string
	Image    string
	Status   Status
	Uptime   string
	Expected bool
	Critical bool
	Reason   string
}

type Snapshot struct {
	Services []Service
	Devices  []Device
	Time     time.Time
}

func has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

func run(ctx context.Context, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, name, args...).Output()
	return string(out), err
}

func runWithEnv(ctx context.Context, environment []string, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	command := exec.CommandContext(cctx, name, args...)
	command.Env = environment
	out, err := command.Output()
	return string(out), err
}

// ── ADB ──────────────────────────────────────────────────────────────────────

var adbModelRe = mustCompile(`model:(\S+)`)

func probeAndroid(ctx context.Context) []Device {
	if !has("adb") {
		return nil
	}
	out, err := run(ctx, "adb", "devices", "-l")
	if err != nil {
		return nil
	}
	var devs []Device
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "List of devices") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		serial, state := fields[0], fields[1]
		d := Device{Serial: serial, Type: "android_phone", OS: "Android"}
		switch {
		case strings.Contains(serial, ":"):
			d.Mode = "wifi"
		case strings.HasPrefix(serial, "emulator-"):
			d.Mode, d.Type = "emulator", "android_emulator"
		default:
			d.Mode = "usb"
		}
		switch state {
		case "device":
			d.Status = Online
		case "offline", "unauthorized":
			d.Status = Offline
		default:
			d.Status = Degraded
		}
		if m := adbModelRe.FindStringSubmatch(line); m != nil {
			d.Name = strings.ReplaceAll(m[1], "_", " ")
		}
		if d.Name == "" {
			d.Name = serial
		}
		if d.Status == Online {
			d.Battery = adbBattery(ctx, serial)
			d.CPU, d.Mem = adbLoad(ctx, serial)
		}
		devs = append(devs, d)
	}
	return devs
}

func adbBattery(ctx context.Context, serial string) int {
	out, err := run(ctx, "adb", "-s", serial, "shell", "dumpsys", "battery")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "level:") {
			if n, e := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(l, "level:"))); e == nil {
				return n
			}
		}
	}
	return 0
}

// adbLoad is a cheap best-effort CPU/mem read; returns 0,0 if unavailable.
func adbLoad(ctx context.Context, serial string) (cpu, mem int) {
	out, err := run(ctx, "adb", "-s", serial, "shell", "dumpsys", "cpuinfo")
	if err != nil {
		return 0, 0
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "TOTAL:") {
			if i := strings.Index(l, "%"); i > 0 {
				start := strings.LastIndexByte(l[:i], ' ') + 1
				if n, e := strconv.ParseFloat(l[start:i], 64); e == nil {
					cpu = int(n)
				}
			}
		}
	}
	return cpu, mem
}

// ── iOS simulators (macOS host) ──────────────────────────────────────────────

func probeIOS(ctx context.Context) []Device {
	if !has("xcrun") {
		return nil
	}
	out, err := run(ctx, "xcrun", "simctl", "list", "devices", "booted", "-j")
	if err != nil {
		return nil
	}
	var parsed struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"devices"`
	}
	if json.Unmarshal([]byte(out), &parsed) != nil {
		return nil
	}
	var devs []Device
	for runtime, list := range parsed.Devices {
		os := strings.ReplaceAll(strings.TrimPrefix(runtime, "com.apple.CoreSimulator.SimRuntime."), "-", " ")
		for _, s := range list {
			st := Offline
			if s.State == "Booted" {
				st = Online
			}
			devs = append(devs, Device{
				Serial: s.UDID, Name: s.Name, Type: "ios_simulator", Mode: "simulator",
				OS: os + " · Sim", Status: st, Battery: 100,
			})
		}
	}
	return devs
}

func probeServices(ctx context.Context, values map[string]string, runtimeRunning bool) []Service {
	runner := Service{ID: "runner", Name: "runner", Role: "Credimi Runner", Expected: true, Critical: true, Status: Offline}
	if runtimeRunning {
		runner.Status = Online
	}
	temporal := Service{ID: "temporal", Name: "temporal", Role: values["TEMPORAL_ADDRESS"], Image: "gRPC", Expected: true, Status: dialTemporal(values["TEMPORAL_ADDRESS"]), Uptime: "—"}
	return []Service{runner, temporal}
}

// dialTemporal reports whether the Temporal gRPC frontend accepts a TCP connection.
func dialTemporal(addr string) Status {
	if addr == "" {
		return Idle
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return Offline
	}
	_ = conn.Close()
	return Online
}
