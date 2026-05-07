package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
	cluelog "goa.design/clue/log"
)

type multipartCapture struct {
	fields map[string]string
	files  map[string]string
	err    error
}

type trackingFileStore struct {
	memoryFileStore
	removed []string
}

type errorReadCloser struct{}

func (errorReadCloser) Read(p []byte) (int, error) { return 0, errors.New("read failed") }
func (errorReadCloser) Close() error               { return nil }

func (t *trackingFileStore) RemoveAll(path string) error {
	t.removed = append(t.removed, path)
	return t.memoryFileStore.RemoveAll(path)
}

func TestStorePipelineResult_MissingVideoPath(t *testing.T) {
	server := NewRunnerService(nil, nil)
	payload := storePipelineResultPayload{
		InstanceURL: "http://example.local",
		VideoPath:   "",
		Platform:    "android",
	}

	result, apiErr := server.storePipelineResultLogic(payload)

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "server",
		Reason:  "missing field",
		Message: "result_path is required",
	}, apiErr)
}

func TestStorePipelineResult_MultipartAndCleanup(t *testing.T) {
	baseURL := "http://example.local"
	storeURL := baseURL + "/api/wallet/store-pipeline-result"
	capture := &multipartCapture{}

	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + storeURL: func(req *http.Request) (*http.Response, error) {
				require.Equal(t, "user-key", req.Header.Get(internalAdminKeyHeader))
				reader, err := req.MultipartReader()
				if err != nil {
					capture.err = err
					return newResponse(http.StatusBadRequest, ""), nil
				}
				capture.fields = make(map[string]string)
				capture.files = make(map[string]string)
				for {
					part, err := reader.NextPart()
					if err == io.EOF {
						break
					}
					if err != nil {
						capture.err = err
						break
					}
					if part.FileName() != "" {
						capture.files[part.FormName()] = part.FileName()
						_, _ = io.Copy(io.Discard, part)
					} else {
						data, _ := io.ReadAll(part)
						capture.fields[part.FormName()] = string(data)
					}
					_ = part.Close()
				}
				return newResponse(http.StatusOK, `{"status":"stored"}`), nil
			},
		},
	}

	store := &trackingFileStore{}
	videoPath := "results/run-1/video.mp4"
	lastFramePath := "results/run-1/last.png"
	logPath := "results/run-1/log.txt"

	writer, err := store.Create(videoPath)
	require.NoError(t, err)
	_, _ = writer.Write([]byte("video"))
	require.NoError(t, writer.Close())
	writer, err = store.Create(lastFramePath)
	require.NoError(t, err)
	_, _ = writer.Write([]byte("frame"))
	require.NoError(t, writer.Close())
	writer, err = store.Create(logPath)
	require.NoError(t, err)
	_, _ = writer.Write([]byte("log"))
	require.NoError(t, writer.Close())

	deps := Deps{
		HTTPClient: client,
		FileStore:  store,
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"}}, deps)
	payload := storePipelineResultPayload{
		InstanceURL:      baseURL,
		VideoPath:        videoPath,
		LastFramePath:    lastFramePath,
		LogPath:          logPath,
		RunIdentifier:    "run-1",
		RunnerIdentifier: "runner-1",
		Platform:         "android",
	}

	result, apiErr := server.storePipelineResultLogic(payload)

	require.Nil(t, apiErr)
	require.JSONEq(t, `{"status":"stored"}`, string(result))
	require.NoError(t, capture.err)
	require.Equal(t, "runner-1", capture.fields["runner_identifier"])
	require.Equal(t, "run-1", capture.fields["run_identifier"])
	require.Equal(t, "android", capture.fields["platform"])
	require.Equal(t, "video.mp4", capture.files["result_video"])
	require.Equal(t, "last.png", capture.files["last_frame"])
	require.Equal(t, "log.txt", capture.files["logfile"])
	require.Contains(t, store.removed, filepath.Dir(videoPath))
}

