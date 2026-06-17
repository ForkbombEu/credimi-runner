package utils

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

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

func TestLoadInstance(t *testing.T) {
	t.Setenv("CREDIMI_URL", "http://prod.local")
	t.Setenv("CREDIMI_USER_API_KEY", "prod-user-key")
	t.Setenv("CREDIMI_INTERNAL_ADMIN_KEY", "prod-admin-key")

	instance := LoadInstance()
	require.Equal(t, "http://prod.local", instance.URL)
	require.Equal(t, "prod-user-key", instance.UserAPIKey)
	require.Equal(t, "prod-admin-key", instance.InternalAdminKey)
}

func TestLoadInstance_DefaultProductionURL(t *testing.T) {
	t.Setenv("CREDIMI_URL", "")

	instance := LoadInstance()
	require.Equal(t, defaultCredimiURL, instance.URL)
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
