package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func readDeploymentFile(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", name))
	require.NoError(t, err)
	return string(contents)
}

func TestCoolifyComposeUsesTheUnifiedLinuxEmulatorTopology(t *testing.T) {
	compose := readDeploymentFile(t, "docker-compose.coolify.yaml")
	for _, want := range []string{
		"runner_emulator:",
		"image: ghcr.io/forkbombeu/credimi-runner:latest",
		"platform: linux/amd64",
		"- /dev/kvm:/dev/kvm",
		`source: "${HOST_AVD_HOME_PATH:-/srv/credimi/avd-home}"`,
		`source: "${HOST_AVD_GOLDEN_PATH:-/srv/credimi/avd-golden}"`,
		"target: /opt/android-sdk",
		"target: /var/lib/credimi-runner",
		"CREDIMI_RUNNER_CONFIG_DIR: /etc/credimi-runner",
		"ANDROID_AVD_HOME: /avd-home",
		"exec /usr/local/bin/credimi-runner internal-service",
	} {
		require.Contains(t, compose, want)
	}
	require.NotContains(t, compose, "credimi-runner-emulator")
	require.NotContains(t, compose, "ports:")
}

func TestCoolifyBootstrapWritesPrivateTypedSingleEmulatorConfig(t *testing.T) {
	compose := readDeploymentFile(t, "docker-compose.coolify.yaml")
	for _, want := range []string{
		"umask 077",
		"chmod 0600 /etc/credimi-runner/config.toml",
		"published = true",
		`auth_mode = "internal_admin"`,
		`api_listen = "0.0.0.0:%s"`,
		`dashboard_listen = "127.0.0.1:8051"`,
		"open_browser = false",
		`mode = "manual"`,
		"public_url = ",
		"[[devices]]",
		`type = "android_emulator"`,
		"headless = true",
		"mkdir -p /etc/credimi-runner /root/.android",
		"chmod 0600 /root/.android/adbkey",
		"chmod 0644 /root/.android/adbkey.pub",
		"toml_string()",
		"TOML values must not contain control characters",
	} {
		require.Contains(t, compose, want)
	}
	require.Equal(t, 1, strings.Count(compose, "[[devices]]"))
	require.NotContains(t, compose, "public_port")
	require.NotContains(t, compose, "CREDIMI_RUNNER_PUBLISHED")
	require.NotContains(t, compose, "CREDIMI_RUNNER_NAME")
	require.NotContains(t, compose, "CREDIMI_DEVICE_NAME")
	require.Contains(t, compose, `CREDIMI_DEVICE_ID: ${CREDIMI_DEVICE_ID:?}`)
	require.Contains(t, compose, `COOLIFY_URL: ${COOLIFY_URL:?an absolute execution API URL is required}`)
}

func TestCoolifyUserFacingEnvironmentContract(t *testing.T) {
	compose := readDeploymentFile(t, "docker-compose.coolify.yaml")
	references := regexp.MustCompile(`\$\{([A-Z0-9_]+)(?::[^}]*)?\}`).FindAllStringSubmatch(compose, -1)
	seen := make(map[string]bool)
	for _, reference := range references {
		seen[reference[1]] = true
	}
	want := []string{
		"CREDIMI_URL", "CREDIMI_INTERNAL_ADMIN_KEY", "TEMPORAL_ADDRESS",
		"OTEL_ENABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SERVICE_NAME",
		"BASE_NAME", "GOLDEN_PATH", "CREDIMI_RUNNER_ID", "CREDIMI_DEVICE_ID",
		"ADB_PRIVATE_KEY", "ADB_PUBLIC_KEY", "PORT", "HOST_AVD_HOME_PATH",
		"HOST_AVD_GOLDEN_PATH", "CPUSET", "CPUS", "COOLIFY_URL",
	}
	for _, name := range want {
		require.Truef(t, seen[name], "missing environment reference %s", name)
	}
	require.Len(t, seen, len(want), "unexpected deployment environment reference: %v", seen)
}

func TestCoolifyLocalWorkflowOnlyPublishesTheExecutionAPIPort(t *testing.T) {
	taskfile := readDeploymentFile(t, "Taskfile.yml")
	local := readDeploymentFile(t, "docker-compose.coolify.local.yaml")
	for _, task := range []string{
		`"docker:coolify:build-local":`, `"docker:coolify:up-local":`,
		`"docker:coolify:logs":`, `"docker:coolify:down":`,
	} {
		require.Contains(t, taskfile, task)
	}
	require.Contains(t, taskfile, "--detach --force-recreate --pull never runner_emulator")
	require.Contains(t, taskfile, "docker compose -p credimi-runner-coolify-local")
	require.Contains(t, taskfile, "Warning: successful startup will register/update")
	require.Contains(t, taskfile, "test -r /dev/kvm && test -w /dev/kvm")
	require.Contains(t, local, `- "${PORT:-8050}:${PORT:-8050}"`)
	require.NotContains(t, local, "8051")
	require.NotContains(t, local, "dashboard")
}
