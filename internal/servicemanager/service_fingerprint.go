package servicemanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

const AppliedServiceConfigFingerprintEnv = "CREDIMI_APPLIED_SERVICE_CONFIG_FINGERPRINT"

type serviceConfigProjection struct {
	Configured        bool                     `json:"configured"`
	APIListen         string                   `json:"api_listen"`
	DashboardListen   string                   `json:"dashboard_listen"`
	ReadHeaderTimeout string                   `json:"read_header_timeout"`
	ShutdownTimeout   string                   `json:"shutdown_timeout"`
	ExposureClass     string                   `json:"exposure_class"`
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
	// Persisted TOML is normally loaded with defaults applied. Applying them
	// here as well keeps callers that hold a freshly assembled Config on the
	// same canonical projection.
	_ = config.ApplyDefaults(&cfg)
	projection := serviceConfigProjection{
		Configured:        configured,
		APIListen:         cfg.Server.APIListen,
		DashboardListen:   cfg.Server.DashboardListen,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Duration().String(),
		ShutdownTimeout:   cfg.Server.ShutdownTimeout.Duration().String(),
		ExposureClass:     serviceExposureClass(cfg.Exposure.Mode),
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
	payload, _ := json.Marshal(projection)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func serviceExposureClass(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "manual") {
		return "manual"
	}
	return "managed"
}
