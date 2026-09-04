package androidtools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAndroidAssetsListAndDownload(t *testing.T) {
	dir := t.TempDir()
	avdHome := filepath.Join(dir, "avd")
	goldenRoot := filepath.Join(dir, "golden")
	if err := os.MkdirAll(filepath.Join(avdHome, "credimi.avd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(avdHome, "credimi.ini"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !AVDAssetsExist(avdHome, "credimi") || len(ListAVDOptions(avdHome)) != 1 {
		t.Fatalf("AVD assets were not discovered: %v", ListAVDOptions(avdHome))
	}
	if err := os.MkdirAll(filepath.Join(goldenRoot, "credimi-golden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !GoldenAssetsExist(goldenRoot, "credimi-golden") || len(ListGoldenOptions(goldenRoot)) != 1 {
		t.Fatalf("golden assets were not discovered: %v", ListGoldenOptions(goldenRoot))
	}
	original := androidAssetHTTPClient
	t.Cleanup(func() { androidAssetHTTPClient = original })
	androidAssetHTTPClient = &http.Client{Transport: assetRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(assetArchive(t, "nested/file.txt", "ok"))), Header: make(http.Header)}, nil
	})}
	dest := filepath.Join(dir, "downloaded")
	var progress []DownloadProgress
	if err := DownloadAndExtractTarball(context.Background(), "https://example.invalid/assets.tar.gz", dest, func(update DownloadProgress) {
		progress = append(progress, update)
	}); err != nil {
		t.Fatal(err)
	}
	if len(progress) < 2 || progress[0].Phase != "downloading" || progress[len(progress)-1].Phase != "extracting" {
		t.Fatalf("asset progress = %#v", progress)
	}
	contents, err := os.ReadFile(filepath.Join(dest, "nested", "file.txt"))
	if err != nil || string(contents) != "ok" {
		t.Fatalf("downloaded asset = %q, err=%v", contents, err)
	}
}

func TestAndroidAssetsRejectIncompleteOrMissingDirectories(t *testing.T) {
	dir := t.TempDir()
	if AVDAssetsExist("", "credimi") || AVDAssetsExist(dir, "") || len(ListAVDOptions(filepath.Join(dir, "missing"))) != 0 {
		t.Fatal("invalid AVD assets were accepted")
	}
	if GoldenAssetsExist("", "golden") || GoldenAssetsExist(dir, "") || len(ListGoldenOptions(filepath.Join(dir, "missing"))) != 0 {
		t.Fatal("invalid golden assets were accepted")
	}
	avdDir := filepath.Join(dir, "partial.avd")
	if err := os.MkdirAll(avdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if AVDAssetsExist(dir, "partial") {
		t.Fatal("partial AVD was reported complete")
	}
}

type assetRoundTripFunc func(*http.Request) (*http.Response, error)

func (f assetRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func assetArchive(t *testing.T, name, contents string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestDownloadAndExtractTarballRejectsTraversal(t *testing.T) {
	archive := assetArchive(t, "../escape", "bad")
	original := androidAssetHTTPClient
	t.Cleanup(func() { androidAssetHTTPClient = original })
	androidAssetHTTPClient = &http.Client{Transport: assetRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(archive)), Header: make(http.Header)}, nil
	})}
	err := DownloadAndExtractTarball(context.Background(), "https://example.invalid/assets.tar.gz", t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "outside destination") {
		t.Fatalf("traversal error = %v", err)
	}
}

func TestDownloadAndExtractTarballReportsHTTPAndArchiveErrors(t *testing.T) {
	original := androidAssetHTTPClient
	t.Cleanup(func() { androidAssetHTTPClient = original })
	androidAssetHTTPClient = &http.Client{Transport: assetRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "status") {
			return &http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("not gzip")), Header: make(http.Header)}, nil
	})}
	if err := DownloadAndExtractTarball(context.Background(), "https://example.invalid/status", t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Fatalf("HTTP error = %v", err)
	}
	if err := DownloadAndExtractTarball(context.Background(), "https://example.invalid/corrupt", t.TempDir(), nil); err == nil {
		t.Fatal("corrupt archive was accepted")
	}
}
