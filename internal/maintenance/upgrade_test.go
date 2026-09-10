package maintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type upgradeFixtureOptions struct {
	includeBinary   bool
	includeChecksum bool
	duplicateBinary bool
	tag             string
	checksum        string
	apiStatus       int
	binaryStatus    int
	checksumStatus  int
}

type upgradeFixture struct {
	client *http.Client
	server *httptest.Server
	calls  []string
	asset  string
}

func newUpgradeFixture(t *testing.T, binary []byte, options upgradeFixtureOptions) *upgradeFixture {
	t.Helper()
	asset, err := CurrentAssetName()
	if err != nil {
		t.Fatal(err)
	}
	if options.tag == "" {
		options.tag = "v9.9.9"
	}
	if options.apiStatus == 0 {
		options.apiStatus = http.StatusOK
	}
	if options.binaryStatus == 0 {
		options.binaryStatus = http.StatusOK
	}
	if options.checksumStatus == 0 {
		options.checksumStatus = http.StatusOK
	}
	if options.checksum == "" {
		digest := sha256.Sum256(binary)
		options.checksum = hex.EncodeToString(digest[:]) + "  " + asset + "\n"
	}
	fixture := &upgradeFixture{asset: asset}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls = append(fixture.calls, r.URL.Path)
		status := http.StatusNotFound
		var body []byte
		switch r.URL.Path {
		case "/latest":
			status = options.apiStatus
			assets := make([]githubReleaseAsset, 0, 3)
			if options.includeBinary {
				binaryURL := fixture.server.URL + "/releases/" + options.tag + "/" + asset
				assets = append(assets, githubReleaseAsset{Name: asset, BrowserDownloadURL: binaryURL})
				if options.duplicateBinary {
					assets = append(assets, githubReleaseAsset{Name: asset, BrowserDownloadURL: binaryURL})
				}
			}
			if options.includeChecksum {
				checksumName := "credimi-runner_" + options.tag + "_checksums.txt"
				assets = append(assets, githubReleaseAsset{Name: checksumName, BrowserDownloadURL: fixture.server.URL + "/releases/" + options.tag + "/" + checksumName})
			}
			body, _ = json.Marshal(githubRelease{TagName: options.tag, Assets: assets})
		case "/releases/" + options.tag + "/" + asset:
			status = options.binaryStatus
			body = binary
		default:
			checksumName := "credimi-runner_" + options.tag + "_checksums.txt"
			if r.URL.Path == "/releases/"+options.tag+"/"+checksumName {
				status = options.checksumStatus
				body = []byte(options.checksum)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	client := fixture.server.Client()
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == latestReleaseURL {
			fixture.calls = append(fixture.calls, "/latest")
			assets := make([]githubReleaseAsset, 0, 3)
			if options.includeBinary {
				binaryURL := fixture.server.URL + "/releases/" + options.tag + "/" + asset
				assets = append(assets, githubReleaseAsset{Name: asset, BrowserDownloadURL: binaryURL})
				if options.duplicateBinary {
					assets = append(assets, githubReleaseAsset{Name: asset, BrowserDownloadURL: binaryURL})
				}
			}
			if options.includeChecksum {
				checksumName := "credimi-runner_" + options.tag + "_checksums.txt"
				assets = append(assets, githubReleaseAsset{Name: checksumName, BrowserDownloadURL: fixture.server.URL + "/releases/" + options.tag + "/" + checksumName})
			}
			body, _ := json.Marshal(githubRelease{TagName: options.tag, Assets: assets})
			return &http.Response{StatusCode: options.apiStatus, Status: fmt.Sprintf("%d test", options.apiStatus), Body: io.NopCloser(bytes.NewReader(body))}, nil
		}
		return http.DefaultTransport.RoundTrip(request)
	})
	fixture.client = client
	t.Cleanup(fixture.server.Close)
	return fixture
}

func assertNoUpgradeTemps(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".credimi-runner-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary upgrade files remain: %v", matches)
	}
}

