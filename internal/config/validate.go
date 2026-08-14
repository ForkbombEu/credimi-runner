package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var canonicalID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:/[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`)

func ValidateForPlatform(cfg Config, goos string) error {
	if cfg.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", cfg.SchemaVersion)
	}
	if err := required("runner.id", cfg.Runner.ID); err != nil {
		return err
	}
	if !canonicalID.MatchString(cfg.Runner.ID) {
		return fmt.Errorf("runner.id must be a canonical organization/name ID")
	}
	if err := required("runner.name", cfg.Runner.Name); err != nil {
		return err
	}
	if err := required("runner.organization", cfg.Runner.Organization); err != nil {
		return err
	}
	if !strings.HasPrefix(cfg.Runner.ID, cfg.Runner.Organization+"/") {
		return errorsf("runner.id must belong to runner.organization")
	}
	if err := required("credimi.url", cfg.Credimi.URL); err != nil {
		return err
	}
	parsedURL, err := url.ParseRequestURI(cfg.Credimi.URL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return errorsf("credimi.url must be an absolute URL")
	}
	if err := validateAuth(cfg.Credimi); err != nil {
		return err
	}
	if err := required("temporal.address", cfg.Temporal.Address); err != nil {
		return err
	}
	if err := validateListen("server.api_listen", cfg.Server.APIListen); err != nil {
		return err
	}
	if err := validateListen("server.dashboard_listen", cfg.Server.DashboardListen); err != nil {
		return err
	}
	if cfg.Server.ReadHeaderTimeout <= 0 || cfg.Server.ShutdownTimeout <= 0 {
		return errorsf("server durations must be positive")
	}
	if cfg.Storage.ArtifactRetention <= 0 {
		return errorsf("storage.artifact_retention must be positive")
	}
	if err := required("storage.state_dir", cfg.Storage.StateDir); err != nil {
		return err
	}
	if !filepath.IsAbs(cfg.Storage.StateDir) {
		return errorsf("storage.state_dir must be absolute")
	}
	if err := validateExposure(cfg.Exposure); err != nil {
		return err
	}
	if err := validateAndroid(cfg.Android); err != nil {
		return err
	}

	ids, names, serials := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	emulators, simulators := 0, 0
	for index, device := range cfg.Devices {
		label := fmt.Sprintf("devices[%d]", index)
		if err := required(label+".id", device.ID); err != nil {
			return err
		}
		if !strings.HasPrefix(device.ID, cfg.Runner.ID+"/") || !canonicalID.MatchString(device.ID) {
			return fmt.Errorf("%s.id must be a canonical child of runner.id", label)
		}
		if err := unique(ids, device.ID, label+".id"); err != nil {
			return err
		}
		if err := required(label+".name", device.Name); err != nil {
			return err
		}
		if err := unique(names, device.Name, label+".name"); err != nil {
			return err
		}
		if err := validateDevice(device, label, goos, serials); err != nil {
			return err
		}
		switch device.Type {
		case DeviceAndroidEmulator:
			emulators++
		case DeviceIOSSimulator:
			simulators++
		}
	}
	if emulators > 1 {
		return errorsf("only one Android emulator device is allowed")
	}
	if simulators > 1 {
		return errorsf("only one iOS Simulator device is allowed")
	}
	return nil
}

func validateDevice(device DeviceConfig, label, goos string, serials map[string]struct{}) error {
	subtables := 0
	if device.AndroidPhysical != nil {
		subtables++
	}
	if device.AndroidEmulator != nil {
		subtables++
	}
	if device.Redroid != nil {
		subtables++
	}
	if device.IOSSimulator != nil {
		subtables++
	}
	if subtables != 1 {
		return fmt.Errorf("%s must contain exactly one type-specific subtable", label)
	}
	switch device.Type {
	case DeviceAndroidPhysical:
		if device.AndroidPhysical == nil {
			return fmt.Errorf("%s.type android_physical requires [devices.android_physical]", label)
		}
		switch device.AndroidPhysical.Transport {
		case "usb":
			if err := required(label+".android_physical.serial", device.AndroidPhysical.Serial); err != nil {
				return err
			}
			if device.AndroidPhysical.WiFiIP != "" || device.AndroidPhysical.WiFiPort != "" {
				return fmt.Errorf("%s.android_physical.usb must not configure Wi-Fi addressing", label)
			}
			return unique(serials, device.AndroidPhysical.Serial, label+".android_physical.serial")
		case "wifi":
			if err := required(label+".android_physical.wifi_ip", device.AndroidPhysical.WiFiIP); err != nil {
				return err
			}
			if device.AndroidPhysical.Serial != "" {
				return fmt.Errorf("%s.android_physical.wifi must not configure serial", label)
			}
			if device.AndroidPhysical.WiFiPort == "" {
				device.AndroidPhysical.WiFiPort = "5555"
			}
			if !validPort(device.AndroidPhysical.WiFiPort) {
				return fmt.Errorf("%s.android_physical.wifi_port must be between 1 and 65535", label)
			}
			return unique(serials, net.JoinHostPort(device.AndroidPhysical.WiFiIP, device.AndroidPhysical.WiFiPort), label+".android_physical.wifi")
		case "no_device":
			if device.AndroidPhysical.Serial != "" || device.AndroidPhysical.WiFiIP != "" || device.AndroidPhysical.WiFiPort != "" {
				return fmt.Errorf("%s.android_physical.no_device must not configure an address", label)
			}
			return nil
		default:
			return fmt.Errorf("%s.android_physical.transport must be wifi, usb, or no_device", label)
		}
	case DeviceAndroidEmulator:
		if device.AndroidEmulator == nil {
			return fmt.Errorf("%s.type android_emulator requires [devices.android_emulator]", label)
		}
		if goos != "linux" && goos != "darwin" {
			return errorsf("Android emulator devices require Linux or macOS")
		}
		for name, value := range map[string]string{"abi": device.AndroidEmulator.ABI, "system_image": device.AndroidEmulator.SystemImage, "base_name": device.AndroidEmulator.BaseName, "golden_source": device.AndroidEmulator.GoldenSource} {
			if err := required(label+".android_emulator."+name, value); err != nil {
				return err
			}
		}
		if device.AndroidEmulator.APILevel <= 0 || device.AndroidEmulator.MemoryMB <= 0 || device.AndroidEmulator.Cores <= 0 {
			return fmt.Errorf("%s.android_emulator API level, memory_mb, and cores must be positive", label)
		}
		return nil
	case DeviceRedroid:
		if device.Redroid == nil {
			return fmt.Errorf("%s.type redroid requires [devices.redroid]", label)
		}
		if goos != "linux" && goos != "darwin" {
			return errorsf("Redroid devices require Linux or macOS")
		}
		for name, value := range map[string]string{"host": device.Redroid.Host, "image": device.Redroid.Image, "data_dir": device.Redroid.DataDir, "data_archive": device.Redroid.DataArchive} {
			if err := required(label+".redroid."+name, value); err != nil {
				return err
			}
		}
		if device.Redroid.ADBPort < 1 || device.Redroid.ADBPort > 65535 {
			return fmt.Errorf("%s.redroid.adb_port must be between 1 and 65535", label)
		}
		return unique(serials, net.JoinHostPort(device.Redroid.Host, strconv.Itoa(device.Redroid.ADBPort)), label+".redroid")
	case DeviceIOSSimulator:
		if device.IOSSimulator == nil {
			return fmt.Errorf("%s.type ios_simulator requires [devices.ios_simulator]", label)
		}
		if goos != "darwin" {
			return errorsf("iOS Simulator devices require macOS")
		}
		return required(label+".ios_simulator.udid", device.IOSSimulator.UDID)
	default:
		return fmt.Errorf("%s.type %q is unsupported", label, device.Type)
	}
}

func validateAuth(cfg CredimiConfig) error {
	user, admin := strings.TrimSpace(cfg.UserAPIKey), strings.TrimSpace(cfg.InternalAdminKey)
	switch cfg.AuthMode {
	case "user":
		if user == "" || admin != "" {
			return errorsf("credimi.auth_mode user requires exactly user_api_key")
		}
	case "internal_admin":
		if admin == "" || user != "" {
			return errorsf("credimi.auth_mode internal_admin requires exactly internal_admin_key")
		}
	default:
		return errorsf("credimi.auth_mode must be user or internal_admin")
	}
	return nil
}

func validateExposure(cfg ExposureConfig) error {
	if port := strings.TrimSpace(cfg.PublicPort); port != "" && !validPort(port) {
		return errorsf("exposure.public_port must be a valid TCP port")
	}
	switch cfg.Mode {
	case "manual":
		if strings.TrimSpace(cfg.PublicURL) == "" {
			return errorsf("manual exposure requires public_url")
		}
		parsed, err := url.ParseRequestURI(cfg.PublicURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errorsf("exposure.public_url must be an absolute URL")
		}
	case "quick_tunnel":
		if strings.TrimSpace(cfg.CloudflareToken) != "" {
			return errorsf("quick_tunnel must not configure cloudflare_token")
		}
	case "named_tunnel":
		if strings.TrimSpace(cfg.Domain) == "" {
			return errorsf("named_tunnel requires domain")
		}
		if strings.TrimSpace(cfg.CloudflareToken) == "" {
			return errorsf("named_tunnel requires cloudflare_token")
		}
		if strings.Contains(cfg.Domain, "://") {
			parsed, err := url.ParseRequestURI(cfg.Domain)
			if err != nil || parsed.Host == "" {
				return errorsf("exposure.domain must be a hostname or absolute URL")
			}
		}
	default:
		return errorsf("exposure.mode must be manual, quick_tunnel, or named_tunnel")
	}
	return nil
}

func validPort(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	port, err := strconv.Atoi(value)
	return err == nil && port >= 1 && port <= 65535
}

func validateAndroid(cfg AndroidConfig) error {
	if err := required("android.runner_image", cfg.RunnerImage); err != nil {
		return err
	}
	if cfg.PullPolicy != "always" && cfg.PullPolicy != "if-not-present" && cfg.PullPolicy != "never" {
		return errorsf("android.pull_policy must be always, if-not-present, or never")
	}
	for name, value := range map[string]string{"network": cfg.Network, "state_volume": cfg.StateVolume, "tool_cache_volume": cfg.ToolCacheVolume, "sdk_volume": cfg.SDKVolume} {
		if err := required("android."+name, value); err != nil {
			return err
		}
	}
	return nil
}

func validateListen(name, value string) error {
	if _, _, err := net.SplitHostPort(value); err != nil {
		return fmt.Errorf("%s must be host:port: %w", name, err)
	}
	return nil
}
func required(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}
func unique(seen map[string]struct{}, value, label string) error {
	if _, exists := seen[value]; exists {
		return fmt.Errorf("duplicate %s %q", label, value)
	}
	seen[value] = struct{}{}
	return nil
}
func errorsf(format string, args ...any) error { return fmt.Errorf(format, args...) }
