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
	Configured      bool                      `json:"configured"`
	APIListen       string                    `json:"api_listen"`
	DashboardListen string                    `json:"dashboard_listen"`
	ExposureClass   string                    `json:"exposure_class"`
	Android         serviceAndroidProjection  `json:"android"`
	Devices         []serviceDeviceProjection `json:"devices"`
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

type serviceDeviceProjection struct {
	Type                  config.DeviceType `json:"type"`
	Enabled               bool              `json:"enabled"`
	PhysicalTransport     string            `json:"physical_transport,omitempty"`
	RedroidSSHTarget      string            `json:"redroid_ssh_target,omitempty"`
	RedroidKnownHostsPath string            `json:"redroid_known_hosts_path,omitempty"`
}

// ServiceConfigFingerprint identifies only persisted settings that can make
// the current service/container topology stale. Runtime credentials and
// execution settings intentionally do not participate.
func ServiceConfigFingerprint(cfg config.Config, configured bool) string {
	// Persisted TOML is normally loaded with defaults applied. Applying them
	// here as well keeps callers that hold a freshly assembled Config on the
	// same canonical projection.
	_ = config.ApplyDefaults(&cfg)
	projection := serviceConfigProjection{
		Configured:      configured,
		APIListen:       cfg.Server.APIListen,
		DashboardListen: cfg.Server.DashboardListen,
		ExposureClass:   serviceExposureClass(cfg.Exposure.Mode),
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
	for _, device := range cfg.Devices {
		item := serviceDeviceProjection{Type: device.Type, Enabled: device.Enabled}
		if device.AndroidPhysical != nil {
			item.PhysicalTransport = device.AndroidPhysical.Transport
		}
		if device.Redroid != nil {
			item.RedroidSSHTarget = device.Redroid.AVDCTLSSHTarget
			item.RedroidKnownHostsPath = device.Redroid.AVDCTLSSHKnownHostsPath
		}
		projection.Devices = append(projection.Devices, item)
	}
	sort.Slice(projection.Devices, func(i, j int) bool {
		left, right := projection.Devices[i], projection.Devices[j]
		leftKey, _ := json.Marshal(left)
		rightKey, _ := json.Marshal(right)
		return string(leftKey) < string(rightKey)
	})
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