func TestStorePipelineResult_IOSMultipartIncludesLogFile(t *testing.T) {
	baseURL := "http://example.local"
	storeURL := baseURL + "/api/wallet/store-pipeline-result"
	capture := &multipartCapture{}

	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + storeURL: func(req *http.Request) (*http.Response, error) {
				reader, err := req.MultipartReader()
				if err != nil {
					capture.err = err
					return newResponse(http.StatusBadRequest, ""), nil
				}
				capture.fields = make(map[string]string)
				capture.files = make(map[string]string)
				for {
					part, err := reader.NextPart()
					if err == io.EOF {
						break
					}
					if err != nil {
						capture.err = err
						break
					}
					if part.FileName() != "" {
						capture.files[part.FormName()] = part.FileName()
						_, _ = io.Copy(io.Discard, part)
					} else {
						data, _ := io.ReadAll(part)
						capture.fields[part.FormName()] = string(data)
					}
					_ = part.Close()
				}
				return newResponse(http.StatusOK, `{"status":"stored"}`), nil
			},
		},
	}

	store := &trackingFileStore{}
	videoPath := "results/run-1/video.mp4"
	lastFramePath := "results/run-1/last.png"
	logPath := "results/run-1/log.txt"

	writer, err := store.Create(videoPath)
	require.NoError(t, err)
	_, _ = writer.Write([]byte("video"))
	require.NoError(t, writer.Close())
	writer, err = store.Create(lastFramePath)
	require.NoError(t, err)
	_, _ = writer.Write([]byte("frame"))
	require.NoError(t, writer.Close())
	writer, err = store.Create(logPath)
	require.NoError(t, err)
	_, _ = writer.Write([]byte("log"))
	require.NoError(t, writer.Close())

	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"}}, Deps{
		HTTPClient: client,
		FileStore:  store,
	})

	result, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
		InstanceURL:      baseURL,
		VideoPath:        videoPath,
		LastFramePath:    lastFramePath,
		LogPath:          logPath,
		RunIdentifier:    "run-1",
		RunnerIdentifier: "runner-1",
		Platform:         "ios",
	})

	require.Nil(t, apiErr)
	require.JSONEq(t, `{"status":"stored"}`, string(result))
	require.NoError(t, capture.err)
	require.Equal(t, "ios", capture.fields["platform"])
	require.Equal(t, "video.mp4", capture.files["result_video"])
	require.Equal(t, "last.png", capture.files["last_frame"])
	require.Equal(t, "log.txt", capture.files["logfile"])
}

func TestStorePipelineResult_UpstreamError(t *testing.T) {
	baseURL := "http://example.local"
	storeURL := baseURL + "/api/wallet/store-pipeline-result"

	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + storeURL: func(req *http.Request) (*http.Response, error) {
				return newResponse(http.StatusBadRequest, `{"status":400,"error":"upstream","reason":"bad","message":"nope"}`), nil
			},
		},
	}

	store := &memoryFileStore{}
	writer, err := store.Create("results/run-1/video.mp4")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/last.png")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/log.txt")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	deps := Deps{
		HTTPClient: client,
		FileStore:  store,
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"}}, deps)
	payload := storePipelineResultPayload{
		InstanceURL:      baseURL,
		VideoPath:        "results/run-1/video.mp4",
		LastFramePath:    "results/run-1/last.png",
		LogPath:          "results/run-1/log.txt",
		RunIdentifier:    "run-1",
		RunnerIdentifier: "runner-1",
		Platform:         "android",
	}

	result, apiErr := server.storePipelineResultLogic(payload)

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "upstream",
		Reason:  "bad",
		Message: "nope",
	}, apiErr)
}

func TestStorePipelineResult_UpstreamWrappedError(t *testing.T) {
	baseURL := "http://example.local"
	storeURL := baseURL + "/api/wallet/store-pipeline-result"

	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + storeURL: func(req *http.Request) (*http.Response, error) {
				return newResponse(
					http.StatusForbidden,
					`{"apiVersion":"2.0","error":{"code":403,"message":"record does not belong to the authenticated user's organization","errors":[{"domain":"authorization","reason":"forbidden","message":"record does not belong to the authenticated user's organization"}]}}`,
				), nil
			},
		},
	}

	store := &memoryFileStore{}
	writer, err := store.Create("results/run-1/video.mp4")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/last.png")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/log.txt")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	deps := Deps{
		HTTPClient: client,
		FileStore:  store,
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"}}, deps)

	result, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
		InstanceURL:      baseURL,
		VideoPath:        "results/run-1/video.mp4",
		LastFramePath:    "results/run-1/last.png",
		LogPath:          "results/run-1/log.txt",
		RunIdentifier:    "run-1",
		RunnerIdentifier: "runner-1",
		Platform:         "android",
	})

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusForbidden,
		Domain:  "authorization",
		Reason:  "forbidden",
		Message: "record does not belong to the authenticated user's organization",
	}, apiErr)
}

func TestStorePipelineResult_UpstreamConcatenatedErrorFallsBackGracefully(t *testing.T) {
	baseURL := "http://example.local"
	storeURL := baseURL + "/api/wallet/store-pipeline-result"

	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + storeURL: func(req *http.Request) (*http.Response, error) {
				return newResponse(
					http.StatusForbidden,
					`{"status":403,"error":"authorization","reason":"forbidden","message":"first"}{"status":403,"error":"authorization","reason":"forbidden","message":"second"}`,
				), nil
			},
		},
	}

	store := &memoryFileStore{}
	writer, err := store.Create("results/run-1/video.mp4")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/last.png")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/log.txt")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	deps := Deps{
		HTTPClient: client,
		FileStore:  store,
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{"test": {URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"}}, deps)

	result, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
		InstanceURL:      baseURL,
		VideoPath:        "results/run-1/video.mp4",
		LastFramePath:    "results/run-1/last.png",
		LogPath:          "results/run-1/log.txt",
		RunIdentifier:    "run-1",
		RunnerIdentifier: "runner-1",
		Platform:         "android",
	})

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusForbidden,
		Domain:  "authorization",
		Reason:  "forbidden",
		Message: "first",
	}, apiErr)
}

