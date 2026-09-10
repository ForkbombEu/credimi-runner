package maintenance

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func DownloadLatestBinary(ctx context.Context, client *http.Client, target string, progress func(string)) error {
	asset, err := CurrentAssetName()
	if err != nil {
		return err
	}
	if client == nil {
		client = http.DefaultClient
	}
	emit(progress, "Resolving latest Credimi Runner release")
	release, err := fetchLatestRelease(ctx, client)
	if err != nil {
		return fmt.Errorf("resolve latest Credimi Runner release: %w", err)
	}
	binaryAsset, err := releaseAsset(release, asset, "runner binary")
	if err != nil {
		return err
	}
	checksumName := "credimi-runner_" + release.TagName + "_checksums.txt"
	checksumAsset, err := releaseAsset(release, checksumName, "runner checksum")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".credimi-runner-upgrade-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Close(); err != nil {
		return err
	}
	emit(progress, "Downloading "+binaryAsset.BrowserDownloadURL)
	if err := downloadAsset(ctx, client, binaryAsset.BrowserDownloadURL, tmpPath, "runner binary"); err != nil {
		return err
	}

	checksumFile, err := os.CreateTemp(filepath.Dir(target), ".credimi-runner-checksums-*")
	if err != nil {
		return err
	}
	checksumPath := checksumFile.Name()
	defer os.Remove(checksumPath)
	if err := checksumFile.Close(); err != nil {
		return err
	}
	emit(progress, "Downloading "+checksumAsset.BrowserDownloadURL)
	if err := downloadAsset(ctx, client, checksumAsset.BrowserDownloadURL, checksumPath, "runner checksum"); err != nil {
		return err
	}
	expected, err := parseChecksumFile(checksumPath, asset)
	if err != nil {
		return err
	}
	actual, err := sha256File(tmpPath)
	if err != nil {
		return fmt.Errorf("hash runner binary: %w", err)
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", asset, actual, expected)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	emit(progress, "Installed Credimi Runner at "+target)
	return nil
}

func releaseAsset(release githubRelease, name, kind string) (githubReleaseAsset, error) {
	var asset githubReleaseAsset
	count := 0
	for _, candidate := range release.Assets {
		if candidate.Name == name {
			asset = candidate
			count++
		}
	}
	if count == 0 {
		return asset, fmt.Errorf("release %s is missing %s asset %q", release.TagName, kind, name)
	}
	if count != 1 {
		return asset, fmt.Errorf("release %s contains %d %s assets named %q", release.TagName, count, kind, name)
	}
	if strings.TrimSpace(asset.BrowserDownloadURL) == "" {
		return asset, fmt.Errorf("release %s has an empty download URL for %s asset %q", release.TagName, kind, name)
	}
	return asset, nil
}

func downloadAsset(ctx context.Context, client *http.Client, url, path, kind string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("request %s: %w", kind, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", kind, err)
	}
	if resp.StatusCode != http.StatusOK {
		status := resp.Status
		resp.Body.Close()
		return fmt.Errorf("download %s: %s", kind, status)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		resp.Body.Close()
		return fmt.Errorf("open %s temporary file: %w", kind, err)
	}
	_, copyErr := io.Copy(file, resp.Body)
	bodyErr := resp.Body.Close()
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download %s: %w", kind, copyErr)
	}
	if bodyErr != nil {
		return fmt.Errorf("close %s response: %w", kind, bodyErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync %s temporary file: %w", kind, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s temporary file: %w", kind, closeErr)
	}
	return nil
}

func parseChecksumFile(path, asset string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read checksum file: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	entries := 0
	var digest string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return "", fmt.Errorf("malformed checksum entry for %s", asset)
		}
		if fields[1] != asset {
			continue
		}
		entries++
		digest = fields[0]
		if len(digest) != sha256.Size*2 {
			return "", fmt.Errorf("malformed SHA256 for %s", asset)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return "", fmt.Errorf("malformed SHA256 for %s: %w", asset, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read checksum file: %w", err)
	}
	if entries == 0 {
		return "", fmt.Errorf("checksum file does not contain an entry for %s", asset)
	}
	if entries != 1 {
		return "", fmt.Errorf("checksum file contains %d entries for %s; want exactly one", entries, asset)
	}
	return digest, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func emit(progress func(string), line string) {
	if progress != nil {
		progress(line)
	}
}
