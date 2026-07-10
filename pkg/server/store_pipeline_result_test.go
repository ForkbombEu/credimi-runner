package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	files  map[string][]capturedMultipartFile
	err    error
}

type capturedMultipartFile struct {
	name string
	data string
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
	server := NewRunnerService(nil, utils.Instance{})
	payload := storePipelineResultPayload{
		VideoPath: "",
		Platform:  "android",
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
				capture.files = make(map[string][]capturedMultipartFile)
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
						data, _ := io.ReadAll(part)
						capture.files[part.FormName()] = append(capture.files[part.FormName()], capturedMultipartFile{name: part.FileName(), data: string(data)})
					} else {
						data, _ := io.ReadAll(part)
						capture.fields[part.FormName()] = string(data)
					}
					_ = part.Close()
				}
				return newResponse(http.StatusOK, `{"status":"stored","maestro_screenshot_urls":["https://example.test/one.png"]}`), nil
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
		HTTPClient:          client,
		FileStore:           store,
		ManagedWorkflowRoot: "results",
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"}, deps)
	payload := storePipelineResultPayload{
		VideoPath:        videoPath,
		LastFramePath:    lastFramePath,
		LogPath:          logPath,
		RunIdentifier:    "run-1",
		RunnerIdentifier: "runner-1",
		Platform:         "android",
	}

	result, apiErr := server.storePipelineResultLogic(payload)

	require.Nil(t, apiErr)
	require.JSONEq(t, `{"status":"stored","maestro_screenshot_urls":["https://example.test/one.png"]}`, string(result))
	require.NoError(t, capture.err)
	require.Equal(t, "runner-1", capture.fields["runner_identifier"])
	require.Equal(t, "run-1", capture.fields["run_identifier"])
	require.Equal(t, "android", capture.fields["platform"])
	require.Equal(t, []capturedMultipartFile{{name: "video.mp4", data: "video"}}, capture.files["result_video"])
	require.Equal(t, []capturedMultipartFile{{name: "last.png", data: "frame"}}, capture.files["last_frame"])
	require.Equal(t, []capturedMultipartFile{{name: "log.txt", data: "log"}}, capture.files["logfile"])
	require.Empty(t, capture.files["maestro_screenshots"])
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
				capture.files = make(map[string][]capturedMultipartFile)
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
						data, _ := io.ReadAll(part)
						capture.files[part.FormName()] = append(capture.files[part.FormName()], capturedMultipartFile{name: part.FileName(), data: string(data)})
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

	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"}, Deps{
		HTTPClient: client,
		FileStore:  store,
	})

	result, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
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
	require.Equal(t, "video.mp4", capture.files["result_video"][0].name)
	require.Equal(t, "last.png", capture.files["last_frame"][0].name)
	require.Equal(t, "log.txt", capture.files["logfile"][0].name)
}

func TestValidateScreenshotPaths(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "child", "screen.png")
	require.NoError(t, os.MkdirAll(filepath.Dir(valid), 0755))
	require.NoError(t, os.WriteFile(valid, []byte("png"), 0600))

	paths, apiErr := validateScreenshotPaths([]string{valid, valid}, root, osFileStore{})
	require.Nil(t, apiErr)
	require.Equal(t, []string{valid}, paths)

	tests := map[string][]string{
		"empty":     {""},
		"missing":   {filepath.Join(root, "child", "missing.png")},
		"escaped":   {filepath.Join(root, "..", "escaped.png")},
		"not image": {filepath.Join(root, "child", "screen.txt")},
		"directory": {filepath.Join(root, "child")},
	}
	require.NoError(t, os.WriteFile(tests["not image"][0], []byte("text"), 0600))
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			_, apiErr := validateScreenshotPaths(input, root, osFileStore{})
			require.NotNil(t, apiErr)
			require.Equal(t, http.StatusBadRequest, apiErr.Code)
		})
	}

	symlink := filepath.Join(root, "child", "link.png")
	require.NoError(t, os.Symlink(valid, symlink))
	_, apiErr = validateScreenshotPaths([]string{symlink}, root, osFileStore{})
	require.NotNil(t, apiErr)
	require.Contains(t, apiErr.Message, "symlink")

	symlinkedDirectory := filepath.Join(root, "linked-child")
	require.NoError(t, os.Symlink(filepath.Dir(valid), symlinkedDirectory))
	_, apiErr = validateScreenshotPaths([]string{filepath.Join(symlinkedDirectory, "screen.png")}, root, osFileStore{})
	require.NotNil(t, apiErr)
	require.Contains(t, apiErr.Message, "symlink")

	tooMany := make([]string, maxMaestroScreenshots+1)
	_, apiErr = validateScreenshotPaths(tooMany, root, osFileStore{})
	require.NotNil(t, apiErr)
	require.Contains(t, apiErr.Message, "maximum of 99")
}

func TestManagedArtifactDirectoriesRejectsRootAndParents(t *testing.T) {
	root := filepath.Join("credimi", "workflows")
	require.Equal(t, []string{filepath.Join(root, "child")}, managedArtifactDirectories([]string{
		filepath.Join(root, "file.png"),
		filepath.Join(root, "child", "file.png"),
		filepath.Join(root, "..", "outside", "file.png"),
	}, root))
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

	store := &trackingFileStore{}
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
		HTTPClient:          client,
		FileStore:           store,
		ManagedWorkflowRoot: "results",
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"}, deps)
	payload := storePipelineResultPayload{
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
	require.Empty(t, store.removed)
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
	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"}, deps)

	result, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
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
	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: baseURL, UserAPIKey: "user-key", InternalAdminKey: "internal-admin-key"}, deps)

	result, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
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

	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{URL: baseURL}, Deps{
		HTTPClient: &fakeHTTPClient{},
		FileStore:  store,
	})

	_, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
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

		return NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{
			URL:              baseURL,
			UserAPIKey:       "user-key",
			InternalAdminKey: "internal-admin-key",
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
	server := NewRunnerService(NewProcessStore(), utils.Instance{})
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
	server := NewRunnerService(nil, utils.Instance{})

	result, apiErr := server.storePipelineResultLogic(storePipelineResultPayload{
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
