package maintenance

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestAssetNameMatchesInstallerPlatforms(t *testing.T) {
	tests := map[string]string{"linux:amd64": "credimi-runner-Linux-x86_64", "linux:arm64": "credimi-runner-Linux-aarch64", "darwin:amd64": "credimi-runner-Darwin-x86_64", "darwin:arm64": "credimi-runner-Darwin-arm64"}
	for platform, want := range tests {
		parts := strings.Split(platform, ":")
		got, err := AssetName(parts[0], parts[1])
		if err != nil || got != want {
			t.Fatalf("AssetName(%s) = %q, %v", platform, got, err)
		}
	}
	if _, err := AssetName("windows", "amd64"); err == nil {
		t.Fatal("expected unsupported OS error")
	}
}

func TestCheckerComparesRunnerReleaseMetadata(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"tag_name":"v2.0.0","published_at":"2026-07-01T12:00:00Z"}`))}, nil
	})}
	status := (Checker{HTTPClient: client}).Check(context.Background(), "v1.0.0", time.Time{})
	if !status.Runner.UpdateAvailable {
		t.Fatalf("status = %#v", status)
	}
	if status.Runner.LatestVersion != "v2.0.0" {
		t.Fatalf("status = %#v", status)
	}
}

func TestRemoteDigestAndBearerAuthentication(t *testing.T) {
	var manifestCalls, tokenCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/token") {
			tokenCalls++
			_, _ = io.WriteString(w, `{"token":"abc"}`)
			return
		}
		manifestCalls++
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+"http://"+r.Host+`/token",service="ghcr.io",scope="repository:x:y:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:new")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	digest, err := remoteDigest(context.Background(), srv.Client(), srv.URL+"/x/y:latest")
	if err != nil || digest != "sha256:new" || manifestCalls != 2 || tokenCalls != 1 {
		t.Fatalf("digest=%q err=%v calls=%d/%d", digest, err, manifestCalls, tokenCalls)
	}
	if pinned, err := remoteDigest(context.Background(), srv.Client(), srv.URL+"/x/y@sha256:pinned"); err != nil || pinned != "sha256:pinned" {
		t.Fatalf("pinned=%q err=%v", pinned, err)
	}
}

func TestRemoteDigestFallsBackFromHeadToGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:get")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	got, err := remoteDigest(context.Background(), server.Client(), server.URL+"/repo/image:latest")
	if err != nil || got != "sha256:get" {
		t.Fatalf("digest=%q err=%v", got, err)
	}
}

func TestCheckerImageStateAndFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service-image-state.json"), []byte(`{"image":"x","digest":"sha256:same"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:same")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	checker := Checker{ConfigDir: dir, HTTPClient: srv.Client()}
	status := checker.Check(context.Background(), "v1", time.Time{})
	if status.Image.UpdateAvailable || status.Image.CurrentVersion == "" {
		t.Fatalf("status=%+v", status)
	}
	if got := (Checker{ConfigDir: t.TempDir()}).Check(context.Background(), "v1", time.Time{}); got.Image.LatestVersion != "" || got.Error == "" {
		t.Fatalf("missing state=%+v", got)
	}
}

func TestParseImageRejectsMalformedReferences(t *testing.T) {
	for _, image := range []string{"", "x@yesterday", "http://host"} {
		if _, _, _, err := parseImage(image); err == nil {
			t.Fatalf("accepted %q", image)
		}
	}
}

func TestImageStateReportsDigestChangeAndRegistryErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service-image-state.json"), []byte(`{"image":"http://registry/repo:latest","digest":"sha256:old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:new")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := os.WriteFile(filepath.Join(dir, "service-image-state.json"), []byte(`{"image":"`+server.URL+`/repo:latest","digest":"sha256:old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status := (Checker{ConfigDir: dir, HTTPClient: server.Client()}).Check(context.Background(), "v1", time.Time{})
	if !status.Image.UpdateAvailable || status.Image.LatestVersion != "sha256:new" {
		t.Fatalf("status=%+v", status)
	}
	for _, challenge := range []string{"", "Basic abc", `Bearer service="x"`} {
		if _, err := bearerToken(context.Background(), server.Client(), challenge); err == nil {
			t.Fatalf("accepted challenge %q", challenge)
		}
	}
}

func TestDownloadLatestBinaryAtomicallyReplacesTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "credimi-runner")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if !strings.Contains(request.URL.Path, "credimi-runner-") {
			t.Fatalf("download URL = %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("new-binary"))}, nil
	})}
	var progress []string
	if err := DownloadLatestBinary(context.Background(), client, target, func(line string) { progress = append(progress, line) }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new-binary" {
		t.Fatalf("target = %q, %v", data, err)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if len(progress) < 2 {
		t.Fatalf("progress = %v", progress)
	}
}

func TestCheckerAllowsConfigurationWithoutRunnerImage(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1","published_at":"2026-01-01T00:00:00Z"}`))}, nil
	})}
	status := (Checker{HTTPClient: client}).Check(context.Background(), "v1", time.Time{})
	if status.Runner.UpdateAvailable || !strings.Contains(status.Error, "image:") {
		t.Fatalf("status = %#v", status)
	}
}

func TestCheckerReportsReleaseErrors(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 unavailable", Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	status := (Checker{HTTPClient: client}).Check(context.Background(), "v1", time.Time{})
	if !strings.Contains(status.Error, "GitHub returned 503") {
		t.Fatalf("status error = %q", status.Error)
	}
}

func TestDownloadLatestBinaryRejectsFailedResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if err := DownloadLatestBinary(context.Background(), client, filepath.Join(t.TempDir(), "runner"), nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v", err)
	}
}

func TestDownloadLatestBinaryReportsTransportAndInstallErrors(t *testing.T) {
	transportFailure := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("network failed") })}
	if err := DownloadLatestBinary(context.Background(), transportFailure, filepath.Join(t.TempDir(), "runner"), nil); err == nil || !strings.Contains(err.Error(), "network failed") {
		t.Fatalf("transport error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("binary"))}, nil
	})}
	targetDirectory := filepath.Join(t.TempDir(), "runner")
	if err := os.Mkdir(targetDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := DownloadLatestBinary(context.Background(), client, targetDirectory, nil); err == nil || !strings.Contains(err.Error(), "replace") {
		t.Fatalf("replace error = %v", err)
	}
}

func TestCheckerRejectsMalformedReleaseMetadata(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("not-json"))}, nil
	})}
	status := (Checker{HTTPClient: client}).Check(context.Background(), "v1", time.Time{})
	if !strings.Contains(status.Error, "invalid character") {
		t.Fatalf("status error = %q", status.Error)
	}
}
