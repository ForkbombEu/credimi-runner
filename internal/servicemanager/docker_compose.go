package servicemanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
)

const (
	defaultServiceImage = "ghcr.io/forkbombeu/credimi-runner:latest"
	defaultPullPolicy   = "missing"

	ContainerConfigDir  = "/etc/credimi-runner"
	ContainerAndroidDir = "/root/.android"
	ContainerAVDHome    = "/root/.android/avd"
	ContainerGoldenRoot = "/avd-golden"
	ContainerStateDir   = "/var/lib/credimi-runner"
	ContainerToolsDir   = "/opt/credimi-tools"
	ContainerSDKDir     = "/opt/android-sdk"
	ADBServerSocket     = "tcp:127.0.0.1:5037"
	ConfigOwnerUIDEnv   = "CREDIMI_CONFIG_OWNER_UID"
	ConfigOwnerGIDEnv   = "CREDIMI_CONFIG_OWNER_GID"
	HostHomeEnv         = "CREDIMI_HOST_HOME"
	HostAndroidDirEnv   = "CREDIMI_HOST_ANDROID_DIR"
	HostGoldenRootEnv   = "CREDIMI_HOST_GOLDEN_ROOT"
	ContainerAndroidEnv = "CREDIMI_CONTAINER_ANDROID_DIR"
	ContainerAVDHomeEnv = "CREDIMI_CONTAINER_AVD_HOME"
	ContainerGoldenEnv  = "CREDIMI_CONTAINER_GOLDEN_ROOT"
	AndroidAVDHomeEnv   = "ANDROID_AVD_HOME"
	ADBServerSocketEnv  = "ADB_SERVER_SOCKET"
	// ServiceNetworkModeEnv describes the actual Docker topology to the
	// internal service; it is not user configuration.
	ServiceNetworkModeEnv = "CREDIMI_SERVICE_NETWORK_MODE"
	serviceFingerprint    = "io.credimi.runner.service-fingerprint"
	serviceManagedLabel   = "io.credimi.runner.managed"
	serviceProjectLabel   = "io.credimi.runner.project"
)

type HostContext struct {
	ConfigDir            string
	HomeDir              string
	UID                  int
	GID                  int
	AndroidDir           string
	AVDHome              string
	GoldenRoot           string
	ADBServerSocket      string
	HasKVM               bool
	OS                   string
	BeforeSetup          bool
	Bootstrap            BootstrapOptions
	HostAddresses        []string
	ResolvedHostLocality map[string]bool
}

type BindMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type NamedVolume struct {
	Name   string
	Target string
}

type DeviceMapping struct {
	Source string
	Target string
}

type PortMapping struct {
	HostIP        string
	HostPort      string
	ContainerPort string
}

type ServiceSpec struct {
	Image         string
	PullPolicy    string
	NetworkMode   string
	Networks      []string
	Environment   map[string]string
	BindMounts    []BindMount
	Volumes       []NamedVolume
	Devices       []DeviceMapping
	Ports         []PortMapping
	RestartPolicy string
	Command       []string
	Labels        map[string]string
	ExtraHosts    []string
}

