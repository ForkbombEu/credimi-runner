package maintenance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const latestDownloadBase = "https://github.com/ForkbombEu/credimi-runner/releases/latest/download/"

func DownloadLatestBinary(ctx context.Context, client *http.Client, target string, progress func(string)) error {
	asset, err := CurrentAssetName()
	if err != nil {
		return err
	}
	if client == nil {
		client = http.DefaultClient
	}
	url := latestDownloadBase + asset
	emit(progress, "Downloading "+url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download runner binary: %s", resp.Status)
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
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	emit(progress, "Installed latest Credimi Runner at "+target)
	return nil
}

func emit(progress func(string), line string) {
	if progress != nil {
		progress(line)
	}
}
