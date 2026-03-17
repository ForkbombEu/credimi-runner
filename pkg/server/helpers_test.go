package server

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/stretchr/testify/require"
)

type failingFileStore struct {
	memoryFileStore
	mkdirErr     error
	statErr      error
	createErr    error
	createWriter io.WriteCloser
	openErr      error
	openReader   io.ReadCloser
	removeAllErr error
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
	if f.createWriter != nil {
		return f.createWriter, nil
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

func (f failingFileStore) RemoveAll(path string) error {
	if f.removeAllErr != nil {
		return f.removeAllErr
	}
	return f.memoryFileStore.RemoveAll(path)
}

type errReadCloser struct{}

func (errReadCloser) Read(p []byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error               { return nil }

type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) { return 0, errors.New("write failed") }

type errWriteCloser struct{}

func (errWriteCloser) Write(p []byte) (int, error) { return 0, errors.New("write failed") }
func (errWriteCloser) Close() error                { return nil }

type closeErrWriteCloser struct {
	bytes.Buffer
}

func (c *closeErrWriteCloser) Close() error { return errors.New("close failed") }

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
		_, err := downloadFileIfMissing("http://example.local/file.apk", "token", "apk", "file.apk", &fakeHTTPClient{}, &failingFileStore{
			mkdirErr: errors.New("mkdir boom"),
		})
		require.ErrorContains(t, err, "failed to create apps directory")
	})

	t.Run("invalid request URL", func(t *testing.T) {
		_, err := downloadFileIfMissing(":", "token", "apk", "file.apk", &fakeHTTPClient{}, &failingFileStore{
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

		_, err := downloadFileIfMissing("http://example.local/file.apk", "token", "apk", "file.apk", client, &failingFileStore{
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

		_, err := downloadFileIfMissing("http://example.local/file.apk", "token", "apk", "file.apk", client, &failingFileStore{
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

		_, err := downloadFileIfMissing("http://example.local/file.apk", "token", "apk", "file.apk", client, &failingFileStore{
			statErr:   os.ErrNotExist,
			createErr: errors.New("create failed"),
		})
		require.ErrorContains(t, err, "failed to create file")
	})
}

func TestDownloadInstallerIfMissing_ErrorPropagation(t *testing.T) {
	_, err := downloadInstallerIfMissing(":", "token", "ios", "app.zip", "ios", &fakeHTTPClient{}, &failingFileStore{
		statErr: os.ErrNotExist,
	})
	require.ErrorContains(t, err, "failed to create request")
}

func TestUnzipIOSAppIfNeeded_CachedAppPath(t *testing.T) {
	store := &memoryFileStore{}
	require.NoError(t, store.MkdirAll("apps/cache/Payload/Test.app", 0755))

	writer, err := store.Create("apps/cache.app-path")
	require.NoError(t, err)
	_, err = writer.Write([]byte("apps/cache/Payload/Test.app"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	path, err := unzipIOSAppIfNeeded("apps/cache.zip", "cache", store)
	require.NoError(t, err)
	require.Equal(t, "apps/cache/Payload/Test.app", path)
}

func TestUnzipIOSAppIfNeeded_ErrorPaths(t *testing.T) {
	t.Run("archive missing", func(t *testing.T) {
		_, err := unzipIOSAppIfNeeded("apps/missing.zip", "missing", &memoryFileStore{})
		require.ErrorContains(t, err, "failed to read ios archive")
	})

	t.Run("invalid zip bytes", func(t *testing.T) {
		store := &memoryFileStore{}
		writer, err := store.Create("apps/invalid.zip")
		require.NoError(t, err)
		_, err = writer.Write([]byte("not-a-zip"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		_, err = unzipIOSAppIfNeeded("apps/invalid.zip", "invalid", store)
		require.ErrorContains(t, err, "failed to open ios archive")
	})

	t.Run("remove extracted app fails", func(t *testing.T) {
		store := failingFileStore{
			memoryFileStore: memoryFileStore{
				files: map[string]*bytes.Buffer{
					"apps/remove.zip": bytes.NewBuffer(createZipArchive(t, map[string]string{
						"Payload/Test.app/Test": "binary",
					})),
				},
			},
			removeAllErr: errors.New("remove failed"),
		}

		_, err := unzipIOSAppIfNeeded("apps/remove.zip", "remove", &store)
		require.ErrorContains(t, err, "failed to clear extracted ios app")
	})

	t.Run("persist app path fails", func(t *testing.T) {
		store := &markerCreateFailFileStore{
			memoryFileStore: memoryFileStore{
				files: map[string]*bytes.Buffer{
					"apps/persist.zip": bytes.NewBuffer(createZipArchive(t, map[string]string{
						"Payload/Test.app/Test": "binary",
					})),
				},
			},
			markerPath: "apps/persist.app-path",
		}

		_, err := unzipIOSAppIfNeeded("apps/persist.zip", "persist", store)
		require.ErrorContains(t, err, "failed to persist ios app path")
	})
}

func TestExtractIOSAppArchive_ErrorPaths(t *testing.T) {
	t.Run("success with directory entries", func(t *testing.T) {
		reader := newZipReader(t, func(w *zip.Writer) {
			header := &zip.FileHeader{Name: "Payload/"}
			header.SetMode(os.ModeDir | 0755)
			_, err := w.CreateHeader(header)
			require.NoError(t, err)

			header = &zip.FileHeader{Name: "Payload/Test.app/"}
			header.SetMode(os.ModeDir | 0755)
			_, err = w.CreateHeader(header)
			require.NoError(t, err)

			entry, err := w.Create("Payload/Test.app/TestBinary")
			require.NoError(t, err)
			_, err = entry.Write([]byte("binary"))
			require.NoError(t, err)
		})

		store := &memoryFileStore{}
		appPath, err := extractIOSAppArchive(reader, "apps/zip", store)
		require.NoError(t, err)
		require.Equal(t, "apps/zip/Payload/Test.app", appPath)

		_, err = store.Stat("apps/zip/Payload/Test.app/TestBinary")
		require.NoError(t, err)
	})

	t.Run("invalid entry path", func(t *testing.T) {
		reader := newZipReader(t, func(w *zip.Writer) {
			entry, err := w.Create("../evil.txt")
			require.NoError(t, err)
			_, err = entry.Write([]byte("bad"))
			require.NoError(t, err)
		})

		_, err := extractIOSAppArchive(reader, "apps/zip", &memoryFileStore{})
		require.ErrorContains(t, err, "invalid zip entry path")
	})

	t.Run("directory entry and missing app bundle", func(t *testing.T) {
		reader := newZipReader(t, func(w *zip.Writer) {
			header := &zip.FileHeader{Name: "Payload/"}
			header.SetMode(os.ModeDir | 0755)
			_, err := w.CreateHeader(header)
			require.NoError(t, err)

			entry, err := w.Create("Payload/readme.txt")
			require.NoError(t, err)
			_, err = entry.Write([]byte("text"))
			require.NoError(t, err)
		})

		_, err := extractIOSAppArchive(reader, "apps/zip", &memoryFileStore{})
		require.ErrorContains(t, err, "does not contain an .app bundle")
	})

	t.Run("create extracted directory for file fails", func(t *testing.T) {
		reader := newZipReader(t, func(w *zip.Writer) {
			entry, err := w.Create("Payload/Test.app/TestBinary")
			require.NoError(t, err)
			_, err = entry.Write([]byte("binary"))
			require.NoError(t, err)
		})

		_, err := extractIOSAppArchive(reader, "apps/zip", &failingFileStore{
			mkdirErr: errors.New("mkdir failed"),
		})
		require.ErrorContains(t, err, "failed to create extracted directory for Payload/Test.app/TestBinary")
	})

	t.Run("create extracted file fails", func(t *testing.T) {
		reader := newZipReader(t, func(w *zip.Writer) {
			entry, err := w.Create("Payload/Test.app/TestBinary")
			require.NoError(t, err)
			_, err = entry.Write([]byte("binary"))
			require.NoError(t, err)
		})

		_, err := extractIOSAppArchive(reader, "apps/zip", &failingFileStore{
			createErr: errors.New("create failed"),
		})
		require.ErrorContains(t, err, "failed to create extracted file Payload/Test.app/TestBinary")
	})

	t.Run("extract zip entry write fails", func(t *testing.T) {
		reader := newZipReader(t, func(w *zip.Writer) {
			entry, err := w.Create("Payload/Test.app/TestBinary")
			require.NoError(t, err)
			_, err = entry.Write([]byte("binary"))
			require.NoError(t, err)
		})

		_, err := extractIOSAppArchive(reader, "apps/zip", &failingFileStore{
			createWriter: errWriteCloser{},
		})
		require.ErrorContains(t, err, "failed to extract zip entry Payload/Test.app/TestBinary")
	})

	t.Run("close extracted file fails", func(t *testing.T) {
		reader := newZipReader(t, func(w *zip.Writer) {
			entry, err := w.Create("Payload/Test.app/TestBinary")
			require.NoError(t, err)
			_, err = entry.Write([]byte("binary"))
			require.NoError(t, err)
		})

		_, err := extractIOSAppArchive(reader, "apps/zip", &failingFileStore{
			createWriter: &closeErrWriteCloser{},
		})
		require.ErrorContains(t, err, "failed to close extracted file Payload/Test.app/TestBinary")
	})
}

func TestReadStoredAppPath_EmptyMarker(t *testing.T) {
	store := &memoryFileStore{}
	writer, err := store.Create("apps/empty.app-path")
	require.NoError(t, err)
	_, err = writer.Write([]byte(" \n "))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	_, err = readStoredAppPath("apps/empty.app-path", store)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestWriteStoredAppPath_WriteFailure(t *testing.T) {
	err := writeStoredAppPath("apps/fail.app-path", "apps/fail/Test.app", &failingFileStore{
		createWriter: errWriteCloser{},
	})
	require.ErrorContains(t, err, "write failed")
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

type markerCreateFailFileStore struct {
	memoryFileStore
	markerPath string
}

func (m *markerCreateFailFileStore) Create(name string) (io.WriteCloser, error) {
	if filepath.Clean(name) == filepath.Clean(m.markerPath) {
		return nil, errors.New("marker create failed")
	}
	return m.memoryFileStore.Create(name)
}

func newZipReader(t *testing.T, build func(*zip.Writer)) *zip.Reader {
	t.Helper()

	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	build(writer)
	require.NoError(t, writer.Close())

	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	return reader
}
