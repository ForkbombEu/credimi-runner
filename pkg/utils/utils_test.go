package utils

import (
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

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
	t.Setenv("CREDIMI_USER_API_KEY", "prod-user-key")
	t.Setenv("CREDIMI_INTERNAL_ADMIN_KEY", "prod-admin-key")
	t.Setenv("CREDIMI_STAGING_URL", "http://staging.local")
	t.Setenv("CREDIMI_STAGING_USER_API_KEY", "st-user-key")
	t.Setenv("CREDIMI_STAGING_INTERNAL_ADMIN_KEY", "st-admin-key")
	t.Setenv("CREDIMI_DEV_URL", "http://dev.local")
	t.Setenv("CREDIMI_DEV_USER_API_KEY", "dev-user-key")
	t.Setenv("CREDIMI_DEV_INTERNAL_ADMIN_KEY", "dev-admin-key")

	instances := LoadInstances()
	require.Equal(t, "http://prod.local", instances["production"].URL)
	require.Equal(t, "prod-user-key", instances["production"].UserAPIKey)
	require.Equal(t, "prod-admin-key", instances["production"].InternalAdminKey)
	require.Equal(t, "st-user-key", instances["staging"].UserAPIKey)
	require.Equal(t, "st-admin-key", instances["staging"].InternalAdminKey)
	require.Equal(t, "http://dev.local", instances["dev"].URL)
	require.Equal(t, "dev-user-key", instances["dev"].UserAPIKey)
	require.Equal(t, "dev-admin-key", instances["dev"].InternalAdminKey)
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
		UserAPIKey: "user-key-123",
	})
	require.NoError(t, err)
	require.Equal(t, "token-user", token)
}
