package runtime

import (
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func TestSelectBackendByHostPlatform(t *testing.T) {
	cases := []struct {
		name string
		goos string
		want Backend
		err  bool
	}{
		{name: "linux", goos: "linux", want: Container},
		{name: "macos", goos: "darwin", want: Native},
		{name: "windows unsupported", goos: "windows", err: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Select(c.goos)
			if c.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("backend=%q err=%v, want %q", got, err, c.want)
			}
		})
	}
}

func TestValidateDeviceTypesSeparatesPlatformCapabilityFromPlacement(t *testing.T) {
	for _, test := range []struct {
		name  string
		goos  string
		types []config.DeviceType
		want  bool
	}{
		{"linux android", "linux", []config.DeviceType{config.DeviceAndroidPhysical, config.DeviceAndroidEmulator, config.DeviceRedroid}, true},
		{"mac mixed", "darwin", []config.DeviceType{config.DeviceAndroidEmulator, config.DeviceIOSSimulator}, true},
		{"linux ios", "linux", []config.DeviceType{config.DeviceIOSSimulator}, false},
		{"unsupported host", "freebsd", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidateDeviceTypes(test.types, test.goos) == nil; got != test.want {
				t.Fatalf("ValidateDeviceTypes(%q, %v) success=%t, want %t", test.goos, test.types, got, test.want)
			}
		})
	}
}
