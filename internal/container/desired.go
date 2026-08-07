package container

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/forkbombeu/credimi-runner/internal/config"
	runnerplacement "github.com/forkbombeu/credimi-runner/internal/runtime"
)

type HostCapabilities struct{ Docker, KVM bool }
type Inputs struct {
	CloudflareTokenPath string
	ConfigPath          string
}

func Desired(cfg config.Config, goos string, capabilities HostCapabilities, inputs Inputs) ([]Spec, error) {
	specs := []Spec{}
	backend, err := runnerplacement.Select(cfg, goos)
	if err != nil {
		return nil, err
	}
	native := backend == runnerplacement.Native
	if !native {
		if !capabilities.Docker && goos == "linux" {
			return nil, fmt.Errorf("container backend requires Docker")
		}
		runner := Spec{
			Name: resourceName(cfg.Runner.ID, "runner"), Image: cfg.Android.RunnerImage,
			PullPolicy: cfg.Android.PullPolicy, Network: cfg.Android.Network,
			Command: []string{"credimi-runner", "internal-runtime"},
			Mounts:  []Mount{{inputs.ConfigPath, "/etc/credimi-runner/config.toml", true}, {cfg.Android.StateVolume, "/var/lib/credimi-runner", false}, {cfg.Android.ToolCacheVolume, "/opt/credimi-runner/tools", false}, {cfg.Android.SDKVolume, "/opt/android-sdk", false}},
			Ports: []Port{
				{"127.0.0.1", listenPort(cfg.Server.APIListen, 8050), listenPort(cfg.Server.APIListen, 8050)},
				{"127.0.0.1", listenPort(cfg.Server.DashboardListen, 8051), listenPort(cfg.Server.DashboardListen, 8051)},
			},
		}
		if capabilities.KVM {
			runner.Devices = []string{"/dev/kvm"}
		}
		specs = append(specs, runner)
	}
	if cfg.Exposure.Mode == "quick_tunnel" || cfg.Exposure.Mode == "named_tunnel" {
		_, port, err := net.SplitHostPort(cfg.Server.APIListen)
		if err != nil {
			return nil, fmt.Errorf("split API listen address: %w", err)
		}
		network := cfg.Android.Network
		originHost := "runner"
		extraHosts := []string(nil)
		if native {
			originHost, extraHosts = "host.docker.internal", []string{"host.docker.internal:host-gateway"}
		}
		caddy := Spec{Name: resourceName(cfg.Runner.ID, "caddy"), Image: "caddy:2.9-alpine", PullPolicy: "if-not-present", Network: network, Command: []string{"caddy", "reverse-proxy", "--from", ":80", "--to", originHost + ":" + port}, ExtraHosts: extraHosts}
		specs = append(specs, caddy)
		origin := "http://caddy:80"
		tunnel := Spec{Name: resourceName(cfg.Runner.ID, "tunnel"), Image: "cloudflare/cloudflared:latest", PullPolicy: "if-not-present", ExtraHosts: extraHosts}
		tunnel.Network = network
		if cfg.Exposure.Mode == "quick_tunnel" {
			tunnel.Command = []string{"tunnel", "--no-autoupdate", "--url", origin}
		} else {
			// cloudflared supports token-file directly. Keeping the token in the
			// read-only mount prevents it appearing in inspect output or labels.
			tunnel.Command = []string{"tunnel", "--no-autoupdate", "run", "--token-file", "/run/secrets/cloudflare-token", "--url", origin}
		}
		if cfg.Exposure.Mode == "named_tunnel" {
			tunnel.Mounts = []Mount{{inputs.CloudflareTokenPath, "/run/secrets/cloudflare-token", true}}
		}
		specs = append(specs, tunnel)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}

func listenPort(address string, fallback int) int {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fallback
	}
	var value int
	_, _ = fmt.Sscanf(port, "%d", &value)
	if value < 1 || value > 65535 {
		return fallback
	}
	return value
}

// ResourceName returns the deterministic name of a Docker resource owned by a
// runner. It is exported so runtime readiness can inspect its tunnel logs
// without guessing at Docker labels or listing unrelated containers.
func ResourceName(runnerID, suffix string) string {
	return "credimi-" + strings.NewReplacer("/", "-", "_", "-").Replace(runnerID) + "-" + suffix
}

func resourceName(runnerID, suffix string) string { return ResourceName(runnerID, suffix) }
