package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
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
	Image     Component
	CheckedAt time.Time
	Error     string
}

type commandRunner func(context.Context, string, ...string) ([]byte, error)

type Checker struct {
	HTTPClient *http.Client
	Run        commandRunner
}

func (c Checker) Check(ctx context.Context, currentVersion string, currentBuiltAt time.Time, image string) Status {
	status := Status{CheckedAt: time.Now(), Runner: Component{CurrentVersion: currentVersion, CurrentBuiltAt: currentBuiltAt}, Image: Component{CurrentVersion: image}}
	var problems []string
	if err := c.checkRelease(ctx, &status.Runner); err != nil {
		problems = append(problems, "runner: "+err.Error())
	}
	if strings.TrimSpace(image) != "" {
		if err := c.checkImage(ctx, image, &status.Image); err != nil {
			problems = append(problems, "image: "+err.Error())
		}
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

func (c Checker) checkImage(ctx context.Context, image string, component *Component) error {
	run := c.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	localRaw, err := run(ctx, "docker", "image", "inspect", image)
	if err != nil {
		return fmt.Errorf("inspect installed image: %w", err)
	}
	var local []struct {
		ID, Created string
		RepoDigests []string
	}
	if err := json.Unmarshal(localRaw, &local); err != nil || len(local) == 0 {
		return errors.New("decode installed image metadata")
	}
	component.CurrentBuiltAt, _ = time.Parse(time.RFC3339Nano, local[0].Created)
	component.CurrentVersion = firstDigest(local[0].RepoDigests, local[0].ID)

	remoteRaw, err := run(ctx, "docker", "buildx", "imagetools", "inspect", image, "--format", "{{json .}}")
	if err != nil {
		return fmt.Errorf("inspect latest image: %w", err)
	}
	var remote map[string]any
	if err := json.Unmarshal(remoteRaw, &remote); err != nil {
		return fmt.Errorf("decode latest image metadata: %w", err)
	}
	component.LatestVersion = findString(remote, "Digest", "digest")
	created := findString(remote, "Created", "created")
	component.LatestBuiltAt, _ = time.Parse(time.RFC3339Nano, created)
	component.UpdateAvailable = component.LatestVersion != "" && !containsDigest(local[0].RepoDigests, component.LatestVersion)
	return nil
}

func normalizeVersion(value string) string { return strings.TrimPrefix(strings.TrimSpace(value), "v") }
func firstDigest(digests []string, fallback string) string {
	if len(digests) > 0 {
		return digests[0]
	}
	return fallback
}
func containsDigest(values []string, digest string) bool {
	for _, value := range values {
		if strings.HasSuffix(value, "@"+digest) || value == digest {
			return true
		}
	}
	return false
}
func findString(value any, keys ...string) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if found, ok := typed[key].(string); ok {
				return found
			}
		}
		for _, child := range typed {
			if found := findString(child, keys...); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findString(child, keys...); found != "" {
				return found
			}
		}
	}
	return ""
}

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
