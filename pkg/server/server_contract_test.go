package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
	cluelog "goa.design/clue/log"
)

type storeCapture struct {
	mu     sync.Mutex
	fields map[string]string
	files  map[string]string
	err    error
}

func (c *storeCapture) setErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
	}
}

func (c *storeCapture) recordField(name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fields == nil {
		c.fields = make(map[string]string)
	}
	c.fields[name] = value
}

func (c *storeCapture) recordFile(name, filename string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.files == nil {
		c.files = make(map[string]string)
	}
	c.files[name] = filename
}

func (c *storeCapture) snapshot() (map[string]string, map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fields := make(map[string]string)
	for k, v := range c.fields {
		fields[k] = v
	}
	files := make(map[string]string)
	for k, v := range c.files {
		files[k] = v
	}
	return fields, files, c.err
}

func newTestInstanceServer(t *testing.T, capture *storeCapture) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/collections/_superusers/auth-with-password", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token":"test-token"}`))
	})
	mux.HandleFunc("/api/canonify/identifier/validate", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"record":{"code":"ACTION-CODE"}}`))
	})
	mux.HandleFunc("/api/wallet/get-apk-md5-or-etag", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"record_id":"rec-1","apk_name":"app.apk","apk_identifier":"apk-123","version_id":"v1"}`))
	})
	mux.HandleFunc("/api/files/wallet_versions/rec-1/app.apk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("apk-bytes"))
	})
	mux.HandleFunc("/api/wallet/store-pipeline-result", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			capture.setErr(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		for name, values := range r.MultipartForm.Value {
			if len(values) > 0 {
				capture.recordField(name, values[0])
			}
		}
		for name, files := range r.MultipartForm.File {
			if len(files) > 0 {
				capture.recordFile(name, files[0].Filename)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"stored","id":"abc"}`))
	})

	return httptest.NewServer(mux)
}

func newRunnerServiceForTest(instances map[string]utils.Instance, store *ProcessStore) http.Handler {
	if store == nil {
		store = NewProcessStore()
	}
	deps := Deps{
		Sleeper: func(time.Duration) {},
	}
	srv := NewRunnerServiceWithDeps(store, instances, deps)
	ctx := cluelog.Context(context.Background(), cluelog.WithFormat(cluelog.FormatJSON))
	return NewHTTPHandler(ctx, srv, false)
}

func TestServerContract_ProcessStart(t *testing.T) {
	t.Run("missing namespace", func(t *testing.T) {
		server := newRunnerServiceForTest(nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/worker/process/", strings.NewReader(`{"old_namespace":""}`))
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)
		require.Equal(t, http.StatusBadRequest, resp.Code)
		var apiErr runner.APIError
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &apiErr))
		require.Equal(t, runner.APIError{
			Name:    "bad_request",
			Code:    http.StatusBadRequest,
			Domain:  "Server",
			Reason:  "NamespaceMissing",
			Message: "namespace is required",
		}, apiErr)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		server := newRunnerServiceForTest(nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/worker/process/example", strings.NewReader("{"))
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)

		require.Equal(t, http.StatusBadRequest, resp.Code)
		var apiErr runner.APIError
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &apiErr))
		require.Equal(t, runner.APIError{
			Name:    "bad_request",
			Code:    http.StatusBadRequest,
			Domain:  "server",
			Reason:  "invalid JSON",
			Message: "invalid request body",
		}, apiErr)
	})

	t.Run("started", func(t *testing.T) {
		store := NewProcessStore()
		store.Add(NewProcess("alpha", nil))
		server := newRunnerServiceForTest(nil, store)
		req := httptest.NewRequest(http.MethodPost, "/api/worker/process/alpha", strings.NewReader(`{"old_namespace":""}`))
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)

		require.Equal(t, http.StatusAccepted, resp.Code)
		var body map[string]string
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
		require.Equal(t, "started", body["status"])
		require.Equal(t, "alpha", body["namespace"])
	})

	t.Run("already running", func(t *testing.T) {
		store := NewProcessStore()
		proc := NewProcess("beta", nil)
		proc.Running = true
		store.Add(proc)
		server := newRunnerServiceForTest(nil, store)
		req := httptest.NewRequest(http.MethodPost, "/api/worker/process/beta", strings.NewReader(`{"old_namespace":""}`))
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)

		require.Equal(t, http.StatusAccepted, resp.Code)
		var body map[string]string
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
		require.Equal(t, "already running", body["status"])
		require.Equal(t, "beta", body["namespace"])
	})
}

