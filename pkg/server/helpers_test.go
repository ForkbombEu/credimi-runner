package server

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"testing"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/stretchr/testify/require"
)

type failingFileStore struct {
	memoryFileStore
	mkdirErr   error
	statErr    error
	createErr  error
	openErr    error
	openReader io.ReadCloser
}

func (f failingFileStore) MkdirAll(path string, perm os.FileMode) error {
	if f.mkdirErr != nil {
		return f.mkdirErr
	}
	return nil
}

func (f failingFileStore) Stat(name string) (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.memoryFileStore.Stat(name)
}

func (f failingFileStore) Create(name string) (io.WriteCloser, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.memoryFileStore.Create(name)
}

func (f failingFileStore) Open(name string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	if f.openReader != nil {
		return f.openReader, nil
	}
	return f.memoryFileStore.Open(name)
}

type errReadCloser struct{}

func (errReadCloser) Read(p []byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error               { return nil }

type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) { return 0, errors.New("write failed") }

func TestDeriveWalletIdentifier(t *testing.T) {
	testCases := []struct {
		name      string
		versionID string
		actionID  string
		expected  string
		ok        bool
	}{
		{name: "derive from action path", versionID: "", actionID: "wallet/action", expected: "wallet", ok: true},
		{name: "derive nested path", versionID: "", actionID: "a/b/c", expected: "a/b", ok: true},
		{name: "skip when version present", versionID: "v1", actionID: "wallet/action", expected: "", ok: false},
		{name: "skip when action has no slash", versionID: "", actionID: "single", expected: "", ok: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := deriveWalletIdentifier(tc.versionID, tc.actionID)
			require.Equal(t, tc.expected, got)
			require.Equal(t, tc.ok, ok)
		})
	}
}

func TestValidateActionIdentifier(t *testing.T) {
	t.Run("client call fails", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodPost + " http://example.local/validate": func(req *http.Request) (*http.Response, error) {
					return nil, errors.New("boom")
				},
			},
		}

		_, err := validateActionIdentifier("http://example.local/validate", "wallet/action", "token", client)
		require.ErrorContains(t, err, "failed to call validate endpoint")
	})

	t.Run("non-200 with invalid error body", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodPost + " http://example.local/validate": func(req *http.Request) (*http.Response, error) {
					return newResponse(http.StatusBadRequest, "{"), nil
				},
			},
		}

		_, err := validateActionIdentifier("http://example.local/validate", "wallet/action", "token", client)
		require.ErrorContains(t, err, "validate failed")
	})

	t.Run("non-200 with API error body", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodPost + " http://example.local/validate": func(req *http.Request) (*http.Response, error) {
					return newResponse(http.StatusBadRequest, `{"status":400,"error":"CredimiAPI","reason":"bad","message":"invalid"}`), nil
				},
			},
		}

		_, err := validateActionIdentifier("http://example.local/validate", "wallet/action", "token", client)
		var apiErr *runner.APIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, "bad", apiErr.Reason)
	})

	t.Run("missing code field", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodPost + " http://example.local/validate": func(req *http.Request) (*http.Response, error) {
					return newResponse(http.StatusOK, `{"record":{}}`), nil
				},
			},
		}

		_, err := validateActionIdentifier("http://example.local/validate", "wallet/action", "token", client)
		require.ErrorContains(t, err, "record missing 'code' field")
	})

	t.Run("success", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodPost + " http://example.local/validate": func(req *http.Request) (*http.Response, error) {
					return newResponse(http.StatusOK, `{"record":{"code":"ACTION"}}`), nil
				},
			},
		}

		code, err := validateActionIdentifier("http://example.local/validate", "wallet/action", "token", client)
		require.NoError(t, err)
		require.Equal(t, "ACTION", code)
	})
}

func TestDownloadFileIfMissing_ErrorPaths(t *testing.T) {
	t.Run("mkdir fails", func(t *testing.T) {
		_, err := downloadFileIfMissing("http://example.local/file.apk", "token", "apk", &fakeHTTPClient{}, &failingFileStore{
			mkdirErr: errors.New("mkdir boom"),
		})
		require.ErrorContains(t, err, "failed to create apps directory")
	})

	t.Run("invalid request URL", func(t *testing.T) {
		_, err := downloadFileIfMissing(":", "token", "apk", &fakeHTTPClient{}, &failingFileStore{
			statErr: os.ErrNotExist,
		})
		require.ErrorContains(t, err, "failed to create request")
	})

	t.Run("download request fails", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodGet + " http://example.local/file.apk": func(req *http.Request) (*http.Response, error) {
					return nil, errors.New("network failed")
				},
			},
		}

		_, err := downloadFileIfMissing("http://example.local/file.apk", "token", "apk", client, &failingFileStore{
			statErr: os.ErrNotExist,
		})
		require.ErrorContains(t, err, "failed to download file")
	})

	t.Run("download non-200", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodGet + " http://example.local/file.apk": func(req *http.Request) (*http.Response, error) {
					return newResponse(http.StatusBadGateway, ""), nil
				},
			},
		}

		_, err := downloadFileIfMissing("http://example.local/file.apk", "token", "apk", client, &failingFileStore{
			statErr: os.ErrNotExist,
		})
		require.ErrorContains(t, err, "download failed")
	})

	t.Run("create file fails", func(t *testing.T) {
		client := &fakeHTTPClient{
			handlers: map[string]func(*http.Request) (*http.Response, error){
				http.MethodGet + " http://example.local/file.apk": func(req *http.Request) (*http.Response, error) {
					return newResponse(http.StatusOK, "apk-bytes"), nil
				},
			},
		}

		_, err := downloadFileIfMissing("http://example.local/file.apk", "token", "apk", client, &failingFileStore{
			statErr:   os.ErrNotExist,
			createErr: errors.New("create failed"),
		})
		require.ErrorContains(t, err, "failed to create file")
	})
}

func TestAddFileToMultipart_ErrorPaths(t *testing.T) {
	t.Run("open file fails", func(t *testing.T) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)

		apiErr := addFileToMultipart(writer, "file", "missing.txt", &failingFileStore{
			openErr: errors.New("open failed"),
		})
		require.NotNil(t, apiErr)
		require.Equal(t, "file open failed", apiErr.Reason)
	})

	t.Run("create form file fails", func(t *testing.T) {
		store := &memoryFileStore{}
		w, err := store.Create("result.txt")
		require.NoError(t, err)
		_, _ = w.Write([]byte("ok"))
		require.NoError(t, w.Close())

		writer := multipart.NewWriter(errWriter{})

		apiErr := addFileToMultipart(writer, "file", "result.txt", store)
		require.NotNil(t, apiErr)
		require.Equal(t, "multipart failed", apiErr.Reason)
	})

	t.Run("copy to multipart fails", func(t *testing.T) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)

		apiErr := addFileToMultipart(writer, "file", "result.txt", &failingFileStore{
			openReader: errReadCloser{},
		})
		require.NotNil(t, apiErr)
		require.Equal(t, "multipart failed", apiErr.Reason)
	})
}