// Fingerprint hashes every field that can change the service container's
// capabilities. Slice order is normalized so equivalent specs remain stable.
func (s ServiceSpec) Fingerprint() string {
	canonical := s
	// Restart policy is host lifecycle state. Enable and disable update it on
	// the live container without recreating the service, so it is not part of
	// the capability fingerprint used to detect topology changes.
	canonical.RestartPolicy = ""
	canonical.Command = append([]string(nil), s.Command...)
	canonical.BindMounts = append([]BindMount(nil), s.BindMounts...)
	canonical.Volumes = append([]NamedVolume(nil), s.Volumes...)
	canonical.Devices = append([]DeviceMapping(nil), s.Devices...)
	canonical.Ports = append([]PortMapping(nil), s.Ports...)
	canonical.ExtraHosts = append([]string(nil), s.ExtraHosts...)
	canonical.Networks = append([]string(nil), s.Networks...)
	canonical.Environment = cloneStringMap(s.Environment)
	canonical.Labels = cloneStringMap(s.Labels)
	sort.Slice(canonical.BindMounts, func(i, j int) bool {
		return bindMountKey(canonical.BindMounts[i]) < bindMountKey(canonical.BindMounts[j])
	})
	sort.Slice(canonical.Volumes, func(i, j int) bool {
		return namedVolumeKey(canonical.Volumes[i]) < namedVolumeKey(canonical.Volumes[j])
	})
	sort.Slice(canonical.Devices, func(i, j int) bool { return deviceKey(canonical.Devices[i]) < deviceKey(canonical.Devices[j]) })
	sort.Slice(canonical.Ports, func(i, j int) bool { return portKey(canonical.Ports[i]) < portKey(canonical.Ports[j]) })
	sort.Strings(canonical.ExtraHosts)
	sort.Strings(canonical.Networks)
	payload, _ := json.Marshal(canonical)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func BuildServiceSpec(cfg runnerconfig.Config, host HostContext) (ServiceSpec, error) {
	return BuildServiceSpecWithAutostart(cfg, host, false)
}

func BuildServiceSpecWithAutostart(cfg runnerconfig.Config, host HostContext, autostart bool) (ServiceSpec, error) {
	if host.OS == "" {
		host.OS = runtime.GOOS
	}
	if host.ConfigDir == "" {
		host.ConfigDir = "."
	}
	if host.HomeDir == "" {
		return ServiceSpec{}, fmt.Errorf("host home directory is empty")
	}
	if host.AndroidDir == "" {
		host.AndroidDir = filepath.Join(host.HomeDir, ".android")
	}
	if host.AVDHome == "" {
		host.AVDHome = filepath.Join(host.AndroidDir, "avd")
	}
	if host.GoldenRoot == "" {
		host.GoldenRoot = filepath.Join(host.HomeDir, "avd-golden")
	}

	spec := ServiceSpec{
		Image:         defaultIfEmpty(cfg.Android.RunnerImage, defaultServiceImage),
		PullPolicy:    composePullPolicy(cfg.Android.PullPolicy),
		NetworkMode:   "bridge",
		Environment:   map[string]string{},
		RestartPolicy: "on-failure",
		Command:       []string{"internal-service"},
		Labels: map[string]string{
			serviceManagedLabel: "true",
			serviceProjectLabel: ProjectName(host.ConfigDir, host.UID),
		},
	}
	if autostart {
		spec.RestartPolicy = "always"
	}
	configuredNetwork := strings.TrimSpace(cfg.Android.Network)
	if configuredNetwork != "" && !strings.EqualFold(configuredNetwork, "host") && !strings.EqualFold(configuredNetwork, "bridge") {
		spec.Networks = []string{configuredNetwork}
	}

	setEnv := func(key, value string) { spec.Environment[key] = value }
	setEnv("CREDIMI_RUNNER_CONFIG_DIR", ContainerConfigDir)
	setEnv(ConfigOwnerUIDEnv, strconv.Itoa(host.UID))
	setEnv(ConfigOwnerGIDEnv, strconv.Itoa(host.GID))
	setEnv(ComposeProjectEnv, ProjectName(host.ConfigDir, host.UID))
	setEnv(AppliedServiceConfigFingerprintEnv, ServiceConfigFingerprintForHost(cfg, !host.BeforeSetup, host))
	capabilities := ServiceCapabilitiesForConfig(cfg)
	setEnv(AppliedServiceNeedsHostADBEnv, strconv.FormatBool(capabilities.NeedsHostADB || host.BeforeSetup))
	setEnv(AppliedServiceNeedsUSBEnv, strconv.FormatBool(capabilities.NeedsUSB))
	setEnv(AppliedServiceNeedsEmulatorEnv, strconv.FormatBool(capabilities.NeedsEmulator))
	knownHostsMetadata, _ := json.Marshal(ServiceRedroidKnownHostsForConfig(cfg))
	setEnv(AppliedServiceRedroidKnownHostsEnv, string(knownHostsMetadata))
	hostAddresses, _ := json.Marshal(host.HostAddresses)
	setEnv(AppliedServiceHostAddressesEnv, string(hostAddresses))
	resolvedHosts, _ := json.Marshal(cloneBoolMap(host.ResolvedHostLocality))
	setEnv(AppliedServiceResolvedHostsEnv, string(resolvedHosts))
	setEnv(HostHomeEnv, host.HomeDir)
	setEnv(HostAndroidDirEnv, host.AndroidDir)
	setEnv(HostGoldenRootEnv, host.GoldenRoot)
	setEnv(ContainerAndroidEnv, ContainerAndroidDir)
	setEnv(ContainerAVDHomeEnv, ContainerAVDHome)
	setEnv(ContainerGoldenEnv, ContainerGoldenRoot)
	if host.BeforeSetup {
		if image := strings.TrimSpace(host.Bootstrap.Image); image != "" {
			setEnv("CREDIMI_BOOTSTRAP_IMAGE", image)
			setEnv("ANDROID_RUNNER_IMAGE", image)
		}
		if policy := strings.TrimSpace(host.Bootstrap.PullPolicy); policy != "" {
			setEnv("CREDIMI_BOOTSTRAP_PULL_POLICY", policy)
			setEnv("ANDROID_PULL_POLICY", policy)
		}
	}

	needsAndroid, needsEmulator, needsUSB, usesHostADB := false, false, false, host.BeforeSetup
	knownHosts := map[string]struct{}{}
	for index, device := range cfg.Devices {
		if !device.Enabled {
			continue
		}
		switch device.Type {
		case runnerconfig.DeviceAndroidPhysical:
			if device.AndroidPhysical == nil || device.AndroidPhysical.Transport == "no_device" {
				continue
			}
			needsAndroid = true
			usesHostADB = true
			if device.AndroidPhysical.Transport == "usb" {
				needsUSB = true
			}
		case runnerconfig.DeviceAndroidEmulator:
			if device.AndroidEmulator == nil {
				return ServiceSpec{}, fmt.Errorf("device %q has no Android emulator configuration", device.ID)
			}
			needsAndroid, needsEmulator = true, true
		case runnerconfig.DeviceRedroid:
			if device.Redroid == nil {
				return ServiceSpec{}, fmt.Errorf("device %q has no Redroid configuration", device.ID)
			}
			path := strings.TrimSpace(device.Redroid.AVDCTLSSHKnownHostsPath)
			if path == "" && strings.TrimSpace(device.Redroid.AVDCTLSSHTarget) != "" {
				path = filepath.Join(host.HomeDir, ".ssh", "known_hosts")
			}
			if path != "" {
				if err := validateKnownHostsPath(path); err != nil {
					return ServiceSpec{}, fmt.Errorf("device %q known-hosts file %q is not available: %w", device.ID, path, err)
				}
				if _, exists := knownHosts[path]; !exists {
					knownHosts[path] = struct{}{}
					spec.BindMounts = appendBindMount(spec.BindMounts, BindMount{Source: path, Target: path, ReadOnly: true})
				}
			}
		default:
			return ServiceSpec{}, fmt.Errorf("device %d has unsupported service type %q", index, device.Type)
		}
	}

	// Keep the old host-backed Android state contract. The mounts are present
	// even during first-run setup, when no device is typed yet, so the
	// Dashboard can discover and download assets into the host-backed paths.
	if host.OS == "linux" {
		androidSource := host.AndroidDir
		readOnly := false
		if cfg.Android.ADBKeysPath != "" && !needsEmulator && needsAndroid {
			androidSource, readOnly = cfg.Android.ADBKeysPath, true
		}
		spec.BindMounts = appendBindMount(spec.BindMounts, BindMount{Source: androidSource, Target: ContainerAndroidDir, ReadOnly: readOnly})
		spec.BindMounts = appendBindMount(spec.BindMounts, BindMount{Source: host.GoldenRoot, Target: ContainerGoldenRoot})
	}
	if host.OS == "linux" && needsEmulator {
		spec.BindMounts = appendBindMount(spec.BindMounts, BindMount{Source: host.GoldenRoot, Target: ContainerGoldenRoot})
		if host.HasKVM {
			spec.Devices = appendDevice(spec.Devices, DeviceMapping{Source: "/dev/kvm", Target: "/dev/kvm"})
		}
	}
	if host.OS == "linux" && needsUSB {
		spec.Devices = appendDevice(spec.Devices, DeviceMapping{Source: "/dev/bus/usb", Target: "/dev/bus/usb"})
	}
	// Apply the same host-resolved topology decision used by the fingerprint.
	spec.NetworkMode = ServiceNetworkModeForConfig(cfg, host)
	// Set this only after every input that can select host networking.
	setEnv(ServiceNetworkModeEnv, spec.NetworkMode)
	if usesHostADB {
		adbSocket := strings.TrimSpace(host.ADBServerSocket)
		if adbSocket == "" {
			adbSocket = ADBServerSocket
			if spec.NetworkMode != "host" {
				adbSocket = "tcp:host.docker.internal:5037"
				spec.ExtraHosts = append(spec.ExtraHosts, "host.docker.internal:host-gateway")
			}
		}
		setEnv(ADBServerSocketEnv, adbSocket)
	}

	if host.OS == "linux" {
		setEnv(AndroidAVDHomeEnv, ContainerAVDHome)
	}
	if spec.NetworkMode != "host" {
		if hostPort, containerPort := listenPort(cfg.Server.DashboardListen, "8051"); containerPort != "" {
			spec.Ports = appendPort(spec.Ports, PortMapping{HostIP: "127.0.0.1", HostPort: hostPort, ContainerPort: containerPort})
		}
		if hostPort, containerPort := listenPort(cfg.Server.APIListen, "8050"); containerPort != "" && containerPort != "0" {
			apiHostIP := "127.0.0.1"
			if cfg.Exposure.Mode == "manual" {
				apiHostIP = "0.0.0.0"
			}
			spec.Ports = appendPort(spec.Ports, PortMapping{HostIP: apiHostIP, HostPort: hostPort, ContainerPort: containerPort})
		}
	}

	spec.BindMounts = appendBindMount(spec.BindMounts, BindMount{Source: host.ConfigDir, Target: ContainerConfigDir})
	for _, volume := range []NamedVolume{
		{Name: defaultIfEmpty(cfg.Android.StateVolume, "credimi-runner-state"), Target: ContainerStateDir},
		{Name: defaultIfEmpty(cfg.Android.ToolCacheVolume, "credimi-runner-tools"), Target: ContainerToolsDir},
		{Name: defaultIfEmpty(cfg.Android.SDKVolume, "credimi-runner-sdk"), Target: ContainerSDKDir},
	} {
		spec.Volumes = appendNamedVolume(spec.Volumes, volume)
	}
	return spec, nil
}

func WriteServiceComposeWithHost(dir string, cfg runnerconfig.Config, host HostContext) error {
	return WriteServiceComposeWithHostAndAutostart(dir, cfg, host, false)
}

func WriteServiceComposeWithHostAndAutostart(dir string, cfg runnerconfig.Config, host HostContext, autostart bool) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("service config directory is empty")
	}
	host.ConfigDir = dir
	spec, err := BuildServiceSpecWithAutostart(cfg, host, autostart)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	content := RenderServiceCompose(spec)
	tmp := filepath.Join(dir, ".service-compose.yaml.tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "service-compose.yaml"))
}