func TestServerContract_ProcessList(t *testing.T) {
	store := NewProcessStore()
	procA := NewProcess("alpha", nil)
	procA.Running = true
	procB := NewProcess("bravo", nil)
	procB.Running = false
	procC := NewProcess("charlie", nil)
	procC.Running = true
	store.Add(procA)
	store.Add(procB)
	store.Add(procC)

	server := newRunnerServiceForTest(nil, store)
	req := httptest.NewRequest(http.MethodGet, "/api/worker/processes", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	var list []string
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &list))
	require.ElementsMatch(t, []string{"alpha", "charlie"}, list)
}

func TestServerContract_Docs(t *testing.T) {
	server := newRunnerServiceForTest(nil, nil)

	t.Run("spotlight page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)

		require.Equal(t, http.StatusOK, resp.Code)
		require.Contains(t, resp.Header().Get("Content-Type"), "text/html")
		require.Contains(t, resp.Body.String(), "<elements-api")
		require.Contains(t, resp.Body.String(), `apiDescriptionUrl="/docs/openapi3-public.json"`)
	})

	t.Run("openapi public json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/docs/openapi3-public.json", nil)
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)

		require.Equal(t, http.StatusOK, resp.Code)
		require.Contains(t, resp.Body.String(), `"openapi": "3.0.3"`)
		require.NotContains(t, resp.Body.String(), `"/docs/openapi3.yaml"`)
		require.NotContains(t, resp.Body.String(), `"/docs/openapi.yaml"`)
	})
}

func TestServerContract_FetchApkAndAction(t *testing.T) {
	t.Run("invalid JSON", func(t *testing.T) {
		server := newRunnerServiceForTest(nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/credimi/apk-action", strings.NewReader("{"))
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)

		require.Equal(t, http.StatusBadRequest, resp.Code)
		var apiErr runner.APIError
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &apiErr))
		require.Equal(t, runner.APIError{
			Name:    "bad_request",
			Code:    http.StatusBadRequest,
			Domain:  "server",
			Reason:  "invalid JSON",
			Message: "invalid request body",
		}, apiErr)
	})

	t.Run("invalid instance url", func(t *testing.T) {
		server := newRunnerServiceForTest(nil, nil)
		payload := `{"instance_url":"http://missing.local","version_identifier":"v1"}`
		req := httptest.NewRequest(http.MethodPost, "/api/credimi/apk-action", strings.NewReader(payload))
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)
		require.Equal(t, http.StatusBadRequest, resp.Code)
		var apiErr runner.APIError
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &apiErr))
		require.Equal(t, runner.APIError{
			Name:    "bad_request",
			Code:    http.StatusBadRequest,
			Domain:  "server",
			Reason:  "invalid instance url",
			Message: "no instance found for URL: http://missing.local",
		}, apiErr)
	})

	t.Run("success with action", func(t *testing.T) {
		capture := &storeCapture{}
		upstream := newTestInstanceServer(t, capture)
		defer upstream.Close()

		instances := map[string]utils.Instance{
			"test": {
				URL:      upstream.URL,
				PB_ADMIN: "admin",
				PB_PASS:  "pass",
			},
		}

		tmpDir := t.TempDir()
		cwd, err := os.Getwd()
		require.NoError(t, err)
		require.NoError(t, os.Chdir(tmpDir))
		t.Cleanup(func() {
			_ = os.Chdir(cwd)
		})

		server := newRunnerServiceForTest(instances, nil)
		payload := `{"instance_url":"` + upstream.URL + `","version_identifier":"v1","action_identifier":"wallet/action"}`
		req := httptest.NewRequest(http.MethodPost, "/api/credimi/apk-action", strings.NewReader(payload))
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)

		require.Equal(t, http.StatusOK, resp.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
		require.Equal(t, "apps/apk-123.apk", body["apk_path"])
		require.Equal(t, "v1", body["version_id"])
		require.Equal(t, "ACTION-CODE", body["code"])
	})
}

