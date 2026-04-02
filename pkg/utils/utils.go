package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Instance struct {
	URL        string
	PB_ADMIN   string
	PB_PASS    string
	UserAPIKey string
}

// tokenCacheEntry stores a cached token and its expiration time
type tokenCacheEntry struct {
	token     string
	expiresAt time.Time
	mutex     sync.Mutex
}

var tokenCache = make(map[string]*tokenCacheEntry)
var tokenCacheGlobalMutex sync.Mutex

const userAPIKeyHeader = "Credimi-Api-Key"

// GetEnvironmentVariable retrieves the value of an environment variable.
//
// Parameters:
//   - name: The name of the environment variable to retrieve.
//   - defaultValue (if provided): The value to return if the environment variable is not set.
//   - required (if provided): A boolean indicating whether the environment variable is required.
//
// Returns:
//   - string: The value of the environment variable, or the default value if not set.
//     If the variable is required and not set, the function panics.
func GetEnvironmentVariable(name string, others ...any) string {
	var defaultValue = ""
	var required bool

	if len(others) > 0 {
		if val, ok := others[0].(string); ok {
			defaultValue = val
		}
	}
	if len(others) > 1 {
		if val, ok := others[1].(bool); ok {
			required = val
		}
	}

	output := os.Getenv(name)
	if output == "" {
		output = defaultValue
	}
	output = trimOptionalQuotes(output)
	if output == "" && required {
		panic("The environment variable " + name + " is required")
	}
	return output
}

// GetEnvironmentVariableAsInteger retrieves the value of an environment variable and converts it to an integer.
//
// Parameters:
//   - name: The name of the environment variable to retrieve.
//   - others: Optional variadic parameters:
//   - First parameter (if provided): The default integer value to return if the environment variable is not set or
//     empty.
//   - Second parameter (if provided): A boolean indicating whether the environment variable is required.
//
// Returns:
//
//   - int: The integer value of the environment variable, or the default value if not set or empty.
//
//   - error: An error if the environment variable cannot be parsed as an integer or if the value is out of range for
//     int.
//
//     Returns nil if no error occurred.
func GetEnvironmentVariableAsInteger(name string, others ...any) (int, error) {
	var defaultValue = 0
	var required bool

	if len(others) > 0 {
		if val, ok := others[0].(int); ok {
			defaultValue = val
		}
	}
	if len(others) > 1 {
		if val, ok := others[1].(bool); ok {
			required = val
		}
	}

	output := GetEnvironmentVariable(name, "", required)
	if output == "" {
		return defaultValue, nil
	}
	outputAsInt, err := strconv.ParseInt(output, 10, 64)
	if err != nil {
		return 0, err
	}
	if outputAsInt > math.MaxInt || outputAsInt < math.MinInt {
		return 0, fmt.Errorf("value out of range for int: %d", outputAsInt)
	}
	return int(outputAsInt), nil
}

// GetBearerToken returns a PocketBase bearer token using the configured auth mode.
func GetBearerToken(instance Instance) (string, error) {
	if strings.TrimSpace(instance.UserAPIKey) != "" {
		return GetUserAPIKeyToken(instance)
	}
	return GetAdminToken(instance)
}

// GetAdminToken authenticates with PocketBase using admin credentials.
func GetAdminToken(instance Instance) (string, error) {
	if strings.TrimSpace(instance.PB_ADMIN) == "" || strings.TrimSpace(instance.PB_PASS) == "" {
		return "", fmt.Errorf(
			"missing admin credentials for %s: set CREDIMI_USER_API_KEY or CREDIMI_PB_ADMIN/CREDIMI_PB_PASS",
			instance.URL,
		)
	}

	return getCachedToken(instance.tokenCacheKey(), func() (string, error) {
		url := JoinURL(instance.URL, "api", "collections", "_superusers", "auth-with-password")

		payload := map[string]string{
			"identity": instance.PB_ADMIN,
			"password": instance.PB_PASS,
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("failed to marshal payload: %w", err)
		}
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return "", fmt.Errorf("failed to contact PocketBase: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("auth failed: %s", resp.Status)
		}

		var res struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			return "", fmt.Errorf("failed to decode PocketBase response: %w", err)
		}

		if res.Token == "" {
			return "", fmt.Errorf("no token returned by PocketBase")
		}
		return res.Token, nil
	})
}

