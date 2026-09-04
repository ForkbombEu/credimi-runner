package servicemanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"runtime"
	"sort"
	"strings"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

const (
	AppliedServiceConfigFingerprintEnv = "CREDIMI_APPLIED_SERVICE_CONFIG_FINGERPRINT"
	AppliedServiceNeedsHostADBEnv      = "CREDIMI_APPLIED_SERVICE_NEEDS_HOST_ADB"
	AppliedServiceNeedsUSBEnv          = "CREDIMI_APPLIED_SERVICE_NEEDS_USB"
	AppliedServiceNeedsEmulatorEnv     = "CREDIMI_APPLIED_SERVICE_NEEDS_EMULATOR"
	AppliedServiceRedroidKnownHostsEnv = "CREDIMI_APPLIED_SERVICE_REDROID_KNOWN_HOSTS"
	AppliedServiceHostAddressesEnv     = "CREDIMI_APPLIED_SERVICE_HOST_ADDRESSES"
	AppliedServiceResolvedHostsEnv     = "CREDIMI_APPLIED_SERVICE_RESOLVED_HOSTS"
)

// ServiceCapabilities describes capabilities that may safely be retained when
// a desired configuration removes them. Expanding any of these capabilities
// still requires service recreation. RedroidKnownHosts is the applied set of
// read-only mounts and follows the same safe-superset rule.
type ServiceCapabilities struct {
	NeedsHostADB         bool
	NeedsUSB             bool
	NeedsEmulator        bool
	RedroidKnownHosts    []string
	NetworkMode          string
	HostAddresses        []string
	ResolvedHostLocality map[string]string
}

type serviceConfigProjection struct {
	Configured        bool                     `json:"configured"`
	APIListen         string                   `json:"api_listen"`
	DashboardListen   string                   `json:"dashboard_listen"`
	ReadHeaderTimeout string                   `json:"read_header_timeout"`
	ShutdownTimeout   string                   `json:"shutdown_timeout"`
	ExposureClass     string                   `json:"exposure_class"`
	NetworkMode       string                   `json:"network_mode"`
	Android           serviceAndroidProjection `json:"android"`
	NeedsHostADB      bool                     `json:"needs_host_adb"`
	NeedsUSB          bool                     `json:"needs_usb"`
	NeedsEmulator     bool                     `json:"needs_emulator"`
	RedroidKnownHosts []string                 `json:"redroid_known_hosts,omitempty"`
}

type serviceAndroidProjection struct {
	RunnerImage     string `json:"runner_image"`
	PullPolicy      string `json:"pull_policy"`
	Network         string `json:"network"`
	StateVolume     string `json:"state_volume"`
	ToolCacheVolume string `json:"tool_cache_volume"`
	SDKVolume       string `json:"sdk_volume"`
	ADBKeysPath     string `json:"adb_keys_path"`
}

const defaultRedroidKnownHosts = "<host-default-known-hosts>"

// ServiceConfigFingerprint identifies only persisted settings that can make
// the current service/container topology stale. Runtime credentials and
// execution settings intentionally do not participate.
func ServiceConfigFingerprint(cfg config.Config, configured bool) string {
	return ServiceConfigFingerprintForHost(cfg, configured, HostContext{})
}

// ServiceConfigFingerprintForHost includes the host-resolved network
// namespace decision used by the generated service. Keeping that decision in
// the same projection prevents the service specification and restart guard
// from disagreeing about host-local dependencies.
func ServiceConfigFingerprintForHost(cfg config.Config, configured bool, host HostContext) string {
	// Persisted TOML is normally loaded with defaults applied. Applying them
	// here as well keeps callers that hold a freshly assembled Config on the
	// same canonical projection.
	_ = config.ApplyDefaults(&cfg)
	return fingerprintServiceProjection(serviceConfigProjectionForHost(cfg, configured, host))
}

func serviceConfigProjectionFor(cfg config.Config, configured bool) serviceConfigProjection {
	return serviceConfigProjectionForHost(cfg, configured, HostContext{})
}

