package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
	cluelog "goa.design/clue/log"
)

type fakeHTTPClient struct {
	handlers map[string]func(*http.Request) (*http.Response, error)
	calls    map[string]int
}

func (f *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.String()
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[key]++
	if handler, ok := f.handlers[key]; ok {
		return handler(req)
	}
	return nil, fmt.Errorf("unexpected request: %s", key)
}

func (f *fakeHTTPClient) callCount(method, url string) int {
	if f.calls == nil {
		return 0
	}
	return f.calls[method+" "+url]
}

type memoryFileStore struct {
	files map[string]*bytes.Buffer
	dirs  map[string]struct{}
}

func (m *memoryFileStore) MkdirAll(path string, perm os.FileMode) error {
	if m.dirs == nil {
		m.dirs = make(map[string]struct{})
	}

	cleanPath := filepath.Clean(path)
	if cleanPath == "." {
		return nil
	}

	current := ""
	for _, part := range strings.Split(cleanPath, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current = filepath.Join(current, part)
		}
		m.dirs[current] = struct{}{}
	}

	return nil
}

func (m *memoryFileStore) Stat(name string) (os.FileInfo, error) {
	cleanName := filepath.Clean(name)
	if m.files == nil {
		if _, ok := m.dirs[cleanName]; ok {
			return fakeFileInfo{name: filepath.Base(cleanName), isDir: true}, nil
		}
		return nil, os.ErrNotExist
	}
	buf, ok := m.files[cleanName]
	if !ok {
		if _, ok := m.dirs[cleanName]; ok {
			return fakeFileInfo{name: filepath.Base(cleanName), isDir: true}, nil
		}
		return nil, os.ErrNotExist
	}
	return fakeFileInfo{name: filepath.Base(name), size: int64(buf.Len())}, nil
}

func (m *memoryFileStore) Create(name string) (io.WriteCloser, error) {
	if m.files == nil {
		m.files = make(map[string]*bytes.Buffer)
	}
	if err := m.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return nil, err
	}
	buf := &bytes.Buffer{}
	m.files[filepath.Clean(name)] = buf
	return nopWriteCloser{buf}, nil
}

func (m *memoryFileStore) Open(name string) (io.ReadCloser, error) {
	if m.files == nil {
		return nil, os.ErrNotExist
	}
	buf, ok := m.files[filepath.Clean(name)]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

func (m *memoryFileStore) RemoveAll(path string) error {
	cleanPath := filepath.Clean(path)
	if m.files == nil {
		m.files = make(map[string]*bytes.Buffer)
	}
	if m.dirs == nil {
		m.dirs = make(map[string]struct{})
	}
	for name := range m.files {
		if name == cleanPath || strings.HasPrefix(name, cleanPath+string(os.PathSeparator)) {
			delete(m.files, name)
		}
	}
	for name := range m.dirs {
		if name == cleanPath || strings.HasPrefix(name, cleanPath+string(os.PathSeparator)) {
			delete(m.dirs, name)
		}
	}
	return nil
}

type fakeFileInfo struct {
	name  string
	size  int64
	isDir bool
}

func (f fakeFileInfo) Name() string { return f.name }
func (f fakeFileInfo) Size() int64  { return f.size }
func (f fakeFileInfo) Mode() os.FileMode {
	if f.isDir {
		return os.ModeDir | 0755
	}
	return 0644
}
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.isDir }
func (f fakeFileInfo) Sys() any           { return nil }

type nopWriteCloser struct {
	*bytes.Buffer
}

func (n nopWriteCloser) Close() error { return nil }

func newResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func newBytesResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}
}

func createZipArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range files {
		entry, err := writer.Create(name)
		require.NoError(t, err)
		_, err = entry.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	return buf.Bytes()
}

func TestFetchInstallerAndAction_InvalidJSON(t *testing.T) {
	srv := NewRunnerService(NewProcessStore(), nil)
	ctx := cluelog.Context(context.Background(), cluelog.WithFormat(cluelog.FormatJSON))
	handler := NewHTTPHandler(ctx, srv, false)
	req := httptest.NewRequest(http.MethodPost, "/credimi/installer-action", strings.NewReader("{"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var apiErr runner.APIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	require.Equal(t, runner.APIError{
		Name:    "bad_request",
		Code:    http.StatusBadRequest,
		Domain:  "server",
		Reason:  "invalid JSON",
		Message: "invalid request body",
	}, apiErr)
}

func TestFetchInstallerAndAction_InvalidInstanceURL(t *testing.T) {
	instances := map[string]utils.Instance{
		"test": {URL: "http://example.local"},
	}
	deps := Deps{
		HTTPClient:    &fakeHTTPClient{},
		TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		FileStore:     &memoryFileStore{},
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), instances, deps)
	payload := fetchInstallerAndActionPayload{
		InstanceURL:       "http://missing.local",
		VersionIdentifier: "v1",
		Platform:          "android",
	}

	result, err := server.fetchInstallerAndActionLogic(payload)

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "server",
		Reason:  "invalid instance url",
		Message: "no instance found for URL: http://missing.local",
	}, err)
}