func TestDownloadLatestBinaryVerifiedUpgrade(t *testing.T) {
	binary := []byte("new-binary")
	fixture := newUpgradeFixture(t, binary, upgradeFixtureOptions{includeBinary: true, includeChecksum: true})
	target := filepath.Join(t.TempDir(), "credimi-runner")
	if err := os.WriteFile(target, []byte("old-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	var progress []string
	if err := DownloadLatestBinary(context.Background(), fixture.client, target, func(message string) { progress = append(progress, message) }); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(binary) {
		t.Fatalf("target=%q err=%v", got, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("target mode=%v err=%v", info.Mode().Perm(), err)
	}
	checksumName := "credimi-runner_v9.9.9_checksums.txt"
	wantRequests := "/latest,/releases/v9.9.9/" + fixture.asset + ",/releases/v9.9.9/" + checksumName
	if strings.Join(fixture.calls, ",") != wantRequests {
		t.Fatalf("requests=%v", fixture.calls)
	}
	if len(progress) != 4 || !strings.Contains(progress[0], "Resolving") || !strings.Contains(progress[3], "Installed") {
		t.Fatalf("progress=%v", progress)
	}
}

func TestDownloadLatestBinaryRejectsIntegrityFailuresWithoutReplacement(t *testing.T) {
	asset, err := CurrentAssetName()
	if err != nil {
		t.Fatal(err)
	}
	goodHash := sha256.Sum256([]byte("new-binary"))
	goodDigest := hex.EncodeToString(goodHash[:])
	tests := []struct {
		name    string
		options upgradeFixtureOptions
		want    string
	}{
		{"checksum mismatch", upgradeFixtureOptions{includeBinary: true, includeChecksum: true, checksum: strings.Repeat("0", 64) + "  " + asset + "\n"}, "checksum mismatch"},
		{"missing checksum entry", upgradeFixtureOptions{includeBinary: true, includeChecksum: true, checksum: goodDigest + "  other\n"}, "does not contain an entry"},
		{"duplicate checksum entry", upgradeFixtureOptions{includeBinary: true, includeChecksum: true, checksum: goodDigest + "  " + asset + "\n" + goodDigest + "  " + asset + "\n"}, "exactly one"},
		{"duplicate binary asset", upgradeFixtureOptions{includeBinary: true, includeChecksum: true, duplicateBinary: true}, "contains 2 runner binary assets"},
		{"short SHA", upgradeFixtureOptions{includeBinary: true, includeChecksum: true, checksum: "abc  " + asset + "\n"}, "malformed SHA256"},
		{"invalid SHA", upgradeFixtureOptions{includeBinary: true, includeChecksum: true, checksum: strings.Repeat("z", 64) + "  " + asset + "\n"}, "malformed SHA256"},
		{"missing checksum asset", upgradeFixtureOptions{includeBinary: true}, "missing runner checksum asset"},
		{"missing binary asset", upgradeFixtureOptions{includeChecksum: true}, "missing runner binary asset"},
		{"metadata failure", upgradeFixtureOptions{apiStatus: http.StatusBadGateway}, "502"},
		{"binary failure", upgradeFixtureOptions{includeBinary: true, includeChecksum: true, binaryStatus: http.StatusBadGateway}, "download runner binary"},
		{"checksum failure", upgradeFixtureOptions{includeBinary: true, includeChecksum: true, checksumStatus: http.StatusBadGateway}, "download runner checksum"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old := []byte("old-binary")
			fixture := newUpgradeFixture(t, []byte("new-binary"), tc.options)
			dir := t.TempDir()
			target := filepath.Join(dir, "credimi-runner")
			if err := os.WriteFile(target, old, 0o755); err != nil {
				t.Fatal(err)
			}
			err := DownloadLatestBinary(context.Background(), fixture.client, target, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v; want %q", err, tc.want)
			}
			got, readErr := os.ReadFile(target)
			if readErr != nil || string(got) != string(old) {
				t.Fatalf("target changed to %q err=%v", got, readErr)
			}
			assertNoUpgradeTemps(t, dir)
		})
	}
}

func TestDownloadLatestBinaryUsesOneReleaseForBothAssets(t *testing.T) {
	binary := []byte("release-binary")
	fixture := newUpgradeFixture(t, binary, upgradeFixtureOptions{includeBinary: true, includeChecksum: true, tag: "v9.9.9"})
	target := filepath.Join(t.TempDir(), "runner")
	if err := DownloadLatestBinary(context.Background(), fixture.client, target, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range fixture.calls[1:] {
		if !strings.Contains(path, "/releases/v9.9.9/") {
			t.Fatalf("asset request %q did not use resolved release", path)
		}
	}
}

func TestDownloadLatestBinaryReportsTransportFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("network failed")
	})}
	err := DownloadLatestBinary(context.Background(), client, filepath.Join(t.TempDir(), "runner"), nil)
	if err == nil || !strings.Contains(err.Error(), "network failed") {
		t.Fatalf("error=%v", err)
	}
}
