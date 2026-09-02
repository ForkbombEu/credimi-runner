package maintenance

import (
	"context"
	"errors"
	"io"
	"net/http"
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
	if status.Error != "" || status.Runner.UpdateAvailable {
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
