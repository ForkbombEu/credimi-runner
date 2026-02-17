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
	URL      string
	PB_ADMIN string
	PB_PASS  string
}

// tokenCacheEntry stores a cached token and its expiration time
type tokenCacheEntry struct {
	token     string
	expiresAt time.Time
	mutex     sync.Mutex
}

var tokenCache = make(map[string]*tokenCacheEntry)
var tokenCacheGlobalMutex sync.Mutex

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

// GetAdminToken authenticates with PocketBase and returns a bearer token.
func GetAdminToken(instance Instance) (string, error) {

	// Ensure thread-safe access to the token cache map
	tokenCacheGlobalMutex.Lock()
	entry, exists := tokenCache[instance.URL]
	if !exists {
		entry = &tokenCacheEntry{}
		tokenCache[instance.URL] = entry
	}
	tokenCacheGlobalMutex.Unlock()

	// Lock the entry to prevent concurrent refreshes
	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	// Return cached token if valid
	if entry.token != "" && time.Now().Before(entry.expiresAt) {
		return entry.token, nil
	}
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
	entry.token = res.Token
	// Set the expiration time to 14 days
	entry.expiresAt = time.Now().Add(1209600 * time.Second)

	return res.Token, nil
}
func LoadInstances() map[string]Instance {
	return map[string]Instance{
		"production": {
			URL:      GetEnvironmentVariable("CREDIMI_URL", "http://localhost:8090"),
			PB_ADMIN: GetEnvironmentVariable("CREDIMI_PB_ADMIN"),
			PB_PASS:  GetEnvironmentVariable("CREDIMI_PB_PASS"),
		},
		"staging": {
			URL:      GetEnvironmentVariable("CREDIMI_STAGING_URL"),
			PB_ADMIN: GetEnvironmentVariable("CREDIMI_STAGING_PB_ADMIN"),
			PB_PASS:  GetEnvironmentVariable("CREDIMI_STAGING_PB_PASS"),
		},
		"dev": {
			URL:      GetEnvironmentVariable("CREDIMI_DEV_URL"),
			PB_ADMIN: GetEnvironmentVariable("CREDIMI_DEV_PB_ADMIN"),
			PB_PASS:  GetEnvironmentVariable("CREDIMI_DEV_PB_PASS"),
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