func TestServerContract_StorePipelineResult(t *testing.T) {
	t.Run("missing video path", func(t *testing.T) {
		server := newRunnerServiceForTest(nil, nil)
		payload := `{"instance_url":"http://example.local","video_path":"","last_frame_path":"","logcat_path":"","run_identifier":"run-1","runner_identifier":"runner-1"}`
		req := httptest.NewRequest(http.MethodPost, "/api/credimi/pipeline-result", strings.NewReader(payload))
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)
		require.Equal(t, http.StatusBadRequest, resp.Code)
		var apiErr runner.APIError
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &apiErr))
		require.Equal(t, runner.APIError{
			Name:    "bad_request",
			Code:    http.StatusBadRequest,
			Domain:  "server",
			Reason:  "missing field",
			Message: "result_path is required",
		}, apiErr)
	})

	t.Run("success pass-through", func(t *testing.T) {
		capture := &storeCapture{}
		upstream := newTestInstanceServer(t, capture)
		defer upstream.Close()

		instances := map[string]utils.Instance{
			"test": {
				URL:      upstream.URL,
				PB_ADMIN: "admin",
				PB_PASS:  "pass",
			},
		}

		tmpDir := t.TempDir()
		videoPath := filepath.Join(tmpDir, "video.mp4")
		lastFramePath := filepath.Join(tmpDir, "last.png")
		logcatPath := filepath.Join(tmpDir, "logcat.txt")
		require.NoError(t, os.WriteFile(videoPath, []byte("video"), 0600))
		require.NoError(t, os.WriteFile(lastFramePath, []byte("frame"), 0600))
		require.NoError(t, os.WriteFile(logcatPath, []byte("log"), 0600))

		server := newRunnerServiceForTest(instances, nil)
		payload := map[string]string{
			"instance_url":      upstream.URL,
			"video_path":        videoPath,
			"last_frame_path":   lastFramePath,
			"logcat_path":       logcatPath,
			"run_identifier":    "run-1",
			"runner_identifier": "runner-1",
		}
		buf := &bytes.Buffer{}
		require.NoError(t, json.NewEncoder(buf).Encode(payload))
		req := httptest.NewRequest(http.MethodPost, "/api/credimi/pipeline-result", buf)
		resp := httptest.NewRecorder()

		server.ServeHTTP(resp, req)

		require.Equal(t, http.StatusOK, resp.Code)
		require.JSONEq(t, `{"status":"stored","id":"abc"}`, resp.Body.String())

		fields, files, err := capture.snapshot()
		require.NoError(t, err)
		require.Equal(t, "runner-1", fields["runner_identifier"])
		require.Equal(t, "run-1", fields["run_identifier"])
		require.Equal(t, "video.mp4", files["result_video"])
		require.Equal(t, "last.png", files["last_frame"])
		require.Equal(t, "logcat.txt", files["logcat"])
	})
}

func TestServerContract_TouchFingerprint(t *testing.T) {
	adbDir := t.TempDir()
	adbPath := filepath.Join(adbDir, "adb")
	adbContent := "#!/bin/sh\nexit 0\n"
	require.NoError(t, os.WriteFile(adbPath, []byte(adbContent), 0755))
	t.Setenv("PATH", adbDir)

	server := newRunnerServiceForTest(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/mobile/fingerprint/touch", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
	require.Equal(t, "fingerprint touch executed", body["status"])
}

func TestServerContract_NotFound(t *testing.T) {
	server := newRunnerServiceForTest(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	require.Equal(t, http.StatusNotFound, resp.Code)
}

func TestServerContract_FetchApkAndAction_OptionalCode(t *testing.T) {
	capture := &storeCapture{}
	upstream := newTestInstanceServer(t, capture)
	defer upstream.Close()

	instances := map[string]utils.Instance{
		"test": {
			URL:      upstream.URL,
			PB_ADMIN: "admin",
			PB_PASS:  "pass",
		},
	}

	tmpDir := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	server := newRunnerServiceForTest(instances, nil)
	payload := `{"instance_url":"` + upstream.URL + `","version_identifier":"v1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/credimi/apk-action", strings.NewReader(payload))
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	require.Equal(t, http.StatusOK, resp.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
	_, hasCode := body["code"]
	require.False(t, hasCode)
}
