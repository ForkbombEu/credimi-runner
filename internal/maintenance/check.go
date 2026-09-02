package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

type Checker struct {
	HTTPClient *http.Client
	ConfigDir  string
}

func (c Checker) Check(ctx context.Context, currentVersion string, currentBuiltAt time.Time) Status {
	status := Status{CheckedAt: time.Now(), Runner: Component{CurrentVersion: currentVersion, CurrentBuiltAt: currentBuiltAt}}
	var problems []string
	if err := c.checkRelease(ctx, &status.Runner); err != nil {
		problems = append(problems, "runner: "+err.Error())
	}
	if runtime.GOOS != "darwin" {
		if err := c.checkImage(ctx, &status.Image); err != nil {
			problems = append(problems, "image: "+err.Error())
		}
	}
	status.Error = strings.Join(problems, "; ")
	return status
}

type ServiceImageState struct {
	Image     string    `json:"image"`
	Digest    string    `json:"digest"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

func ReadImageState(configDir string) (ServiceImageState, error) {
	raw, err := os.ReadFile(filepath.Join(configDir, "service-image-state.json"))
	if err != nil {
		return ServiceImageState{}, err
	}
	var state ServiceImageState
	if err := json.Unmarshal(raw, &state); err != nil {
		return ServiceImageState{}, err
	}
	return state, nil
}

func (c Checker) checkImage(ctx context.Context, component *Component) error {
	state, err := ReadImageState(c.ConfigDir)
	if err != nil {
		return fmt.Errorf("applied image state unavailable: %w", err)
	}
	if strings.TrimSpace(state.Image) == "" {
		return fmt.Errorf("applied image reference unavailable")
	}
	component.CurrentVersion = state.Digest
	if !validDigest(state.Digest) {
		return fmt.Errorf("applied image digest unavailable")
	}
	if at := strings.LastIndex(state.Image, "@"); at >= 0 && normalizeDigest(state.Image[at+1:]) != normalizeDigest(state.Digest) {
		return fmt.Errorf("applied image reference and digest are inconsistent")
	}
	digest, err := remoteDigest(ctx, c.client(), state.Image)
	if err != nil {
		return err
	}
	component.LatestVersion = digest
	component.UpdateAvailable = normalizeDigest(state.Digest) != normalizeDigest(digest)
	return nil
}

func (c Checker) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func normalizeDigest(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "sha256:") }

func validDigest(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "sha256:") && len(strings.TrimPrefix(v, "sha256:")) > 0
}

func remoteDigest(ctx context.Context, client *http.Client, image string) (string, error) {
	registry, repo, ref, err := parseImage(image)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(ref, "sha256:") {
		if !validDigest(ref) {
			return "", fmt.Errorf("invalid image digest")
		}
		return ref, nil
	}
	base := "https://" + registry
	if strings.Contains(registry, "://") {
		base = registry
	}
	manifestURL := base + "/v2/" + repo + "/manifests/" + ref
	token := ""
	resp, err := manifestRequest(ctx, client, http.MethodHead, manifestURL, token)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		token, err = bearerToken(ctx, client, challenge)
		if err != nil {
			return "", err
		}
		resp, err = manifestRequest(ctx, client, http.MethodHead, manifestURL, token)
		if err != nil {
			return "", err
		}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		digest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
		resp.Body.Close()
		if digest != "" {
			if !validDigest(digest) {
				return "", fmt.Errorf("registry returned invalid Docker-Content-Digest %q", digest)
			}
			return digest, nil
		}
	} else {
		resp.Body.Close()
	}

	resp, err = manifestRequest(ctx, client, http.MethodGet, manifestURL, token)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		token, err = bearerToken(ctx, client, challenge)
		if err != nil {
			return "", err
		}
		resp, err = manifestRequest(ctx, client, http.MethodGet, manifestURL, token)
		if err != nil {
			return "", err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := resp.Status
		resp.Body.Close()
		return "", fmt.Errorf("registry returned %s", status)
	}
	digest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
	resp.Body.Close()
	if digest == "" {
		return "", fmt.Errorf("registry response omitted Docker-Content-Digest")
	}
	if !validDigest(digest) {
		return "", fmt.Errorf("registry returned invalid Docker-Content-Digest %q", digest)
	}
	return digest, nil
}

func manifestRequest(ctx context.Context, client *http.Client, method, manifestURL, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, manifestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

func parseImage(image string) (registry, repo, ref string, err error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return "", "", "", fmt.Errorf("image reference is empty")
	}
	if at := strings.LastIndex(image, "@"); at >= 0 {
		registryRepo := image[:at]
		ref = image[at+1:]
		image = registryRepo
		if !strings.HasPrefix(ref, "sha256:") {
			return "", "", "", fmt.Errorf("unsupported image digest")
		}
	}
	customRegistry := ""
	if strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		schemeEnd := strings.Index(image, "://")
		rest := image[schemeEnd+3:]
		slash := strings.IndexByte(rest, '/')
		if slash < 1 {
			return "", "", "", fmt.Errorf("invalid image reference")
		}
		customRegistry, image = image[:schemeEnd+3+slash], rest[slash+1:]
	}
	parts := strings.Split(image, "/")
	registry = "docker.io"
	if customRegistry != "" {
		registry = customRegistry
	}
	if customRegistry == "" && len(parts) > 1 {
		registry = parts[0]
		parts = parts[1:]
	}
	repo = strings.Join(parts, "/")
	if repo == "" {
		return "", "", "", fmt.Errorf("invalid image reference")
	}
	if ref == "" {
		ref = "latest"
		if colon := strings.LastIndex(repo, ":"); colon >= 0 {
			ref, repo = repo[colon+1:], repo[:colon]
		}
	}
	if strings.Contains(ref, "/") {
		return "", "", "", fmt.Errorf("invalid image reference")
	}
	return registry, repo, ref, nil
}

func bearerToken(ctx context.Context, client *http.Client, challenge string) (string, error) {
	fields := strings.Fields(challenge)
	if len(fields) < 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", fmt.Errorf("registry authentication challenge unavailable")
	}
	params := map[string]string{}
	for _, part := range strings.Split(strings.Join(fields[1:], " "), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			params[k] = strings.Trim(v, "\"")
		}
	}
	if params["realm"] == "" {
		return "", fmt.Errorf("registry token realm unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params["realm"], nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	if params["scope"] != "" {
		q.Set("scope", params["scope"])
	}
	req.URL.RawQuery = q.Encode()
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token returned %s", resp.Status)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.Token == "" {
		body.Token = body.AccessToken
	}
	if body.Token == "" {
		return "", fmt.Errorf("registry token missing")
	}
	return body.Token, nil
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
