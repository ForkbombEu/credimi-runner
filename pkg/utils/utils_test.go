package utils

import (
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func resetTokenCache() {
	tokenCacheGlobalMutex.Lock()
	defer tokenCacheGlobalMutex.Unlock()
	tokenCache = make(map[string]*tokenCacheEntry)
}

func makeHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestGetEnvironmentVariable(t *testing.T) {
	t.Setenv("ENV_STRING_TEST", "")
	require.Equal(t, "fallback", GetEnvironmentVariable("ENV_STRING_TEST", "fallback"))

	t.Setenv("ENV_STRING_TEST", "value")
	require.Equal(t, "value", GetEnvironmentVariable("ENV_STRING_TEST", "fallback"))

	t.Setenv("ENV_STRING_TEST", "\"quoted\"")
	require.Equal(t, "quoted", GetEnvironmentVariable("ENV_STRING_TEST", "fallback"))

	t.Setenv("ENV_STRING_TEST", "'quoted-single'")
	require.Equal(t, "quoted-single", GetEnvironmentVariable("ENV_STRING_TEST", "fallback"))

	require.PanicsWithValue(t, "The environment variable ENV_REQUIRED_TEST is required", func() {
		_ = GetEnvironmentVariable("ENV_REQUIRED_TEST", "", true)
	})
}

func TestGetEnvironmentVariableAsInteger(t *testing.T) {
	t.Setenv("ENV_INT_TEST", "")
	v, err := GetEnvironmentVariableAsInteger("ENV_INT_TEST", 8050, false)
	require.NoError(t, err)
	require.Equal(t, 8050, v)

	t.Setenv("ENV_INT_TEST", "123")
	v, err = GetEnvironmentVariableAsInteger("ENV_INT_TEST")
	require.NoError(t, err)
	require.Equal(t, 123, v)

	t.Setenv("ENV_INT_TEST", "not-a-number")
	_, err = GetEnvironmentVariableAsInteger("ENV_INT_TEST")
	require.Error(t, err)

	t.Setenv("ENV_INT_TEST", "9223372036854775808")
	_, err = GetEnvironmentVariableAsInteger("ENV_INT_TEST")
	require.Error(t, err)

	if math.MaxInt == math.MaxInt64 {
		t.Setenv("ENV_INT_TEST", "9223372036854775807")
		v, err = GetEnvironmentVariableAsInteger("ENV_INT_TEST")
		require.NoError(t, err)
		require.Equal(t, int64(math.MaxInt64), int64(v))
	}
}

func TestLoadInstances(t *testing.T) {
	t.Setenv("CREDIMI_URL", "http://prod.local")
	t.Setenv("CREDIMI_PB_ADMIN", "admin")
	t.Setenv("CREDIMI_PB_PASS", "pass")
	t.Setenv("CREDIMI_USER_API_KEY", "prod-user-key")
	t.Setenv("CREDIMI_STAGING_URL", "http://staging.local")
	t.Setenv("CREDIMI_STAGING_PB_ADMIN", "st-admin")
	t.Setenv("CREDIMI_STAGING_PB_PASS", "st-pass")
	t.Setenv("CREDIMI_STAGING_USER_API_KEY", "st-user-key")
	t.Setenv("CREDIMI_DEV_URL", "http://dev.local")
	t.Setenv("CREDIMI_DEV_PB_ADMIN", "dev-admin")
	t.Setenv("CREDIMI_DEV_PB_PASS", "dev-pass")
	t.Setenv("CREDIMI_DEV_USER_API_KEY", "dev-user-key")

	instances := LoadInstances()
	require.Equal(t, "http://prod.local", instances["production"].URL)
	require.Equal(t, "admin", instances["production"].PB_ADMIN)
	require.Equal(t, "prod-user-key", instances["production"].UserAPIKey)
	require.Equal(t, "st-pass", instances["staging"].PB_PASS)
	require.Equal(t, "st-user-key", instances["staging"].UserAPIKey)
	require.Equal(t, "http://dev.local", instances["dev"].URL)
	require.Equal(t, "dev-user-key", instances["dev"].UserAPIKey)
}

func TestLoadInstances_DefaultProductionURL(t *testing.T) {
	t.Setenv("CREDIMI_URL", "")

	instances := LoadInstances()
	require.Equal(t, defaultCredimiURL, instances["production"].URL)
}

func TestJoinAndNormalizeURL(t *testing.T) {
	joined := JoinURL("http://example.local/base/", "/api", "v1", "items")
	require.Equal(t, "http://example.local/base/api/v1/items", joined)

	quoted := JoinURL("\"http://example.local/base/\"", "/api", "v1")
	require.Equal(t, "http://example.local/base/api/v1", quoted)

	require.NotPanics(t, func() {
		_ = JoinURL("%zz", "api")
	})

	normalized, err := NormalizeURL("http://example.local/api/")
	require.NoError(t, err)
	require.Equal(t, "http://example.local/api", normalized)

	_, err = NormalizeURL("")
	require.ErrorContains(t, err, "empty URL")
}

func TestGetAdminToken_CachedToken(t *testing.T) {
	resetTokenCache()
	tokenCache["http://cached.local|admin"] = &tokenCacheEntry{
		token:     "cached-token",
		expiresAt: time.Now().Add(time.Hour),
	}

	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })
	http.DefaultClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("unexpected HTTP call to %s", req.URL.String())
			return nil, nil
		}),
	}

	token, err := GetAdminToken(Instance{
		URL:      "http://cached.local",
		PB_ADMIN: "admin",
		PB_PASS:  "pass",
	})
	require.NoError(t, err)
	require.Equal(t, "cached-token", token)
}

