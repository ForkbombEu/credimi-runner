package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const latestReleaseURL = "https://api.github.com/repos/ForkbombEu/credimi-runner/releases/latest"

type Component struct {
	CurrentVersion  string
	LatestVersion   string
	CurrentBuiltAt  time.Time
	LatestBuiltAt   time.Time
	UpdateAvailable bool
}

type Status struct {
	Runner    Component
	CheckedAt time.Time
	Error     string
}

type Checker struct {
	HTTPClient *http.Client
}

func (c Checker) Check(ctx context.Context, currentVersion string, currentBuiltAt time.Time) Status {
	status := Status{CheckedAt: time.Now(), Runner: Component{CurrentVersion: currentVersion, CurrentBuiltAt: currentBuiltAt}}
	var problems []string
	if err := c.checkRelease(ctx, &status.Runner); err != nil {
		problems = append(problems, "runner: "+err.Error())
	}
	status.Error = strings.Join(problems, "; ")
	return status
}

func (c Checker) checkRelease(ctx context.Context, component *Component) error {
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned %s", resp.Status)
	}
	var release struct {
		TagName     string    `json:"tag_name"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return err
	}
	component.LatestVersion = release.TagName
	component.LatestBuiltAt = release.PublishedAt
	component.UpdateAvailable = normalizeVersion(component.CurrentVersion) != normalizeVersion(release.TagName)
	return nil
}

func normalizeVersion(value string) string { return strings.TrimPrefix(strings.TrimSpace(value), "v") }

func AssetName(goos, goarch string) (string, error) {
	osPart := map[string]string{"linux": "Linux", "darwin": "Darwin"}[goos]
	if osPart == "" {
		return "", fmt.Errorf("unsupported operating system: %s", goos)
	}
	archPart := ""
	switch goos + ":" + goarch {
	case "linux:amd64", "darwin:amd64":
		archPart = "x86_64"
	case "linux:arm64":
		archPart = "aarch64"
	case "darwin:arm64":
		archPart = "arm64"
	}
	if archPart == "" {
		return "", fmt.Errorf("unsupported architecture: %s on %s", goarch, goos)
	}
	return "credimi-runner-" + osPart + "-" + archPart, nil
}

func CurrentAssetName() (string, error) { return AssetName(runtime.GOOS, runtime.GOARCH) }
