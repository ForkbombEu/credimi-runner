package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
)

func resetClientFlagsForTest() {
	clientURL = ""
	clientHost = "127.0.0.1"
	clientPort = 8050
	clientSecure = false
	clientTimeout = 30
	clientVerbose = false
}

func TestResolveClientBaseURL(t *testing.T) {
	t.Run("uses explicit url", func(t *testing.T) {
		resetClientFlagsForTest()
		clientURL = "http://api.local:8050"
		got, err := resolveClientBaseURL()
		require.NoError(t, err)
		require.Equal(t, "http://api.local:8050", got)
	})

	t.Run("rejects empty host", func(t *testing.T) {
		resetClientFlagsForTest()
		clientHost = ""
		_, err := resolveClientBaseURL()
		require.ErrorContains(t, err, "--host cannot be empty")
	})

	t.Run("rejects invalid port", func(t *testing.T) {
		resetClientFlagsForTest()
		clientPort = 70000
		_, err := resolveClientBaseURL()
		require.ErrorContains(t, err, "--port must be in 1..65535")
	})

	t.Run("supports secure scheme", func(t *testing.T) {
		resetClientFlagsForTest()
		clientHost = "example.local"
		clientPort = 8443
		clientSecure = true
		got, err := resolveClientBaseURL()
		require.NoError(t, err)
		require.Equal(t, "https://example.local:8443", got)
	})

	t.Run("keeps host port if already present", func(t *testing.T) {
		resetClientFlagsForTest()
		clientHost = "127.0.0.1:9000"
		got, err := resolveClientBaseURL()
		require.NoError(t, err)
		require.Equal(t, "http://127.0.0.1:9000", got)
	})
}

func TestClientCmdRunE_ValidatesURL(t *testing.T) {
	t.Run("invalid scheme", func(t *testing.T) {
		resetClientFlagsForTest()
		clientURL = "ftp://example.local:8050"
		err := clientCmd.RunE(clientCmd, []string{"worker", "process-list"})
		require.ErrorContains(t, err, "invalid scheme")
	})

	t.Run("missing host", func(t *testing.T) {
		resetClientFlagsForTest()
		clientURL = "http://"
		err := clientCmd.RunE(clientCmd, []string{"worker", "process-list"})
		require.ErrorContains(t, err, "missing host")
	})

	t.Run("parses endpoint and executes request", func(t *testing.T) {
		resetClientFlagsForTest()
		clientURL = "http://example.local"
		origParse := parseEndpoint
		t.Cleanup(func() { parseEndpoint = origParse })
		parseEndpoint = func(
			scheme, host string,
			doer goahttp.Doer,
			enc func(*http.Request) goahttp.Encoder,
			dec func(*http.Response) goahttp.Decoder,
			restore bool,
		) (goa.Endpoint, any, error) {
			return func(ctx context.Context, payload any) (any, error) {
				return nil, errors.New("endpoint failed")
			}, map[string]any{}, nil
		}
		origArgs := os.Args
		t.Cleanup(func() { os.Args = origArgs })

		err := clientCmd.RunE(clientCmd, []string{"worker", "process-list"})
		require.ErrorContains(t, err, "endpoint failed")
		require.Equal(t, origArgs, os.Args)
	})
}

func TestClientCmdRunE_ResponseHandling(t *testing.T) {
	t.Run("nil response", func(t *testing.T) {
		resetClientFlagsForTest()
		clientURL = "http://example.local"
		origParse := parseEndpoint
		t.Cleanup(func() { parseEndpoint = origParse })
		parseEndpoint = func(
			scheme, host string,
			doer goahttp.Doer,
			enc func(*http.Request) goahttp.Encoder,
			dec func(*http.Response) goahttp.Decoder,
			restore bool,
		) (goa.Endpoint, any, error) {
			return func(ctx context.Context, payload any) (any, error) {
				return nil, nil
			}, map[string]any{}, nil
		}

		require.NoError(t, clientCmd.RunE(clientCmd, []string{"worker", "process-list"}))
	})

	t.Run("marshal response error", func(t *testing.T) {
		resetClientFlagsForTest()
		clientURL = "http://example.local"
		origParse := parseEndpoint
		t.Cleanup(func() { parseEndpoint = origParse })
		parseEndpoint = func(
			scheme, host string,
			doer goahttp.Doer,
			enc func(*http.Request) goahttp.Encoder,
			dec func(*http.Response) goahttp.Decoder,
			restore bool,
		) (goa.Endpoint, any, error) {
			return func(ctx context.Context, payload any) (any, error) {
				return map[string]any{"bad": make(chan int)}, nil
			}, map[string]any{}, nil
		}

		err := clientCmd.RunE(clientCmd, []string{"worker", "process-list"})
		require.ErrorContains(t, err, "marshal response")
	})

	t.Run("prints JSON response", func(t *testing.T) {
		resetClientFlagsForTest()
		clientURL = "http://example.local"
		origParse := parseEndpoint
		t.Cleanup(func() { parseEndpoint = origParse })
		parseEndpoint = func(
			scheme, host string,
			doer goahttp.Doer,
			enc func(*http.Request) goahttp.Encoder,
			dec func(*http.Response) goahttp.Decoder,
			restore bool,
		) (goa.Endpoint, any, error) {
			return func(ctx context.Context, payload any) (any, error) {
				return map[string]any{"status": "ok"}, nil
			}, map[string]any{}, nil
		}

		origStdout := os.Stdout
		r, w, err := os.Pipe()
		require.NoError(t, err)
		os.Stdout = w
		t.Cleanup(func() { os.Stdout = origStdout })

		require.NoError(t, clientCmd.RunE(clientCmd, []string{"worker", "process-list"}))
		require.NoError(t, w.Close())
		output, err := io.ReadAll(r)
		require.NoError(t, err)
		require.Contains(t, string(output), `"status": "ok"`)
	})
}

func TestExecute(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })
	os.Args = []string{"credimi-runner", "--help"}

	Execute()
}