func TestGetAdminToken_SuccessAndCache(t *testing.T) {
	resetTokenCache()

	var calls int
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })
	http.DefaultClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			require.Equal(t, "http://example.local/api/collections/_superusers/auth-with-password", req.URL.String())
			return makeHTTPResponse(http.StatusOK, `{"token":"token-abc"}`), nil
		}),
	}

	instance := Instance{
		URL:      "http://example.local",
		PB_ADMIN: "admin",
		PB_PASS:  "pass",
	}
	token, err := GetAdminToken(instance)
	require.NoError(t, err)
	require.Equal(t, "token-abc", token)

	token2, err := GetAdminToken(instance)
	require.NoError(t, err)
	require.Equal(t, "token-abc", token2)
	require.Equal(t, 1, calls)
}

func TestGetAdminToken_FailurePaths(t *testing.T) {
	testCases := []struct {
		name       string
		response   *http.Response
		transport  error
		errContain string
	}{
		{
			name:       "http transport error",
			transport:  errors.New("network down"),
			errContain: "failed to contact PocketBase",
		},
		{
			name:       "non-200 response",
			response:   makeHTTPResponse(http.StatusUnauthorized, ""),
			errContain: "auth failed",
		},
		{
			name:       "invalid JSON response",
			response:   makeHTTPResponse(http.StatusOK, "{"),
			errContain: "failed to decode PocketBase response",
		},
		{
			name:       "missing token",
			response:   makeHTTPResponse(http.StatusOK, `{"token":""}`),
			errContain: "no token returned by PocketBase",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetTokenCache()
			originalClient := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = originalClient })
			http.DefaultClient = &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if tc.transport != nil {
						return nil, tc.transport
					}
					return tc.response, nil
				}),
			}

			_, err := GetAdminToken(Instance{
				URL:      "http://failure.local",
				PB_ADMIN: "admin",
				PB_PASS:  "pass",
			})
			require.ErrorContains(t, err, tc.errContain)
		})
	}
}

func TestGetUserAPIKeyToken_SuccessAndCache(t *testing.T) {
	resetTokenCache()

	var calls int
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })
	http.DefaultClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			require.Equal(t, "http://example.local/api/apikey/authenticate", req.URL.String())
			require.Equal(t, "user-key-123", req.Header.Get(userAPIKeyHeader))
			return makeHTTPResponse(http.StatusOK, `{"token":"token-user"}`), nil
		}),
	}

	instance := Instance{
		URL:        "http://example.local",
		UserAPIKey: "user-key-123",
	}

	token, err := GetUserAPIKeyToken(instance)
	require.NoError(t, err)
	require.Equal(t, "token-user", token)

	token, err = GetUserAPIKeyToken(instance)
	require.NoError(t, err)
	require.Equal(t, "token-user", token)
	require.Equal(t, 1, calls)
}

func TestGetBearerToken_PrefersUserAPIKey(t *testing.T) {
	resetTokenCache()

	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })
	http.DefaultClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "http://example.local/api/apikey/authenticate", req.URL.String())
			return makeHTTPResponse(http.StatusOK, `{"token":"token-user"}`), nil
		}),
	}

	token, err := GetBearerToken(Instance{
		URL:        "http://example.local",
		PB_ADMIN:   "admin",
		PB_PASS:    "pass",
		UserAPIKey: "user-key-123",
	})
	require.NoError(t, err)
	require.Equal(t, "token-user", token)
}
