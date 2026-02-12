package server

import (
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
}

func (m *memoryFileStore) MkdirAll(path string, perm os.FileMode) error {
	return nil
}

func (m *memoryFileStore) Stat(name string) (os.FileInfo, error) {
	if m.files == nil {
		return nil, os.ErrNotExist
	}
	buf, ok := m.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fakeFileInfo{name: filepath.Base(name), size: int64(buf.Len())}, nil
}

func (m *memoryFileStore) Create(name string) (io.WriteCloser, error) {
	if m.files == nil {
		m.files = make(map[string]*bytes.Buffer)
	}
	buf := &bytes.Buffer{}
	m.files[name] = buf
	return nopWriteCloser{buf}, nil
}

func (m *memoryFileStore) Open(name string) (io.ReadCloser, error) {
	if m.files == nil {
		return nil, os.ErrNotExist
	}
	buf, ok := m.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

func (m *memoryFileStore) RemoveAll(path string) error {
	if m.files == nil {
		return nil
	}
	for name := range m.files {
		if strings.HasPrefix(name, path) {
			delete(m.files, name)
		}
	}
	return nil
}

type fakeFileInfo struct {
	name string
	size int64
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() os.FileMode  { return 0644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
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

func TestFetchApkAndAction_InvalidJSON(t *testing.T) {
	srv := NewRunnerService(NewProcessStore(), nil)
	ctx := cluelog.Context(context.Background(), cluelog.WithFormat(cluelog.FormatJSON))
	handler := NewHTTPHandler(ctx, srv, false)
	req := httptest.NewRequest(http.MethodPost, "/api/credimi/apk-action", strings.NewReader("{"))
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

func TestFetchApkAndAction_InvalidInstanceURL(t *testing.T) {
	instances := map[string]utils.Instance{
		"test": {URL: "http://example.local"},
	}
	deps := Deps{
		HTTPClient:    &fakeHTTPClient{},
		TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		FileStore:     &memoryFileStore{},
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), instances, deps)
	payload := fetchApkAndActionPayload{
		InstanceURL:       "http://missing.local",
		VersionIdentifier: "v1",
	}

	result, err := server.fetchApkAndActionLogic(payload)

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "server",
		Reason:  "invalid instance url",
		Message: "no instance found for URL: http://missing.local",
	}, err)
}

func TestFetchApkAndAction_ValidateFailure(t *testing.T) {
	baseURL := "http://example.local"
	validateURL := baseURL + "/api/canonify/identifier/validate"
	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + validateURL: func(req *http.Request) (*http.Response, error) {
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
	payload := fetchApkAndActionPayload{
		InstanceURL:       baseURL,
		VersionIdentifier: "v1",
		ActionIdentifier:  "wallet/action",
	}

	result, err := server.fetchApkAndActionLogic(payload)

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "CredimiAPI",
		Reason:  "BadIdentifier",
		Message: "invalid",
	}, err)
}

func TestFetchApkAndAction_GetMD5Failure(t *testing.T) {
	baseURL := "http://example.local"
	getMD5URL := baseURL + "/api/wallet/get-apk-md5-or-etag"
	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + getMD5URL: func(req *http.Request) (*http.Response, error) {
				return newResponse(http.StatusBadRequest, `{"status":400,"error":"CredimiAPI","reason":"get-md5 failed","message":"nope"}`), nil
			},
		},
	}
	deps := Deps{
		HTTPClient:    client,
		TokenProvider: func(instance utils.Instance) (string, error) { return "token", nil },
		FileStore:     &memoryFileStore{},
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL}}, deps)
	payload := fetchApkAndActionPayload{
		InstanceURL:       baseURL,
		VersionIdentifier: "v1",
	}

	result, err := server.fetchApkAndActionLogic(payload)

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "CredimiAPI",
		Reason:  "get-md5 failed",
		Message: "nope",
	}, err)
}

func TestFetchApkAndAction_DownloadMissingAndCached(t *testing.T) {
	baseURL := "http://example.local"
	getMD5URL := baseURL + "/api/wallet/get-apk-md5-or-etag"
	downloadURL := baseURL + "/api/files/wallet_versions/rec-1/app.apk"
	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + getMD5URL: func(req *http.Request) (*http.Response, error) {
				return newResponse(http.StatusOK, `{"record_id":"rec-1","apk_name":"app.apk","apk_identifier":"apk-123","version_id":"v1"}`), nil
			},
			http.MethodGet + " " + downloadURL: func(req *http.Request) (*http.Response, error) {
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
	payload := fetchApkAndActionPayload{
		InstanceURL:       baseURL,
		VersionIdentifier: "v1",
	}

	result, apiErr := server.fetchApkAndActionLogic(payload)

	require.Nil(t, apiErr)
	require.Equal(t, "apps/apk-123.apk", result.ApkPath)
	require.Equal(t, "v1", result.VersionID)
	require.Equal(t, 1, client.callCount(http.MethodGet, downloadURL))

	_, statErr := store.Stat("apps/apk-123.apk")
	require.NoError(t, statErr)

	client.calls = nil
	_, apiErr = server.fetchApkAndActionLogic(payload)
	require.Nil(t, apiErr)
	require.Equal(t, 0, client.callCount(http.MethodGet, downloadURL))
}
