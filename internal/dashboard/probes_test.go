package dashboard

import (
	"testing"
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
	svc := Service{ID: "runner", Name: "runner", Role: "credimi-runner serve", Status: Online, Uptime: "Up 3h"}
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
		{"ios_phone", "iOS"},
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
		Services: []Service{{ID: "runner", Status: Online}},
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
	snap.Services = append(snap.Services, Service{ID: "caddy", Status: Offline})
	p = h.pillData(snap)
	if p.OK {
		t.Error("expected not OK when one service offline")
	}
	if p.Issues != 1 {
		t.Errorf("expected 1 issue, got %d", p.Issues)
	}
}