func TestStorePipelineResult_InvalidInstanceURL(t *testing.T) {
	baseURL := "http://example.local"
	store := &memoryFileStore{}
	writer, err := store.Create("results/run-1/video.mp4")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/last.png")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/log.txt")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
		"prod": {URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"},
	}, Deps{
		HTTPClient: &fakeHTTPClient{},
		FileStore:  store,
	})

	_, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
		InstanceURL:   "http://missing.local",
		VideoPath:     "results/run-1/video.mp4",
		LastFramePath: "results/run-1/last.png",
		LogPath:       "results/run-1/log.txt",
		Platform:      "android",
	})

	require.Equal(t, &runner.APIError{
		Code:    http.StatusInternalServerError,
		Domain:  "server",
		Reason:  "invalid instance url",
		Message: "no instance found for URL: http://missing.local",
	}, apiErr)
}

func TestStorePipelineResult_MissingCredentials(t *testing.T) {
	baseURL := "http://example.local"
	store := &memoryFileStore{}
	writer, err := store.Create("results/run-1/video.mp4")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/last.png")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/log.txt")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	server := NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
		"prod": {URL: baseURL},
	}, Deps{
		HTTPClient: &fakeHTTPClient{},
		FileStore:  store,
	})

	_, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
		InstanceURL:   baseURL,
		VideoPath:     "results/run-1/video.mp4",
		LastFramePath: "results/run-1/last.png",
		LogPath:       "results/run-1/log.txt",
		Platform:      "android",
	})

	require.Equal(t, &runner.APIError{
		Code:    http.StatusUnauthorized,
		Domain:  "authorization",
		Reason:  "missing credentials",
		Message: "missing Credimi credentials: set CREDIMI_USER_API_KEY or CREDIMI_INTERNAL_ADMIN_KEY",
	}, apiErr)
}

func TestStorePipelineResult_RequestFailures(t *testing.T) {
	baseURL := "http://example.local"
	storeURL := baseURL + "/api/wallet/store-pipeline-result"

	newServer := func(client *fakeHTTPClient) *runnerService {
		store := &memoryFileStore{}
		writer, err := store.Create("results/run-1/video.mp4")
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		writer, err = store.Create("results/run-1/last.png")
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		writer, err = store.Create("results/run-1/log.txt")
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		return NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
			"prod": {URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"},
		}, Deps{
			HTTPClient: client,
			FileStore:  store,
		})
	}

	t.Run("upstream request error", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodPost + " " + storeURL: func(req *http.Request) (*http.Response, error) {
					return nil, errors.New("request failed")
				},
			},
		}

		_, apiErr := newServer(client).storePipelineResultLogic(storePipelineResultPayload{
			InstanceURL:   baseURL,
			VideoPath:     "results/run-1/video.mp4",
			LastFramePath: "results/run-1/last.png",
			LogPath:       "results/run-1/log.txt",
			Platform:      "android",
		})

		require.Equal(t, "request failed", apiErr.Reason)
		require.Equal(t, "request failed", apiErr.Message)
	})

	t.Run("read response error", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodPost + " " + storeURL: func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       errorReadCloser{},
						Header:     make(http.Header),
					}, nil
				},
			},
		}

		_, apiErr := newServer(client).storePipelineResultLogic(storePipelineResultPayload{
			InstanceURL:   baseURL,
			VideoPath:     "results/run-1/video.mp4",
			LastFramePath: "results/run-1/last.png",
			LogPath:       "results/run-1/log.txt",
			Platform:      "android",
		})

		require.Equal(t, "request failed", apiErr.Reason)
		require.Equal(t, "read failed", apiErr.Message)
	})

	t.Run("non-200 with unparsable JSON", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodPost + " " + storeURL: func(req *http.Request) (*http.Response, error) {
					return newResponse(http.StatusBadRequest, "{"), nil
				},
			},
		}

		_, apiErr := newServer(client).storePipelineResultLogic(storePipelineResultPayload{
			InstanceURL:   baseURL,
			VideoPath:     "results/run-1/video.mp4",
			LastFramePath: "results/run-1/last.png",
			LogPath:       "results/run-1/log.txt",
			Platform:      "android",
		})

		require.Equal(t, "request failed", apiErr.Reason)
		require.Equal(t, "upstream", apiErr.Domain)
		require.Contains(t, apiErr.Message, "upstream request failed with status 400")
	})
}

func TestStorePipelineResult_InvalidJSON(t *testing.T) {
	server := NewRunnerService(NewProcessStore(), nil)
	ctx := cluelog.Context(context.Background(), cluelog.WithFormat(cluelog.FormatJSON))
	handler := NewHTTPHandler(ctx, server, false)
	req := httptest.NewRequest(http.MethodPost, "/credimi/pipeline-result", strings.NewReader("{"))
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

func TestStorePipelineResult_InvalidPlatform(t *testing.T) {
	server := NewRunnerService(nil, nil)

	result, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
		InstanceURL:   "http://example.local",
		VideoPath:     "results/run-1/video.mp4",
		LastFramePath: "results/run-1/last.png",
		Platform:      "desktop",
	})

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "server",
		Reason:  "invalid platform",
		Message: "supported values are android or ios",
	}, apiErr)
}