func RenderServiceCompose(spec ServiceSpec) string {
	var b strings.Builder
	fingerprint := spec.Fingerprint()
	fmt.Fprintf(&b, "services:\n  runner:\n    image: %s\n    pull_policy: %s\n    restart: %s\n    command:\n", spec.Image, spec.PullPolicy, spec.RestartPolicy)
	for _, command := range spec.Command {
		fmt.Fprintf(&b, "      - %s\n", yamlQuote(command))
	}
	b.WriteString("    environment:\n")
	for _, key := range sortedKeys(spec.Environment) {
		fmt.Fprintf(&b, "      %s: %s\n", key, yamlQuote(spec.Environment[key]))
	}
	if spec.NetworkMode != "" && (spec.NetworkMode != "bridge" || len(spec.Networks) == 0) {
		fmt.Fprintf(&b, "    network_mode: %s\n", yamlQuote(spec.NetworkMode))
	}
	if len(spec.Ports) > 0 {
		b.WriteString("    ports:\n")
		for _, port := range sortedPorts(spec.Ports) {
			fmt.Fprintf(&b, "      - %s\n", yamlQuote(portString(port)))
		}
	}
	if len(spec.ExtraHosts) > 0 {
		b.WriteString("    extra_hosts:\n")
		for _, host := range sortedStrings(spec.ExtraHosts) {
			fmt.Fprintf(&b, "      - %s\n", yamlQuote(host))
		}
	}
	if len(spec.BindMounts)+len(spec.Volumes) > 0 {
		b.WriteString("    volumes:\n")
		for _, mount := range sortedBinds(spec.BindMounts) {
			fmt.Fprintf(&b, "      - %s\n", yamlQuote(bindString(mount)))
		}
		for _, volume := range sortedVolumes(spec.Volumes) {
			fmt.Fprintf(&b, "      - %s\n", yamlQuote(volume.Name+":"+volume.Target))
		}
	}
	if len(spec.Devices) > 0 {
		b.WriteString("    devices:\n")
		for _, device := range sortedDevices(spec.Devices) {
			fmt.Fprintf(&b, "      - %s\n", yamlQuote(device.Source+":"+device.Target))
		}
	}
	if spec.NetworkMode != "host" && len(spec.Networks) > 0 {
		b.WriteString("    networks:\n")
		for _, network := range sortedStrings(spec.Networks) {
			fmt.Fprintf(&b, "      - %s\n", yamlQuote(network))
		}
	}
	b.WriteString("    labels:\n")
	labels := cloneStringMap(spec.Labels)
	labels[serviceFingerprint] = fingerprint
	for _, key := range sortedKeys(labels) {
		fmt.Fprintf(&b, "      %s: %s\n", key, yamlQuote(labels[key]))
	}
	b.WriteString("volumes:\n")
	for _, volume := range sortedVolumes(spec.Volumes) {
		fmt.Fprintf(&b, "  %s: {}\n", volume.Name)
	}
	if spec.NetworkMode != "host" && len(spec.Networks) > 0 {
		b.WriteString("networks:\n")
		for _, network := range sortedStrings(spec.Networks) {
			fmt.Fprintf(&b, "  %s: {}\n", network)
		}
	}
	return b.String()
}

