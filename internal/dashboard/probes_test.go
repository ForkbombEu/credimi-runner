package dashboard

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

func TestProbeAndroid_ParseOutput(t *testing.T) {
	// Simulated output from "adb devices -l"
	simulated := `List of devices attached
emulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a transport_id:1
192.168.1.42:38349     device product:husky model:Pixel_8_Pro device:husky transport_id:2
ABCD12345              offline product:oriole model:Pixel_6 device:oriole transport_id:3
`

	// We can't easily unit test the probe function directly since it shells out.
	// Instead, test that the field registry and types are consistent.
	_ = simulated
}

func TestStatusString(t *testing.T) {
	statuses := []Status{Online, Degraded, Offline, Idle}
	labels := []string{"Online", "Degraded", "Offline", "Idle"}

	for i, s := range statuses {
		if statusLabel(s) != labels[i] {
			t.Errorf("statusLabel(%s) = %q, want %q", s, statusLabel(s), labels[i])
		}
	}
}

func TestServiceTypes(t *testing.T) {
	svc := Service{ID: "runner", Status: Online}
	if svc.ID != "runner" {
		t.Error("service ID mismatch")
	}
	if svc.Status != Online {
		t.Error("service status mismatch")
	}
}

func TestDeviceTypes(t *testing.T) {
	tests := []struct {
		typ  string
		kind string
	}{
		{"android_emulator", "Emulator"},
		{"ios_simulator", "iOS"},
		{"redroid", "Redroid"},
		{"android_phone", "Android"},
		{"unknown", "Android"},
	}

	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			if got := deviceKind(tt.typ); got != tt.kind {
				t.Errorf("deviceKind(%q) = %q, want %q", tt.typ, got, tt.kind)
			}
		})
	}
}

func TestModeLabel(t *testing.T) {
	tests := []struct{ mode, label string }{
		{"wifi", "Wi-Fi ADB"},
		{"usb", "USB"},
		{"emulator", "KVM"},
		{"simulator", "simctl"},
		{"something", "something"},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			if got := modeLabel(tt.mode); got != tt.label {
				t.Errorf("modeLabel(%q) = %q, want %q", tt.mode, got, tt.label)
			}
		})
	}
}

func TestPillData(t *testing.T) {
	h := &Hub{}
	// All healthy
	snap := Snapshot{
		Services: []Service{{ID: "runner", Status: Online, Expected: true, Critical: true}},
		Devices:  []Device{{Serial: "a", Status: Online}},
	}
	p := h.pillData(snap)
	if !p.OK {
		t.Error("expected OK when all healthy")
	}
	if p.Label != "All healthy" {
		t.Errorf("expected 'All healthy', got %q", p.Label)
	}

	// One offline
	snap.Services = append(snap.Services, Service{ID: "caddy", Status: Offline, Expected: true, Critical: true})
	p = h.pillData(snap)
	if p.OK {
		t.Error("expected not OK when one service offline")
	}
	if p.Issues != 1 {
		t.Errorf("expected 1 issue, got %d", p.Issues)
	}
}

func TestProbeAndroidWithFakeADB(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "adb"), `#!/bin/sh
case "$*" in
  "devices -l")
    printf '%s\n' 'List of devices attached' 'emulator-5554 device product:sdk model:Pixel_Emu device:emu transport_id:1' '192.168.1.42:38349 device product:husky model:Pixel_8 device:husky transport_id:2' 'ABCD123 offline product:oriole model:Pixel_6 device:oriole transport_id:3'
    ;;
  *"dumpsys battery"*)
    printf 'level: 87\n'
    ;;
  *"dumpsys cpuinfo"*)
    printf ' 12.5%% TOTAL: 1%% user + 1%% kernel\n'
    ;;
esac
`)
	t.Setenv("PATH", bin)

	devices := probeAndroid(context.Background())
	if len(devices) != 3 {
		t.Fatalf("devices len = %d: %#v", len(devices), devices)
	}
	if devices[0].Type != "android_emulator" || devices[0].Mode != "emulator" || devices[0].Status != Online {
		t.Fatalf("emulator device = %#v", devices[0])
	}
	if devices[1].Mode != "wifi" || devices[1].Battery != 87 || devices[1].CPU != 12 {
		t.Fatalf("wifi device = %#v", devices[1])
	}
	if devices[2].Status != Offline || devices[2].Name != "Pixel 6" {
		t.Fatalf("offline device = %#v", devices[2])
	}
}

