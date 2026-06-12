package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func restoreEnv(t *testing.T, key string) {
	t.Helper()

	value, ok := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	t.Cleanup(func() {
		var err error
		if ok {
			err = os.Setenv(key, value)
		} else {
			err = os.Unsetenv(key)
		}
		require.NoError(t, err)
	})
}

func TestLoadDotEnv_PrefersCurrentDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(origWD))
	})

	configHome := filepath.Join(tmpDir, "config-home")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	restoreEnv(t, "CREDIMI_RUNNER_TEST_LOCAL_ONLY")
	restoreEnv(t, "CREDIMI_RUNNER_TEST_CONFIG_ONLY")
	restoreEnv(t, "CREDIMI_RUNNER_TEST_SHARED_VALUE")

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".env"), []byte("CREDIMI_RUNNER_TEST_LOCAL_ONLY=1\nCREDIMI_RUNNER_TEST_SHARED_VALUE=local\n"), 0o600))
	configDir := filepath.Join(configHome, "credimi", "runner")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, ".env"), []byte("CREDIMI_RUNNER_TEST_CONFIG_ONLY=1\nCREDIMI_RUNNER_TEST_SHARED_VALUE=config\n"), 0o600))

	path, err := loadDotEnv()
	require.NoError(t, err)
	require.Equal(t, ".env", path)
	require.Equal(t, "1", os.Getenv("CREDIMI_RUNNER_TEST_LOCAL_ONLY"))
	require.Empty(t, os.Getenv("CREDIMI_RUNNER_TEST_CONFIG_ONLY"))
	require.Equal(t, "local", os.Getenv("CREDIMI_RUNNER_TEST_SHARED_VALUE"))
}

func TestLoadDotEnv_FallsBackToUserConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(origWD))
	})

	configHome := filepath.Join(tmpDir, "config-home")
	configDir := filepath.Join(configHome, "credimi", "runner")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	configPath := filepath.Join(configDir, ".env")
	require.NoError(t, os.WriteFile(configPath, []byte("CREDIMI_RUNNER_TEST_CONFIG_ONLY=1\n"), 0o600))

	t.Setenv("XDG_CONFIG_HOME", configHome)
	restoreEnv(t, "CREDIMI_RUNNER_TEST_CONFIG_ONLY")

	path, err := loadDotEnv()
	require.NoError(t, err)
	require.Equal(t, configPath, path)
	require.Equal(t, "1", os.Getenv("CREDIMI_RUNNER_TEST_CONFIG_ONLY"))
}

func TestLoadDotEnv_UsesExplicitRunnerConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(origWD))
	})

	configDir := filepath.Join(tmpDir, "runner-config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	configPath := filepath.Join(configDir, ".env")
	require.NoError(t, os.WriteFile(configPath, []byte("CREDIMI_RUNNER_TEST_CONFIG_ONLY=1\n"), 0o600))

	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", configDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "ignored-config-home"))
	restoreEnv(t, "CREDIMI_RUNNER_TEST_CONFIG_ONLY")

	path, err := loadDotEnv()
	require.NoError(t, err)
	require.Equal(t, configPath, path)
	require.Equal(t, "1", os.Getenv("CREDIMI_RUNNER_TEST_CONFIG_ONLY"))
}

func TestLoadDotEnv_NoFileAvailable(t *testing.T) {
	tmpDir := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(origWD))
	})

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "missing-config-home"))
	restoreEnv(t, "CREDIMI_RUNNER_TEST_CONFIG_ONLY")

	path, err := loadDotEnv()
	require.NoError(t, err)
	require.Empty(t, path)
	require.Empty(t, os.Getenv("CREDIMI_RUNNER_TEST_CONFIG_ONLY"))
}

func TestValidateRequiredRuntimeEnv_WithRunnerID(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_ID", "runner-1")

	require.NoError(t, validateRequiredRuntimeEnv())
}

func TestValidateRequiredRuntimeEnv_MissingRunnerID(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("CREDIMI_RUNNER_ID", "")

	err := validateRequiredRuntimeEnv()
	require.EqualError(t, err, "CREDIMI_RUNNER_ID is required; set it in .env, "+filepath.Join(tmpDir, "credimi", "runner", ".env")+", or the process environment")
}
