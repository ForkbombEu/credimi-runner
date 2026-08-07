package runtime

import (
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func TestSelectBackend(t *testing.T) {
	cases := []struct {
		name string
		goos string
		kind config.DeviceType
		want Backend
		err  bool
	}{
		{name: "linux android", goos: "linux", kind: config.DeviceAndroidPhysical, want: Container},
		{name: "linux redroid", goos: "linux", kind: config.DeviceRedroid, want: Container},
		{name: "linux ios rejected", goos: "linux", kind: config.DeviceIOSSimulator, err: true},
		{name: "mac android", goos: "darwin", kind: config.DeviceAndroidPhysical, want: Container},
		{name: "mac ios", goos: "darwin", kind: config.DeviceIOSSimulator, want: Native},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Select(config.Config{Devices: []config.DeviceConfig{{Type: c.kind}}}, c.goos)
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
