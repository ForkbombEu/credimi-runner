package servicemanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

type ServiceSpec struct {
	Image       string
	PullPolicy  string
	NetworkMode string
	Volumes     []string
	Devices     []string
}

func (s ServiceSpec) Fingerprint() string {
	volumes, devices := append([]string(nil), s.Volumes...), append([]string(nil), s.Devices...)
	sort.Strings(volumes)
	sort.Strings(devices)
	payload, _ := json.Marshal(struct {
		Image, PullPolicy, NetworkMode string
		Volumes, Devices               []string
	}{s.Image, s.PullPolicy, s.NetworkMode, volumes, devices})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
func WriteServiceCompose(dir string, cfg runnerconfig.Config) error {
	if dir == "" {
		return fmt.Errorf("service config directory is empty")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	image := cfg.Android.RunnerImage
	if image == "" {
		image = "ghcr.io/forkbombeu/credimi-runner:latest"
	}
	pull := cfg.Android.PullPolicy
	if pull == "" {
		pull = "missing"
	} else if pull == "if-not-present" {
		pull = "missing"
	}
	port := cfg.Server.DashboardListen
	if port == "" {
		port = "127.0.0.1:8051"
	}
	network := cfg.Android.Network
	if strings.TrimSpace(network) == "" {
		network = "bridge"
	}
	volumes := []string{dir + ":/etc/credimi-runner"}
	for _, volume := range []struct{ source, target string }{
		{cfg.Android.StateVolume, "/var/lib/credimi-runner"},
		{cfg.Android.ToolCacheVolume, "/opt/credimi-tools"},
		{cfg.Android.SDKVolume, "/opt/android-sdk"},
	} {
		if strings.TrimSpace(volume.source) != "" {
			volumes = append(volumes, volume.source+":"+volume.target)
		}
	}
	devices := make([]string, 0)
	for _, d := range cfg.Devices {
		if d.Enabled && d.Type == runnerconfig.DeviceAndroidPhysical && d.AndroidPhysical != nil && d.AndroidPhysical.Transport == "usb" {
			devices = append(devices, "/dev/bus/usb")
		}
	}
	if len(devices) > 0 {
		network = "host"
	}
	spec := ServiceSpec{Image: image, PullPolicy: pull, NetworkMode: network, Volumes: volumes, Devices: devices}
	var b strings.Builder
	fmt.Fprintf(&b, "services:\n  runner:\n    image: %s\n    pull_policy: %s\n    restart: unless-stopped\n    command:\n      - internal-service\n    environment:\n      CREDIMI_RUNNER_CONFIG_DIR: /etc/credimi-runner\n    volumes:\n", image, pull)
	for _, volume := range volumes {
		fmt.Fprintf(&b, "      - %s\n", volume)
	}
	if network == "host" {
		b.WriteString("    network_mode: host\n")
	} else {
		b.WriteString("    ports:\n")
		fmt.Fprintf(&b, "      - \"127.0.0.1:%s:8051\"\n", portPort(port))
		apiPort := portPort(cfg.Server.APIListen)
		if apiPort != "" && apiPort != "0" {
			bind := "127.0.0.1"
			if cfg.Exposure.Mode == "manual" {
				bind = "0.0.0.0"
			}
			fmt.Fprintf(&b, "      - \"%s:%s:%s\"\n", bind, apiPort, apiPort)
		}
	}
	if len(devices) > 0 {
		b.WriteString("    devices:\n")
		for _, device := range devices {
			fmt.Fprintf(&b, "      - %s:%s\n", device, device)
		}
	}
	fmt.Fprintf(&b, "    labels:\n      io.credimi.runner.service-fingerprint: %s\n", spec.Fingerprint())
	content := b.String()
	tmp := filepath.Join(dir, ".service-compose.yaml.tmp")
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "service-compose.yaml"))
}

// WriteServiceSpecFingerprint projects only topology-affecting configuration.
// It is kept separate from rendering so status can compare the desired spec
// without mutating the service definition.
func WriteServiceSpecFingerprint(dir string, cfg runnerconfig.Config) (string, error) {
	image := cfg.Android.RunnerImage
	if image == "" {
		image = "ghcr.io/forkbombeu/credimi-runner:latest"
	}
	pull := cfg.Android.PullPolicy
	if pull == "" {
		pull = "missing"
	} else if pull == "if-not-present" {
		pull = "missing"
	}
	network := cfg.Android.Network
	if strings.TrimSpace(network) == "" {
		network = "bridge"
	}
	volumes := []string{dir + ":/etc/credimi-runner"}
	for _, volume := range []struct{ source, target string }{{cfg.Android.StateVolume, "/var/lib/credimi-runner"}, {cfg.Android.ToolCacheVolume, "/opt/credimi-tools"}, {cfg.Android.SDKVolume, "/opt/android-sdk"}} {
		if strings.TrimSpace(volume.source) != "" {
			volumes = append(volumes, volume.source+":"+volume.target)
		}
	}
	devices := make([]string, 0)
	for _, d := range cfg.Devices {
		if d.Enabled && d.Type == runnerconfig.DeviceAndroidPhysical && d.AndroidPhysical != nil && d.AndroidPhysical.Transport == "usb" {
			devices = append(devices, "/dev/bus/usb")
		}
	}
	if len(devices) > 0 {
		network = "host"
	}
	return (ServiceSpec{Image: image, PullPolicy: pull, NetworkMode: network, Volumes: volumes, Devices: devices}).Fingerprint(), nil
}
func portPort(address string) string {
	for i := len(address) - 1; i >= 0; i-- {
		if address[i] == ':' {
			return address[i+1:]
		}
	}
	return "8051"
}