func TestFetchInstallerAndAction_ValidateFailure(t *testing.T) {
	t.Setenv("CREDIMI_INTERNAL_ADMIN_KEY", "internal-admin-key")

	baseURL := "http://example.local"
	validateURL := baseURL + "/api/canonify/identifier/validate"
	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + validateURL: func(req *http.Request) (*http.Response, error) {
				require.Equal(t, "internal-admin-key", req.Header.Get(internalAdminKeyHeader))
				return newResponse(http.StatusBadRequest, `{"status":400,"error":"CredimiAPI","reason":"BadIdentifier","message":"invalid"}`), nil
			},
		},
	}
	deps := Deps{
		HTTPClient:    client,
		TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		FileStore:     &memoryFileStore{},
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL}}, deps)
	payload := fetchInstallerAndActionPayload{
		InstanceURL:       baseURL,
		VersionIdentifier: "v1",
		ActionIdentifier:  "wallet/action",
		Platform:          "android",
	}

	result, err := server.fetchInstallerAndActionLogic(payload)

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "CredimiAPI",
		Reason:  "BadIdentifier",
		Message: "invalid",
	}, err)
}

func TestFetchInstallerAndAction_GetInstallerFailure(t *testing.T) {
	baseURL := "http://example.local"
	getInstallerURL := baseURL + "/api/wallet/get-installer-md5-or-etag"
	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + getInstallerURL: func(req *http.Request) (*http.Response, error) {
				return newResponse(http.StatusBadRequest, `{"status":400,"error":"CredimiAPI","reason":"get-installer failed","message":"nope"}`), nil
			},
		},
	}
	deps := Deps{
		HTTPClient:    client,
		TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		FileStore:     &memoryFileStore{},
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL}}, deps)
	payload := fetchInstallerAndActionPayload{
		InstanceURL:       baseURL,
		VersionIdentifier: "v1",
		Platform:          "android",
	}

	result, err := server.fetchInstallerAndActionLogic(payload)

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "CredimiAPI",
		Reason:  "get-installer failed",
		Message: "nope",
	}, err)
}

func TestFetchInstallerAndAction_DownloadMissingAndCached(t *testing.T) {
	t.Setenv("CREDIMI_INTERNAL_ADMIN_KEY", "internal-admin-key")

	baseURL := "http://example.local"
	getInstallerURL := baseURL + "/api/wallet/get-installer-md5-or-etag"
	downloadURL := baseURL + "/api/files/wallet_versions/rec-1/app.apk"
	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + getInstallerURL: func(req *http.Request) (*http.Response, error) {
				require.Equal(t, "internal-admin-key", req.Header.Get(internalAdminKeyHeader))
				var body map[string]string
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.Equal(t, "android", body["platform"])
				return newResponse(http.StatusOK, `{"record_id":"rec-1","installer_name":"app.apk","installer_identifier":"apk-123","version_id":"v1"}`), nil
			},
			http.MethodGet + " " + downloadURL: func(req *http.Request) (*http.Response, error) {
				require.Equal(t, "internal-admin-key", req.Header.Get(internalAdminKeyHeader))
				return newResponse(http.StatusOK, "apk-bytes"), nil
			},
		},
	}
	store := &memoryFileStore{}
	deps := Deps{
		HTTPClient:    client,
		TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		FileStore:     store,
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL}}, deps)
	payload := fetchInstallerAndActionPayload{
		InstanceURL:       baseURL,
		VersionIdentifier: "v1",
		Platform:          "android",
	}

	result, apiErr := server.fetchInstallerAndActionLogic(payload)

	require.Nil(t, apiErr)
	require.Equal(t, "apps/apk-123.apk", result.InstallerPath)
	require.Equal(t, "v1", result.VersionID)
	require.Equal(t, 1, client.callCount(http.MethodGet, downloadURL))

	_, statErr := store.Stat("apps/apk-123.apk")
	require.NoError(t, statErr)

	client.calls = nil
	_, apiErr = server.fetchInstallerAndActionLogic(payload)
	require.Nil(t, apiErr)
	require.Equal(t, 0, client.callCount(http.MethodGet, downloadURL))
}