// GetUserAPIKeyToken exchanges a user API key for a PocketBase bearer token.
func GetUserAPIKeyToken(instance Instance) (string, error) {
	if strings.TrimSpace(instance.UserAPIKey) == "" {
		return "", fmt.Errorf("missing user API key for %s", instance.URL)
	}

	return getCachedToken(instance.tokenCacheKey(), func() (string, error) {
		url := JoinURL(instance.URL, "api", "apikey", "authenticate")
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return "", fmt.Errorf("failed to create API key auth request: %w", err)
		}
		req.Header.Set(userAPIKeyHeader, instance.UserAPIKey)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("failed to contact PocketBase: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("API key auth failed: %s", resp.Status)
		}

		var res struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			return "", fmt.Errorf("failed to decode PocketBase response: %w", err)
		}
		if res.Token == "" {
			return "", fmt.Errorf("no token returned by PocketBase")
		}
		return res.Token, nil
	})
}

func getCachedToken(cacheKey string, fetch func() (string, error)) (string, error) {
	// Ensure thread-safe access to the token cache map.
	tokenCacheGlobalMutex.Lock()
	entry, exists := tokenCache[cacheKey]
	if !exists {
		entry = &tokenCacheEntry{}
		tokenCache[cacheKey] = entry
	}
	tokenCacheGlobalMutex.Unlock()

	// Lock the entry to prevent concurrent refreshes.
	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	// Return cached token if valid.
	if entry.token != "" && time.Now().Before(entry.expiresAt) {
		return entry.token, nil
	}

	token, err := fetch()
	if err != nil {
		return "", err
	}
	entry.token = token
	// Keep the cache aligned with PocketBase auth token lifetime.
	entry.expiresAt = time.Now().Add(1209600 * time.Second)

	return token, nil
}

func (i Instance) tokenCacheKey() string {
	if strings.TrimSpace(i.UserAPIKey) != "" {
		return i.URL + "|user_api_key"
	}
	return i.URL + "|admin"
}
func LoadInstances() map[string]Instance {
	return map[string]Instance{
		"production": {
			URL:        GetEnvironmentVariable("CREDIMI_URL", "http://localhost:8090"),
			PB_ADMIN:   GetEnvironmentVariable("CREDIMI_PB_ADMIN"),
			PB_PASS:    GetEnvironmentVariable("CREDIMI_PB_PASS"),
			UserAPIKey: GetEnvironmentVariable("CREDIMI_USER_API_KEY"),
		},
		"staging": {
			URL:        GetEnvironmentVariable("CREDIMI_STAGING_URL"),
			PB_ADMIN:   GetEnvironmentVariable("CREDIMI_STAGING_PB_ADMIN"),
			PB_PASS:    GetEnvironmentVariable("CREDIMI_STAGING_PB_PASS"),
			UserAPIKey: GetEnvironmentVariable("CREDIMI_STAGING_USER_API_KEY"),
		},
		"dev": {
			URL:        GetEnvironmentVariable("CREDIMI_DEV_URL"),
			PB_ADMIN:   GetEnvironmentVariable("CREDIMI_DEV_PB_ADMIN"),
			PB_PASS:    GetEnvironmentVariable("CREDIMI_DEV_PB_PASS"),
			UserAPIKey: GetEnvironmentVariable("CREDIMI_DEV_USER_API_KEY"),
		},
	}
}

func JoinURL(base string, parts ...string) string {
	base = trimOptionalQuotes(base)
	u, err := url.Parse(base)
	if err != nil || u == nil {
		joined := strings.TrimRight(base, "/")
		for _, p := range parts {
			p = strings.Trim(p, "/")
			if p == "" {
				continue
			}
			if joined == "" {
				joined = "/" + p
				continue
			}
			joined += "/" + p
		}
		return joined
	}
	for _, p := range parts {
		u.Path, _ = url.JoinPath(u.Path, p)
	}
	return u.String()
}

func trimOptionalQuotes(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func NormalizeURL(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("empty URL")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")

	return parsed.String(), nil
}