func ResolveHostContext(configDir string) (HostContext, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return HostContext{}, fmt.Errorf("resolve host home directory: %w", err)
	}
	uid, gid, err := currentHostIDs()
	if err != nil {
		return HostContext{}, err
	}
	return HostContext{ConfigDir: configDir, HomeDir: home, UID: uid, GID: gid, AndroidDir: filepath.Join(home, ".android"), AVDHome: filepath.Join(home, ".android", "avd"), GoldenRoot: filepath.Join(home, "avd-golden"), HasKVM: fileExists("/dev/kvm"), OS: runtime.GOOS, BeforeSetup: !fileExists(filepath.Join(configDir, "config.toml")), HostAddresses: hostInterfaceAddresses()}, nil
}

func hostInterfaceAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	addresses := make([]string, 0)
	for _, iface := range interfaces {
		values, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, value := range values {
			name, _, err := net.ParseCIDR(value.String())
			if err == nil {
				addresses = append(addresses, name.String())
			}
		}
	}
	sort.Strings(addresses)
	return addresses
}

func listenPort(address, fallback string) (string, string) {
	address = strings.TrimSpace(address)
	if address == "" {
		return fallback, fallback
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fallback, fallback
	}
	return port, port
}

func composePullPolicy(policy string) string {
	if strings.TrimSpace(policy) == "" || policy == "if-not-present" {
		return defaultPullPolicy
	}
	return policy
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func validateKnownHostsPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func yamlQuote(value string) string { return strconv.Quote(value) }

func bindString(mount BindMount) string {
	value := mount.Source + ":" + mount.Target
	if mount.ReadOnly {
		value += ":ro"
	}
	return value
}

func portString(port PortMapping) string {
	if port.HostIP == "" {
		return port.HostPort + ":" + port.ContainerPort
	}
	return port.HostIP + ":" + port.HostPort + ":" + port.ContainerPort
}

func bindMountKey(m BindMount) string     { return bindString(m) }
func namedVolumeKey(v NamedVolume) string { return v.Name + "\x00" + v.Target }
func deviceKey(d DeviceMapping) string    { return d.Source + "\x00" + d.Target }
func portKey(p PortMapping) string        { return p.HostIP + "\x00" + p.HostPort + "\x00" + p.ContainerPort }

func appendBindMount(mounts []BindMount, candidate BindMount) []BindMount {
	for _, mount := range mounts {
		if mount.Target == candidate.Target {
			return mounts
		}
	}
	return append(mounts, candidate)
}

func appendNamedVolume(volumes []NamedVolume, candidate NamedVolume) []NamedVolume {
	for _, volume := range volumes {
		if volume.Target == candidate.Target {
			return volumes
		}
	}
	return append(volumes, candidate)
}

func appendDevice(devices []DeviceMapping, candidate DeviceMapping) []DeviceMapping {
	for _, device := range devices {
		if device.Source == candidate.Source && device.Target == candidate.Target {
			return devices
		}
	}
	return append(devices, candidate)
}

func appendPort(ports []PortMapping, candidate PortMapping) []PortMapping {
	for _, port := range ports {
		if portKey(port) == portKey(candidate) {
			return ports
		}
	}
	return append(ports, candidate)
}

func cloneStringMap(values map[string]string) map[string]string {
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrings(values []string) []string {
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	return copyValues
}

func sortedBinds(values []BindMount) []BindMount {
	copyValues := append([]BindMount(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return bindMountKey(copyValues[i]) < bindMountKey(copyValues[j]) })
	return copyValues
}

func sortedVolumes(values []NamedVolume) []NamedVolume {
	copyValues := append([]NamedVolume(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return namedVolumeKey(copyValues[i]) < namedVolumeKey(copyValues[j]) })
	return copyValues
}

func sortedDevices(values []DeviceMapping) []DeviceMapping {
	copyValues := append([]DeviceMapping(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return deviceKey(copyValues[i]) < deviceKey(copyValues[j]) })
	return copyValues
}

func sortedPorts(values []PortMapping) []PortMapping {
	copyValues := append([]PortMapping(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return portKey(copyValues[i]) < portKey(copyValues[j]) })
	return copyValues
}