func serviceConfigProjectionForHost(cfg config.Config, configured bool, host HostContext) serviceConfigProjection {
	_ = config.ApplyDefaults(&cfg)
	projection := serviceConfigProjection{
		Configured:        configured,
		APIListen:         cfg.Server.APIListen,
		DashboardListen:   cfg.Server.DashboardListen,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Duration().String(),
		ShutdownTimeout:   cfg.Server.ShutdownTimeout.Duration().String(),
		ExposureClass:     serviceExposureClass(cfg.Exposure.Mode),
		NetworkMode:       ServiceNetworkModeForConfig(cfg, host),
		NeedsHostADB:      !configured,
		Android: serviceAndroidProjection{
			RunnerImage:     cfg.Android.RunnerImage,
			PullPolicy:      cfg.Android.PullPolicy,
			Network:         cfg.Android.Network,
			StateVolume:     cfg.Android.StateVolume,
			ToolCacheVolume: cfg.Android.ToolCacheVolume,
			SDKVolume:       cfg.Android.SDKVolume,
			ADBKeysPath:     cfg.Android.ADBKeysPath,
		},
	}
	knownHosts := map[string]struct{}{}
	for _, device := range cfg.Devices {
		if !device.Enabled {
			continue
		}
		switch device.Type {
		case config.DeviceAndroidPhysical:
			if device.AndroidPhysical == nil {
				continue
			}
			switch strings.TrimSpace(device.AndroidPhysical.Transport) {
			case "usb":
				projection.NeedsUSB = true
				projection.NeedsHostADB = true
			case "wifi":
				projection.NeedsHostADB = true
			}
		case config.DeviceAndroidEmulator:
			projection.NeedsEmulator = true
		case config.DeviceRedroid:
			if device.Redroid == nil {
				continue
			}
			path := strings.TrimSpace(device.Redroid.AVDCTLSSHKnownHostsPath)
			if path == "" && strings.TrimSpace(device.Redroid.AVDCTLSSHTarget) != "" {
				path = defaultRedroidKnownHosts
			}
			if path != "" {
				knownHosts[path] = struct{}{}
			}
		}
	}
	for path := range knownHosts {
		projection.RedroidKnownHosts = append(projection.RedroidKnownHosts, path)
	}
	sort.Strings(projection.RedroidKnownHosts)
	return projection
}

func fingerprintServiceProjection(projection serviceConfigProjection) string {
	payload, _ := json.Marshal(projection)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func ServiceCapabilitiesForConfig(cfg config.Config) ServiceCapabilities {
	projection := serviceConfigProjectionFor(cfg, true)
	return ServiceCapabilities{NeedsHostADB: projection.NeedsHostADB, NeedsUSB: projection.NeedsUSB, NeedsEmulator: projection.NeedsEmulator, RedroidKnownHosts: append([]string(nil), projection.RedroidKnownHosts...)}
}

func ServiceRedroidKnownHostsForConfig(cfg config.Config) []string {
	return append([]string(nil), serviceConfigProjectionFor(cfg, true).RedroidKnownHosts...)
}

// ServiceConfigsCompatible reports whether an already applied service can
// satisfy desired configuration. Extra USB, ADB, or KVM capability is a safe
// superset; all other service topology fields must remain equal.
func ServiceConfigsCompatible(applied, desired config.Config, configured bool) bool {
	return ServiceConfigsCompatibleWithHost(applied, desired, configured, HostContext{})
}

// ServiceConfigsCompatibleWithHost applies the capability-superset rules to
// an actual host-resolved service topology. Network namespace changes remain
// exact because host networking changes isolation and Docker DNS behavior.
func ServiceConfigsCompatibleWithHost(applied, desired config.Config, configured bool, host HostContext) bool {
	if ServiceHostLocalityUnknown(desired, host) {
		return false
	}
	appliedProjection := serviceConfigProjectionForHost(applied, configured, host)
	desiredProjection := serviceConfigProjectionForHost(desired, configured, host)
	if appliedProjection.NeedsHostADB {
		desiredProjection.NeedsHostADB = true
	}
	if appliedProjection.NeedsUSB {
		desiredProjection.NeedsUSB = true
	}
	if appliedProjection.NeedsEmulator {
		desiredProjection.NeedsEmulator = true
	}
	if !isStringSetSuperset(appliedProjection.RedroidKnownHosts, desiredProjection.RedroidKnownHosts) {
		return false
	}
	desiredProjection.RedroidKnownHosts = append([]string(nil), appliedProjection.RedroidKnownHosts...)
	sort.Strings(desiredProjection.RedroidKnownHosts)
	return fingerprintServiceProjection(appliedProjection) == fingerprintServiceProjection(desiredProjection)
}

// ServiceConfigCompatibleWithFingerprint applies the same compatibility rule
// when only the applied service fingerprint and its exported capabilities are
// available to the Dashboard process.
func ServiceConfigCompatibleWithFingerprint(cfg config.Config, configured bool, appliedFingerprint string, capabilities ServiceCapabilities) bool {
	if ServiceHostLocalityUnknown(cfg, HostContext{OS: runtime.GOOS, HostAddresses: capabilities.HostAddresses, ResolvedHostLocality: capabilities.ResolvedHostLocality}) {
		return false
	}
	desiredProjection := serviceConfigProjectionForHost(cfg, configured, HostContext{OS: runtime.GOOS, HostAddresses: capabilities.HostAddresses, ResolvedHostLocality: capabilities.ResolvedHostLocality})
	if capabilities.NetworkMode != "" && desiredProjection.NetworkMode != capabilities.NetworkMode {
		return false
	}
	if capabilities.NeedsHostADB {
		desiredProjection.NeedsHostADB = true
	}
	if capabilities.NeedsUSB {
		desiredProjection.NeedsUSB = true
	}
	if capabilities.NeedsEmulator {
		desiredProjection.NeedsEmulator = true
	}
	if !isStringSetSuperset(capabilities.RedroidKnownHosts, desiredProjection.RedroidKnownHosts) {
		return false
	}
	desiredProjection.RedroidKnownHosts = append([]string(nil), capabilities.RedroidKnownHosts...)
	sort.Strings(desiredProjection.RedroidKnownHosts)
	return fingerprintServiceProjection(desiredProjection) == appliedFingerprint
}

// ServiceNetworkModeForConfig is the single host-side topology decision used
// by service rendering and applied-service comparisons. Host networking is
// required for USB/bootstrap operation and when a configured startup
// dependency points at this Linux host. Container DNS names remain bridged.
func ServiceNetworkModeForConfig(cfg config.Config, host HostContext) string {
	if strings.EqualFold(strings.TrimSpace(cfg.Android.Network), "host") {
		return "host"
	}
	if host.OS == "" {
		host.OS = runtime.GOOS
	}
	if host.OS != "linux" {
		return "bridge"
	}
	if host.BeforeSetup || serviceNeedsUSB(cfg) || hostLocalDependencies(cfg, host) {
		return "host"
	}
	return "bridge"
}

func serviceNeedsUSB(cfg config.Config) bool {
	for _, device := range cfg.Devices {
		if device.Enabled && device.Type == config.DeviceAndroidPhysical && device.AndroidPhysical != nil && strings.EqualFold(strings.TrimSpace(device.AndroidPhysical.Transport), "usb") {
			return true
		}
	}
	return false
}

func hostLocalDependencies(cfg config.Config, host HostContext) bool {
	if hostIsLocalURL(cfg.Credimi.URL, host) || hostIsLocalAddress(cfg.Temporal.Address, host) {
		return true
	}
	return serviceExposureClass(cfg.Exposure.Mode) == "manual" && hostIsLocalURL(cfg.Exposure.PublicURL, host)
}

// ServiceHostLocalityUnknown reports whether a hostname relevant to the
// service topology has not been resolved by the authoritative host process.
func ServiceHostLocalityUnknown(cfg config.Config, host HostContext) bool {
	if host.OS == "" {
		return false
	}
	if host.ResolvedHostLocality == nil {
		return false
	}
	if host.OS != "linux" {
		return false
	}
	for _, name := range serviceDependencyHostnames(cfg) {
		if _, known := hostNameLocality(name, host); !known {
			return true
		}
	}
	return false
}

func serviceDependencyHostnames(cfg config.Config) []string {
	names := make([]string, 0, 3)
	if parsed, err := url.Parse(strings.TrimSpace(cfg.Credimi.URL)); err == nil && parsed.Hostname() != "" {
		names = append(names, parsed.Hostname())
	}
	if name, _, err := net.SplitHostPort(strings.TrimSpace(cfg.Temporal.Address)); err == nil && name != "" {
		names = append(names, name)
	} else if name := strings.Trim(strings.TrimSpace(cfg.Temporal.Address), "[]"); name != "" {
		names = append(names, name)
	}
	if serviceExposureClass(cfg.Exposure.Mode) == "manual" {
		if parsed, err := url.Parse(strings.TrimSpace(cfg.Exposure.PublicURL)); err == nil && parsed.Hostname() != "" {
			names = append(names, parsed.Hostname())
		}
	}
	return names
}

func hostIsLocalURL(raw string, host HostContext) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	return hostNameIsLocal(parsed.Hostname(), host)
}

