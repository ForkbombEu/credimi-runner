package dashboard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/forkbombeu/credimi-runner/internal/androidtools"
)

var provisioningCommand = exec.CommandContext

type IOSSimulatorOption struct {
	Label      string `json:"label"`
	Identifier string `json:"identifier"`
}

type IOSSimulatorStatus struct {
	Supported   bool                 `json:"supported"`
	Exists      bool                 `json:"exists"`
	Name        string               `json:"name"`
	UDID        string               `json:"udid,omitempty"`
	DeviceTypes []IOSSimulatorOption `json:"device_types,omitempty"`
	Runtimes    []IOSSimulatorOption `json:"runtimes,omitempty"`
}

type AndroidAVDOption struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type AndroidGoldenOption struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type AndroidEmulatorAssetsStatus struct {
	BaseName       string                `json:"base_name"`
	AndroidKeysDir string                `json:"android_keys_dir"`
	AVDHome        string                `json:"avd_home"`
	GoldenRoot     string                `json:"golden_root"`
	GoldenLeaf     string                `json:"golden_leaf"`
	AVDPresent     bool                  `json:"avd_present"`
	GoldenPresent  bool                  `json:"golden_present"`
	AVDOptions     []AndroidAVDOption    `json:"avd_options,omitempty"`
	GoldenOptions  []AndroidGoldenOption `json:"golden_options,omitempty"`
}

func listIOSSimulatorDeviceTypes(ctx context.Context) ([]IOSSimulatorOption, error) {
	out, err := runProvisioningCommand(ctx, "xcrun", "simctl", "list", "devicetypes")
	if err != nil {
		return nil, err
	}
	var options []IOSSimulatorOption
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "==") {
			continue
		}
		label, identifier, ok := parseSimctlDeviceTypeLine(line)
		if ok {
			options = append(options, IOSSimulatorOption{Label: label, Identifier: identifier})
		}
	}
	return options, nil
}

func listIOSSimulatorRuntimes(ctx context.Context) ([]IOSSimulatorOption, error) {
	out, err := runProvisioningCommand(ctx, "xcrun", "simctl", "list", "runtimes")
	if err != nil {
		return nil, err
	}
	var options []IOSSimulatorOption
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "==") || strings.Contains(strings.ToLower(line), "unavailable") {
			continue
		}
		label, identifier, ok := parseSimctlRuntimeLine(line)
		if ok {
			options = append(options, IOSSimulatorOption{Label: label, Identifier: identifier})
		}
	}
	return options, nil
}

func iosSimulatorUDID(output, name string) string {
	target := strings.TrimSpace(name) + " ("
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, target) {
			continue
		}
		rest := line[len(target):]
		if end := strings.IndexByte(rest, ')'); end >= 0 {
			first := strings.TrimSpace(rest[:end])
			if first != "" && !strings.EqualFold(first, "unavailable") {
				return first
			}
			rest = rest[end+1:]
		}
		if start, end := strings.IndexByte(rest, '('), strings.IndexByte(rest, ')'); start >= 0 && end > start {
			return strings.TrimSpace(rest[start+1 : end])
		}
	}
	return ""
}

func iosSimulatorExists(ctx context.Context, name string) (bool, string, error) {
	out, err := runProvisioningCommand(ctx, "xcrun", "simctl", "list", "devices", "available")
	if err != nil {
		out, err = runProvisioningCommand(ctx, "xcrun", "simctl", "list", "devices")
		if err != nil {
			return false, "", err
		}
	}
	udid := iosSimulatorUDID(out, name)
	return udid != "", udid, nil
}

func createIOSSimulator(ctx context.Context, name, deviceType, runtime string) (string, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(deviceType) == "" || strings.TrimSpace(runtime) == "" {
		return "", fmt.Errorf("name, device type, and runtime are required")
	}
	out, err := runProvisioningCommand(ctx, "xcrun", "simctl", "create", name, deviceType, runtime)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func parseSimctlDeviceTypeLine(line string) (string, string, bool) {
	start := strings.LastIndex(line, "(")
	end := strings.LastIndex(line, ")")
	if start <= 0 || end <= start {
		return "", "", false
	}
	label := strings.TrimSpace(line[:start])
	identifier := strings.TrimSpace(line[start+1 : end])
	if !strings.HasPrefix(identifier, "com.apple.CoreSimulator.SimDeviceType.") {
		return "", "", false
	}
	return label, identifier, true
}

func parseSimctlRuntimeLine(line string) (string, string, bool) {
	parts := strings.Split(line, " - ")
	if len(parts) < 2 {
		return "", "", false
	}
	identifier := strings.TrimSpace(parts[len(parts)-1])
	if !strings.HasPrefix(identifier, "com.apple.CoreSimulator.SimRuntime.") {
		return "", "", false
	}
	label := strings.TrimSpace(strings.Join(parts[:len(parts)-1], " - "))
	return label, identifier, true
}

func avdAssetsExistForName(avdHome, avdName string) bool {
	return androidtools.AVDAssetsExist(avdHome, avdName)
}

func listAVDOptions(avdHome string) []AndroidAVDOption {
	names := androidtools.ListAVDOptions(avdHome)
	options := make([]AndroidAVDOption, 0, len(names))
	for _, name := range names {
		options = append(options, AndroidAVDOption{Name: name, Path: avdHome})
	}
	return options
}

func goldenAssetsPresent(goldenRoot string) bool {
	if strings.TrimSpace(goldenRoot) == "" {
		return false
	}
	return androidtools.GoldenAssetsExist(goldenRoot, "credimi-golden") || androidtools.GoldenAssetsExist(filepath.Dir(goldenRoot), filepath.Base(goldenRoot))
}

func goldenAssetsPresentForLeaf(goldenRoot, goldenLeaf string) bool {
	return androidtools.GoldenAssetsExist(goldenRoot, goldenLeaf)
}

func goldenLeafFromBaseName(baseName string) string {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		baseName = "credimi"
	}
	return baseName + "-golden"
}

func goldenLeafFromPath(goldenPath, baseName string) string {
	goldenPath = strings.TrimSpace(goldenPath)
	if goldenPath == "" {
		return goldenLeafFromBaseName(baseName)
	}
	leaf := filepath.Base(strings.TrimRight(goldenPath, `/\`))
	if leaf == "." || leaf == string(os.PathSeparator) || leaf == "" {
		return goldenLeafFromBaseName(baseName)
	}
	return leaf
}

func listGoldenOptions(root string) []AndroidGoldenOption {
	names := androidtools.ListGoldenOptions(root)
	options := make([]AndroidGoldenOption, 0, len(names))
	for _, name := range names {
		options = append(options, AndroidGoldenOption{Name: name, Path: filepath.Join(root, name)})
	}
	return options
}

type DownloadProgress = androidtools.DownloadProgress

var downloadAndroidAssets = androidtools.DownloadAndExtractTarball

func downloadAndExtractTarball(ctx context.Context, archiveURL, destDir string, progress func(DownloadProgress)) error {
	return downloadAndroidAssets(ctx, archiveURL, destDir, progress)
}

func runProvisioningCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := provisioningCommand(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
