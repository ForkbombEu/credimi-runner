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
)

// ServiceCapabilities describes capabilities that may safely be retained when
// a desired configuration removes them. Expanding any of these capabilities
// still requires service recreation. RedroidKnownHosts is the applied set of
// read-only mounts and follows the same safe-superset rule.
type ServiceCapabilities struct {
	NeedsHostADB      bool
	NeedsUSB          bool
	NeedsEmulator     bool
	RedroidKnownHosts []string
	NetworkMode       string
	HostAddresses     []string
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
	desiredProjection := serviceConfigProjectionForHost(cfg, configured, HostContext{OS: runtime.GOOS, HostAddresses: capabilities.HostAddresses})
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
	name = strings.Trim(strings.ToLower(strings.TrimSpace(name)), "[]")
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(name)
	if ip != nil {
		if ip.IsLoopback() {
			return true
		}
		for _, address := range host.HostAddresses {
			if candidate := net.ParseIP(strings.Trim(address, "[]")); candidate != nil && candidate.Equal(ip) {
				return true
			}
		}
		return false
	}
	if len(host.HostAddresses) == 0 {
		return false
	}
	resolved, err := net.LookupIP(name)
	if err != nil {
		return false
	}
	for _, candidate := range resolved {
		for _, address := range host.HostAddresses {
			if own := net.ParseIP(strings.Trim(address, "[]")); own != nil && own.Equal(candidate) {
				return true
			}
		}
	}
	return false
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