func TestProbeIOSWithFakeXcrun(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "xcrun"), `#!/bin/sh
printf '%s\n' '{"devices":{"com.apple.CoreSimulator.SimRuntime.iOS-18-0":[{"udid":"SIM-1","name":"iPhone 16","state":"Booted"},{"udid":"SIM-2","name":"iPad","state":"Shutdown"}]}}'
`)
	t.Setenv("PATH", bin)

	devices := probeIOS(context.Background())
	if len(devices) != 2 {
		t.Fatalf("devices len = %d: %#v", len(devices), devices)
	}
	if devices[0].Status != Online || devices[0].OS != "iOS 18 0 · Sim" || devices[0].Battery != 100 {
		t.Fatalf("booted simulator = %#v", devices[0])
	}
	if devices[1].Status != Offline {
		t.Fatalf("shutdown simulator = %#v", devices[1])
	}
}

func TestProbeServicesWithFakeDocker(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "docker"), `#!/bin/sh
printf '%s\n' '{"Service":"runner","State":"running","Status":"Up 10 seconds","Image":"runner:local"}' '{"Service":"caddy","State":"paused","Status":"Paused","Image":"caddy:local"}' '{"Service":"cloudflared","State":"exited","Status":"Exited","Image":"cloudflared:local"}'
`)
	t.Setenv("PATH", bin)

	plan := dashboardruntime.BuildRuntimePlan(t.TempDir(), dashboardruntime.Values{
		"CREDIMI_RUNNER_BACKEND": "container",
		"CREDIMI_SERVICE_MODE":   "auto",
	})
	services := probeServices(context.Background(), t.TempDir(), plan, true)
	if len(services) != 4 {
		t.Fatalf("services len = %d", len(services))
	}
	if services[0].Status != Online || services[0].Image != "runner:local" || services[0].Uptime != "Up 10 seconds" {
		t.Fatalf("runner service = %#v", services[0])
	}
	if services[1].Status != Degraded {
		t.Fatalf("caddy service = %#v", services[1])
	}
	if services[2].Status != Offline || services[2].ID != "tunnel" {
		t.Fatalf("tunnel service = %#v", services[2])
	}
	if services[3].ID != "temporal" || services[3].Critical {
		t.Fatalf("temporal service = %#v", services[3])
	}
}

func TestProbeServicesWithoutDocker(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	plan := dashboardruntime.BuildRuntimePlan("", dashboardruntime.Values{
		"CREDIMI_RUNNER_BACKEND": "host",
		"CREDIMI_SERVICE_MODE":   "manual",
	})
	services := probeServices(context.Background(), "", plan, false)
	for _, svc := range services {
		if svc.ID == "temporal" {
			continue
		}
		if svc.Status != Offline {
			t.Fatalf("service should be offline without docker: %#v", svc)
		}
	}
}

func TestRunAndDialTemporal(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "ok"), "#!/bin/sh\nprintf works\n")
	t.Setenv("PATH", bin)
	out, err := run(context.Background(), "ok")
	if err != nil || out != "works" {
		t.Fatalf("run = %q, %v", out, err)
	}

	if got := dialTemporal(""); got != Idle {
		t.Fatalf("empty temporal = %s", got)
	}
	if runtime.GOOS != "js" {
		if got := dialTemporal("127.0.0.1:1"); got != Offline {
			t.Fatalf("closed temporal = %s", got)
		}
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if !strings.HasPrefix(body, "#!") {
		t.Fatalf("test executable %s has no shebang", path)
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
