package maintenance

import (
	"context"
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
	var accepts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/token") {
			tokenCalls++
			_, _ = io.WriteString(w, `{"token":"abc"}`)
			return
		}
		manifestCalls++
		accepts = append(accepts, r.Header.Get("Accept"))
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
	if len(accepts) != 2 || accepts[0] != manifestAccept || accepts[1] != manifestAccept {
		t.Fatalf("manifest Accept headers=%v", accepts)
	}
	if pinned, err := remoteDigest(context.Background(), srv.Client(), srv.URL+"/x/y@sha256:pinned"); err != nil || pinned != "sha256:pinned" {
		t.Fatalf("pinned=%q err=%v", pinned, err)
	}
}

func TestRemoteDigestReusesHeadTokenForAuthenticatedGetFallback(t *testing.T) {
	var methods []string
	var auth []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = io.WriteString(w, `{"access_token":"abc"}`)
			return
		}
		methods = append(methods, r.Method)
		auth = append(auth, r.Header.Get("Authorization"))
		if r.Method == http.MethodHead && r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Header.Get("Authorization") != "Bearer abc" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:reused")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	digest, err := remoteDigest(context.Background(), srv.Client(), srv.URL+"/repo/image:latest")
	if err != nil || digest != "sha256:reused" {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	if strings.Join(methods, ",") != "HEAD,HEAD,GET" || auth[2] != "Bearer abc" {
		t.Fatalf("methods=%v auth=%v", methods, auth)
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

func TestRemoteDigestRejectsInvalidGetFallbackResponses(t *testing.T) {
	tests := []struct {
		name  string
		serve func(http.ResponseWriter)
		want  string
	}{
		{"non-2xx", func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) }, "registry returned"},
		{"missing digest", func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }, "omitted Docker-Content-Digest"},
		{"invalid challenge", func(w http.ResponseWriter) {
			w.Header().Set("WWW-Authenticate", "Basic realm=\"nope\"")
			w.WriteHeader(http.StatusUnauthorized)
		}, "authentication challenge"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				tc.serve(w)
			}))
			defer srv.Close()
			if _, err := remoteDigest(context.Background(), srv.Client(), srv.URL+"/repo/image:latest"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRemoteDigestAuthenticatesGetFallbackIndependently(t *testing.T) {
	var methods []string
	var auth []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = io.WriteString(w, `{"token":"abc"}`)
			return
		}
		methods = append(methods, r.Method)
		auth = append(auth, r.Header.Get("Authorization"))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="ghcr.io",scope="repository:x:y:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:authenticated-get")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	digest, err := remoteDigest(context.Background(), srv.Client(), srv.URL+"/x/y:latest")
	if err != nil || digest != "sha256:authenticated-get" {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	if strings.Join(methods, ",") != "HEAD,GET,GET" {
		t.Fatalf("methods=%v", methods)
	}
	if auth[0] != "" || auth[1] != "" || auth[2] != "Bearer abc" {
		t.Fatalf("authorization=%v", auth)
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

func TestCheckerReportsLocalImageAsUnavailableForRegistryTracking(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service-image-state.json"), []byte(`{"image":"credimi-runner:local","image_id":"sha256:local","digest":"","registry_trackable":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var component Component
	if err := (Checker{ConfigDir: dir}).checkImage(context.Background(), &component); err != nil {
		t.Fatal(err)
	}
	if component.CurrentVersion != "local" || component.LatestVersion != "local" || component.UpdateAvailable {
		t.Fatalf("component=%+v", component)
	}
}

func TestCheckerRejectsIncompleteAppliedImageState(t *testing.T) {
	for _, raw := range []string{`{"image":"","digest":"sha256:old"}`, `{"image":"repo:latest","digest":""}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "service-image-state.json"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		var component Component
		if err := (Checker{ConfigDir: dir}).checkImage(context.Background(), &component); err == nil {
			t.Fatalf("accepted incomplete state %s", raw)
		}
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

func TestCheckerRejectsMalformedReleaseMetadata(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("not-json"))}, nil
	})}
	status := (Checker{HTTPClient: client}).Check(context.Background(), "v1", time.Time{})
	if !strings.Contains(status.Error, "invalid character") {
		t.Fatalf("status error = %q", status.Error)
	}
}
