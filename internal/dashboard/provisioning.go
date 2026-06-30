package dashboard

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultBaseAVDArchiveURL = "https://files.pn-a.com/credimi_base_image.tar.gz"
	defaultGoldenArchiveURL  = "https://files.pn-a.com/credimi_golden.tar.gz"
)

var (
	provisioningHTTPClient = http.DefaultClient
	provisioningCommand    = exec.CommandContext
	provisioningReadDir    = os.ReadDir
	provisioningStat       = os.Stat
	provisioningMkdirAll   = os.MkdirAll
	provisioningOpenFile   = os.OpenFile
)

type IOSSimulatorOption struct {
	Label      string `json:"label"`
	Identifier string `json:"identifier"`
}

type IOSSimulatorStatus struct {
	Supported   bool                 `json:"supported"`
	Exists      bool                 `json:"exists"`
	Name        string               `json:"name"`
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
	BaseName      string                `json:"base_name"`
	AVDHome       string                `json:"avd_home"`
	GoldenRoot    string                `json:"golden_root"`
	GoldenLeaf    string                `json:"golden_leaf"`
	AVDPresent    bool                  `json:"avd_present"`
	GoldenPresent bool                  `json:"golden_present"`
	AVDOptions    []AndroidAVDOption    `json:"avd_options,omitempty"`
	GoldenOptions []AndroidGoldenOption `json:"golden_options,omitempty"`
}

type DownloadProgress struct {
	Phase string `json:"phase"`
	Bytes int64  `json:"bytes"`
	Total int64  `json:"total"`
	Error string `json:"error,omitempty"`
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

func iosSimulatorExists(ctx context.Context, name string) (bool, error) {
	out, err := runProvisioningCommand(ctx, "xcrun", "simctl", "list", "devices", "available")
	if err != nil {
		out, err = runProvisioningCommand(ctx, "xcrun", "simctl", "list", "devices")
		if err != nil {
			return false, err
		}
	}
	target := strings.TrimSpace(name) + " ("
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, target) {
			return true, nil
		}
	}
	return false, nil
}

func createIOSSimulator(ctx context.Context, name, deviceType, runtime string) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(deviceType) == "" || strings.TrimSpace(runtime) == "" {
		return fmt.Errorf("name, device type, and runtime are required")
	}
	_, err := runProvisioningCommand(ctx, "xcrun", "simctl", "create", name, deviceType, runtime)
	return err
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
	if strings.TrimSpace(avdHome) == "" || strings.TrimSpace(avdName) == "" {
		return false
	}
	avdDir := filepath.Join(avdHome, avdName+".avd")
	iniPath := filepath.Join(avdHome, avdName+".ini")
	return pathExists(avdDir) && pathExists(iniPath)
}

func listAVDOptions(avdHome string) []AndroidAVDOption {
	entries, err := provisioningReadDir(avdHome)
	if err != nil {
		return nil
	}
	var options []AndroidAVDOption
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".avd") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".avd")
		if !pathExists(filepath.Join(avdHome, name+".ini")) {
			continue
		}
		options = append(options, AndroidAVDOption{Name: name, Path: avdHome})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Name < options[j].Name })
	return options
}

func goldenAssetsPresent(goldenRoot string) bool {
	if strings.TrimSpace(goldenRoot) == "" {
		return false
	}
	if pathExists(filepath.Join(goldenRoot, "credimi-golden")) {
		return true
	}
	return filepath.Base(strings.TrimRight(goldenRoot, string(os.PathSeparator))) == "credimi-golden" && pathExists(goldenRoot)
}

func goldenAssetsPresentForLeaf(goldenRoot, goldenLeaf string) bool {
	goldenRoot = strings.TrimSpace(goldenRoot)
	goldenLeaf = strings.Trim(strings.TrimSpace(goldenLeaf), `/\`)
	if goldenRoot == "" || goldenLeaf == "" {
		return false
	}
	return pathExists(filepath.Join(goldenRoot, goldenLeaf))
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
	if strings.TrimSpace(root) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var options []AndroidGoldenOption
	add := func(name, path string) {
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		options = append(options, AndroidGoldenOption{Name: name, Path: path})
	}
	entries, err := provisioningReadDir(root)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			child := filepath.Join(root, entry.Name())
			add(entry.Name(), child)
		}
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Path < options[j].Path })
	return options
}

func downloadAndExtractTarball(ctx context.Context, archiveURL, destDir string, progress func(DownloadProgress)) error {
	if err := provisioningMkdirAll(destDir, 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return err
	}
	resp, err := provisioningHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	if progress != nil {
		progress(DownloadProgress{Phase: "downloading", Total: resp.ContentLength})
	}
	gzipReader, err := gzip.NewReader(&progressReader{reader: resp.Body, total: resp.ContentLength, progress: progress})
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	if progress != nil {
		progress(DownloadProgress{Phase: "extracting", Total: resp.ContentLength})
	}
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, header.Name)
		cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)
		cleanTarget := filepath.Clean(target)
		if !strings.HasPrefix(cleanTarget, cleanDest) && cleanTarget != filepath.Clean(destDir) {
			return fmt.Errorf("refusing to extract %s outside destination", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := provisioningMkdirAll(cleanTarget, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := provisioningMkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil {
				return err
			}
			file, err := provisioningOpenFile(cleanTarget, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, tarReader); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		}
	}
}

func runProvisioningCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := provisioningCommand(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := provisioningStat(path)
	return err == nil
}

type progressReader struct {
	reader   io.Reader
	read     int64
	total    int64
	progress func(DownloadProgress)
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.reader.Read(buf)
	p.read += int64(n)
	if p.progress != nil && n > 0 {
		p.progress(DownloadProgress{Phase: "downloading", Bytes: p.read, Total: p.total})
	}
	return n, err
}