func hostIsLocalAddress(raw string, host HostContext) bool {
	raw = strings.TrimSpace(raw)
	name, _, err := net.SplitHostPort(raw)
	if err != nil {
		name = strings.Trim(raw, "[]")
	}
	return hostNameIsLocal(name, host)
}

func hostNameIsLocal(name string, host HostContext) bool {
	local, _ := hostNameLocality(name, host)
	return local
}

// hostNameLocality deliberately has three states. An unrecorded hostname is
// unknown to the Dashboard and must not be silently treated as remote.
func hostNameLocality(name string, host HostContext) (local, known bool) {
	name = strings.Trim(strings.ToLower(strings.TrimSpace(name)), "[]")
	if name == "localhost" {
		return true, true
	}
	ip := net.ParseIP(name)
	if ip != nil {
		if ip.IsLoopback() {
			return true, true
		}
		if host.ResolvedHostLocality != nil {
			if address, ok := host.ResolvedHostLocality[ip.String()]; ok {
				return address != "", true
			}
			return false, false
		}
		for _, address := range host.HostAddresses {
			if candidate := net.ParseIP(strings.Trim(address, "[]")); candidate != nil && candidate.Equal(ip) {
				return true, true
			}
		}
		return false, true
	}
	name = normalizeHostname(name)
	if address, ok := host.ResolvedHostLocality[name]; ok {
		return address != "", true
	}
	return false, false
}

func normalizeHostname(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.Trim(name, "[]")
	return strings.TrimSuffix(name, ".")
}

func isStringSetSuperset(have, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, value := range have {
		set[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func serviceExposureClass(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "manual") {
		return "manual"
	}
	return "managed"
}
