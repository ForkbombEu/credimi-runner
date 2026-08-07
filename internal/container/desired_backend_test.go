package container

import (
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func TestDesiredContainerBackendRoutesCaddyToRunner(t *testing.T) {
	cfg := config.Config{
		Runner:   config.RunnerConfig{ID: "acme/runner"},
		Android:  config.AndroidConfig{RunnerImage: "runner:latest", PullPolicy: "never", Network: "credimi-runner", StateVolume: "state", ToolCacheVolume: "tools", SDKVolume: "sdk"},
		Server:   config.ServerConfig{APIListen: "0.0.0.0:8050", DashboardListen: "127.0.0.1:8051"},
		Exposure: config.ExposureConfig{Mode: "quick_tunnel"},
	}
	specs, err := Desired(cfg, "linux", HostCapabilities{Docker: true}, Inputs{ConfigPath: "/tmp/config.toml"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Spec{}
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	runner := byName["credimi-acme-runner-runner"]
	if runner.Image != "runner:latest" || runner.Network != "credimi-runner" {
		t.Fatalf("runner=%#v", runner)
	}
	caddy := byName["credimi-acme-runner-caddy"]
	if len(caddy.Command) < 6 || caddy.Command[len(caddy.Command)-1] != "runner:8050" {
		t.Fatalf("caddy command=%#v", caddy.Command)
	}
	if len(caddy.ExtraHosts) != 0 {
		t.Fatalf("container caddy unexpectedly uses host bridge: %#v", caddy.ExtraHosts)
	}
}

func TestDesiredNativeIOSBackendDoesNotCreateRunnerContainer(t *testing.T) {
	cfg := config.Config{Runner: config.RunnerConfig{ID: "acme/runner"}, Android: config.AndroidConfig{RunnerImage: "runner:latest", PullPolicy: "never", Network: "credimi-runner"}, Server: config.ServerConfig{APIListen: "0.0.0.0:8050"}, Exposure: config.ExposureConfig{Mode: "quick_tunnel"}, Devices: []config.DeviceConfig{{Type: config.DeviceIOSSimulator}}}
	specs, err := Desired(cfg, "darwin", HostCapabilities{Docker: true}, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		if spec.Name == "credimi-acme-runner-runner" {
			t.Fatal("native backend created a runner container")
		}
	}
	for _, spec := range specs {
		if spec.Name == "credimi-acme-runner-caddy" && len(spec.ExtraHosts) == 0 {
			t.Fatal("native caddy did not use host bridge")
		}
	}
}