func TestFetchInstallerAndAction_IOSInstallerUsesIPAExtension(t *testing.T) {
	baseURL := "http://example.local"
	getInstallerURL := baseURL + "/api/wallet/get-installer-md5-or-etag"
	downloadURL := baseURL + "/api/files/wallet_versions/rec-1/app.ipa"
	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + getInstallerURL: func(req *http.Request) (*http.Response, error) {
				var body map[string]string
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.Equal(t, "ios", body["platform"])
				return newResponse(http.StatusOK, `{"record_id":"rec-1","installer_name":"app.ipa","installer_identifier":"ios-123","version_id":"v2"}`), nil
			},
			http.MethodGet + " " + downloadURL: func(req *http.Request) (*http.Response, error) {
				return newResponse(http.StatusOK, "ipa-bytes"), nil
			},
		},
	}

	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL}}, Deps{
		HTTPClient:    client,
		TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		FileStore:     &memoryFileStore{},
	})

	result, apiErr := server.fetchInstallerAndActionLogic(fetchInstallerAndActionPayload{
		InstanceURL:       baseURL,
		VersionIdentifier: "v2",
		Platform:          "ios",
	})

	require.Nil(t, apiErr)
	require.Equal(t, "apps/ios-123.ipa", result.InstallerPath)
	require.Equal(t, "v2", result.VersionID)
}

func TestFetchInstallerAndAction_IOSZipInstallerUnzipsAppAndCachesPath(t *testing.T) {
	baseURL := "http://example.local"
	getInstallerURL := baseURL + "/api/wallet/get-installer-md5-or-etag"
	downloadURL := baseURL + "/api/files/wallet_versions/rec-zip/app.zip"
	zipBytes := createZipArchive(t, map[string]string{
		"Payload/Test.app/Info.plist": "plist",
		"Payload/Test.app/Test":       "binary",
	})

	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + getInstallerURL: func(req *http.Request) (*http.Response, error) {
				var body map[string]string
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.Equal(t, "ios", body["platform"])
				return newResponse(http.StatusOK, `{"record_id":"rec-zip","installer_name":"app.zip","installer_identifier":"ios-zip-123","version_id":"v3"}`), nil
			},
			http.MethodGet + " " + downloadURL: func(req *http.Request) (*http.Response, error) {
				return newBytesResponse(http.StatusOK, zipBytes), nil
			},
		},
	}

	store := &memoryFileStore{}
	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL}}, Deps{
		HTTPClient:    client,
		TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		FileStore:     store,
	})

	result, apiErr := server.fetchInstallerAndActionLogic(fetchInstallerAndActionPayload{
		InstanceURL:       baseURL,
		VersionIdentifier: "v3",
		Platform:          "ios",
	})

	require.Nil(t, apiErr)
	require.Equal(t, "apps/ios-zip-123/Payload/Test.app", result.InstallerPath)
	require.Equal(t, "v3", result.VersionID)
	require.Equal(t, 1, client.callCount(http.MethodGet, downloadURL))

	_, err := store.Stat("apps/ios-zip-123.zip")
	require.NoError(t, err)
	_, err = store.Stat("apps/ios-zip-123/Payload/Test.app")
	require.NoError(t, err)

	client.calls = nil
	result, apiErr = server.fetchInstallerAndActionLogic(fetchInstallerAndActionPayload{
		InstanceURL:       baseURL,
		VersionIdentifier: "v3",
		Platform:          "ios",
	})

	require.Nil(t, apiErr)
	require.Equal(t, "apps/ios-zip-123/Payload/Test.app", result.InstallerPath)
	require.Equal(t, 0, client.callCount(http.MethodGet, downloadURL))
}

func TestFetchInstallerAndAction_InvalidPlatform(t *testing.T) {
	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
		"test": {URL: "http://example.local"},
	}, Deps{
		HTTPClient:    &fakeHTTPClient{},
		TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		FileStore:     &memoryFileStore{},
	})

	result, apiErr := server.fetchInstallerAndActionLogic(fetchInstallerAndActionPayload{
		InstanceURL:       "http://example.local",
		VersionIdentifier: "v1",
		Platform:          "desktop",
	})

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "server",
		Reason:  "invalid platform",
		Message: "supported values are android or ios",
	}, apiErr)
}
