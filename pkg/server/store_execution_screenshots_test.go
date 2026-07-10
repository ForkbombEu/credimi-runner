package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestStoreExecutionScreenshots_MultipartResponseAndCleanup(t *testing.T) {
	baseURL := "http://example.local"
	storeURL := baseURL + "/api/pipeline/store-step-screenshots"
	store := &memoryFileStore{}
	first := "results/child-1/checkout.png"
	second := "results/child-2/CONFIRM.PNG"
	sibling := "results/child-1/video.mp4"
	writeMemoryFile(t, store, first, "one")
	writeMemoryFile(t, store, second, "two")
	writeMemoryFile(t, store, sibling, "video")

	client := &fakeHTTPClient{handlers: map[string]func(*http.Request) (*http.Response, error){
		http.MethodPost + " " + storeURL: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "user-key", req.Header.Get(internalAdminKeyHeader))
			require.NoError(t, req.ParseMultipartForm(1<<20))
			require.Equal(t, []string{"run-1"}, req.MultipartForm.Value["run_identifier"])
			require.Equal(t, []string{"runner-1"}, req.MultipartForm.Value["runner_identifier"])
			require.Equal(t, []string{"scan-credential"}, req.MultipartForm.Value["step_id"])
			files := req.MultipartForm.File["screenshots"]
			require.Len(t, files, 2)
			require.Equal(t, "checkout.png", files[0].Filename)
			require.Equal(t, "CONFIRM.PNG", files[1].Filename)
			for index, expected := range []string{"one", "two"} {
				file, err := files[index].Open()
				require.NoError(t, err)
				data, err := io.ReadAll(file)
				require.NoError(t, err)
				require.NoError(t, file.Close())
				require.Equal(t, expected, string(data))
			}
			return newResponse(http.StatusOK, `{"status":"success","step_id":"scan-credential","screenshot_file_names":["one.png","two.png"],"screenshot_urls":["https://example.test/one.png","https://example.test/two.png"]}`), nil
		},
	}}
	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{
		URL:              baseURL,
		UserAPIKey:       "user-key",
		InternalAdminKey: "internal-key",
	}, Deps{HTTPClient: client, FileStore: store, ManagedWorkflowRoot: "results"})

	result, apiErr := server.storeExecutionScreenshotsLogic(storeExecutionScreenshotsPayload{
		RunIdentifier:    "run-1",
		RunnerIdentifier: "runner-1",
		StepID:           "scan-credential",
		ScreenshotPaths:  []string{first, second, first},
	})

	require.Nil(t, apiErr)
	require.JSONEq(t, `{"status":"success","step_id":"scan-credential","screenshot_file_names":["one.png","two.png"],"screenshot_urls":["https://example.test/one.png","https://example.test/two.png"]}`, string(result))
	_, err := store.Stat(first)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = store.Stat(second)
	require.Error(t, err)
	_, err = store.Stat(sibling)
	require.NoError(t, err)
	_, err = store.Stat(filepath.Dir(first))
	require.NoError(t, err)
	_, err = store.Stat(filepath.Dir(second))
	require.Error(t, err)
}

func TestStoreExecutionScreenshots_InternalKeyFallback(t *testing.T) {
	store := &memoryFileStore{}
	path := "results/child/screen.png"
	writeMemoryFile(t, store, path, "png")
	client := &fakeHTTPClient{handlers: map[string]func(*http.Request) (*http.Response, error){
		"POST http://example.local/api/pipeline/store-step-screenshots": func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "internal-key", req.Header.Get(internalAdminKeyHeader))
			return newResponse(http.StatusOK, `{"screenshot_urls":["https://example.test/screen.png"]}`), nil
		},
	}}
	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: "http://example.local", InternalAdminKey: "internal-key"}, Deps{
		HTTPClient: client, FileStore: store, ManagedWorkflowRoot: "results",
	})
	_, apiErr := server.storeExecutionScreenshotsLogic(validExecutionScreenshotPayload(path))
	require.Nil(t, apiErr)
}

func TestStoreExecutionScreenshots_RejectsInvalidRequests(t *testing.T) {
	store := &memoryFileStore{}
	valid := "results/child/screen.png"
	writeMemoryFile(t, store, valid, "png")
	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{}, Deps{FileStore: store, ManagedWorkflowRoot: "results"})

	tests := map[string]storeExecutionScreenshotsPayload{
		"run identifier":    {RunnerIdentifier: "runner", StepID: "step", ScreenshotPaths: []string{valid}},
		"runner identifier": {RunIdentifier: "run", StepID: "step", ScreenshotPaths: []string{valid}},
		"step id":           {RunIdentifier: "run", RunnerIdentifier: "runner", ScreenshotPaths: []string{valid}},
		"empty paths":       {RunIdentifier: "run", RunnerIdentifier: "runner", StepID: "step"},
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			_, apiErr := server.storeExecutionScreenshotsLogic(payload)
			require.NotNil(t, apiErr)
			require.Equal(t, http.StatusBadRequest, apiErr.Code)
		})
	}
}

func TestStoreExecutionScreenshots_PreservesFilesOnFailure(t *testing.T) {
	tests := map[string]struct {
		response *http.Response
		err      error
	}{
		"upstream error": {response: newResponse(http.StatusBadRequest, `{"status":400,"error":"upstream","reason":"bad","message":"nope"}`)},
		"network error":  {err: errors.New("network failed")},
		"malformed json": {response: newResponse(http.StatusOK, `{`)},
		"missing urls":   {response: newResponse(http.StatusOK, `{"status":"success"}`)},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := &memoryFileStore{}
			path := "results/child/screen.png"
			writeMemoryFile(t, store, path, "png")
			client := &fakeHTTPClient{handlers: map[string]func(*http.Request) (*http.Response, error){
				"POST http://example.local/api/pipeline/store-step-screenshots": func(req *http.Request) (*http.Response, error) {
					return test.response, test.err
				},
			}}
			server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: "http://example.local", UserAPIKey: "key"}, Deps{
				HTTPClient: client, FileStore: store, ManagedWorkflowRoot: "results",
			})
			result, apiErr := server.storeExecutionScreenshotsLogic(validExecutionScreenshotPayload(path))
			require.Nil(t, result)
			require.NotNil(t, apiErr)
			_, err := store.Stat(path)
			require.NoError(t, err)
		})
	}
}

func validExecutionScreenshotPayload(path string) storeExecutionScreenshotsPayload {
	return storeExecutionScreenshotsPayload{RunIdentifier: "run", RunnerIdentifier: "runner", StepID: "step", ScreenshotPaths: []string{path}}
}

func writeMemoryFile(t *testing.T, store *memoryFileStore, path, content string) {
	t.Helper()
	writer, err := store.Create(path)
	require.NoError(t, err)
	_, err = writer.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
}
