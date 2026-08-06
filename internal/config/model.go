// Package config owns the one canonical, typed Credimi Runner configuration.
package config

import (
	"fmt"
	"time"
)

const SchemaVersion = 1

type DeviceType string

type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Duration() time.Duration      { return time.Duration(d) }
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

const (
	DeviceAndroidPhysical DeviceType = "android_physical"
	DeviceAndroidEmulator DeviceType = "android_emulator"
	DeviceRedroid         DeviceType = "redroid"
	DeviceIOSSimulator    DeviceType = "ios_simulator"
)

type Config struct {
	SchemaVersion int                 `toml:"schema_version"`
	Runner        RunnerConfig        `toml:"runner"`
	Credimi       CredimiConfig       `toml:"credimi"`
	Temporal      TemporalConfig      `toml:"temporal"`
	Server        ServerConfig        `toml:"server"`
	Exposure      ExposureConfig      `toml:"exposure"`
	Observability ObservabilityConfig `toml:"observability"`
	Storage       StorageConfig       `toml:"storage"`
	Android       AndroidConfig       `toml:"android"`
	Devices       []DeviceConfig      `toml:"devices"`
}

type RunnerConfig struct {
	ID           string `toml:"id"`
	Name         string `toml:"name"`
	Organization string `toml:"organization"`
	Description  string `toml:"description"`
	Published    bool   `toml:"published"`
}

type CredimiConfig struct {
	URL              string `toml:"url"`
	AuthMode         string `toml:"auth_mode"`
	UserAPIKey       string `toml:"user_api_key"`
	InternalAdminKey string `toml:"internal_admin_key"`
}

type TemporalConfig struct {
	Address string `toml:"address"`
}

type ServerConfig struct {
	APIListen         string   `toml:"api_listen"`
	DashboardListen   string   `toml:"dashboard_listen"`
	OpenBrowser       bool     `toml:"open_browser"`
	DashboardToken    string   `toml:"dashboard_token"`
	ReadHeaderTimeout Duration `toml:"read_header_timeout"`
	ShutdownTimeout   Duration `toml:"shutdown_timeout"`
}

type ExposureConfig struct {
	Mode            string `toml:"mode"`
	PublicURL       string `toml:"public_url"`
	CloudflareToken string `toml:"cloudflare_token"`
}

type ObservabilityConfig struct {
	Enabled      bool   `toml:"enabled"`
	ServiceName  string `toml:"service_name"`
	OTLPEndpoint string `toml:"otlp_endpoint"`
}

type StorageConfig struct {
	StateDir          string   `toml:"state_dir"`
	TempDir           string   `toml:"temp_dir"`
	ArtifactRetention Duration `toml:"artifact_retention"`
}

type AndroidConfig struct {
	AgentImage         string `toml:"agent_image"`
	AgentPullPolicy    string `toml:"agent_pull_policy"`
	AgentHostPort      int    `toml:"agent_host_port"`
	AgentContainerPort int    `toml:"agent_container_port"`
	AgentNetwork       string `toml:"agent_network"`
	CommonDataVolume   string `toml:"common_data_volume"`
	ToolCacheVolume    string `toml:"tool_cache_volume"`
}

type DeviceConfig struct {
	ID              string                 `toml:"id"`
	Name            string                 `toml:"name"`
	Description     string                 `toml:"description"`
	Type            DeviceType             `toml:"type"`
	Enabled         bool                   `toml:"enabled"`
	AndroidPhysical *AndroidPhysicalConfig `toml:"android_physical"`
	AndroidEmulator *AndroidEmulatorConfig `toml:"android_emulator"`
	Redroid         *RedroidConfig         `toml:"redroid"`
	IOSSimulator    *IOSSimulatorConfig    `toml:"ios_simulator"`
}

type AndroidPhysicalConfig struct {
	Transport string `toml:"transport"`
	Serial    string `toml:"serial"`
}

type AndroidEmulatorConfig struct {
	AVDName      string `toml:"avd_name"`
	APILevel     int    `toml:"api_level"`
	ABI          string `toml:"abi"`
	SystemImage  string `toml:"system_image"`
	BaseName     string `toml:"base_name"`
	GoldenSource string `toml:"golden_source"`
	Headless     bool   `toml:"headless"`
	MemoryMB     int    `toml:"memory_mb"`
	Cores        int    `toml:"cores"`
}

type RedroidConfig struct {
	Image       string `toml:"image"`
	Serial      string `toml:"serial"`
	DataDir     string `toml:"data_dir"`
	DataArchive string `toml:"data_archive"`
	ADBPort     int    `toml:"adb_port"`
}

type IOSSimulatorConfig struct {
	UDID string `toml:"udid"`
}
