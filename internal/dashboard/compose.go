package dashboard

import (
	"os"
	"strings"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

const (
	defaultPhoneImage      = dashboardruntime.DefaultPhoneImage
	defaultEmulatorImage   = dashboardruntime.DefaultEmulatorImage
	defaultAndroidWiFiPort = dashboardruntime.DefaultWiFiPort
)

func normalizeWizardValues(vals map[string]string) {
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(vals), currentGOOS())
	if err != nil {
		return
	}
	for key, value := range normalized {
		vals[key] = value
	}
}

func WriteComposeFile(dir string, vals map[string]string) error {
	return dashboardruntime.WriteComposeFile(dir, dashboardruntime.Values(vals))
}

func ComposeServices(vals map[string]string) []string {
	normalized, err := dashboardruntime.NormalizeValues(dashboardruntime.Values(vals), currentGOOS())
	if err != nil {
		return nil
	}
	return dashboardruntime.BuildRuntimePlan("", normalized).ComposeServices
}

func runnerConnectivityBlock(vals map[string]string) string {
	block := `    expose:
      - "${RUNNER_PORT:-` + dashboardruntime.DefaultRunnerPort + `}"`
	if valDefault(vals, "CREDIMI_SERVICE_MODE", "auto") == "manual" {
		block += `
    ports:
	      - "${RUNNER_PORT:-` + dashboardruntime.DefaultRunnerPort + `}:${RUNNER_PORT:-` + dashboardruntime.DefaultRunnerPort + `}"`
		return block
	}
	return block + `
    ports:
      - "127.0.0.1:${RUNNER_PORT:-` + dashboardruntime.DefaultRunnerPort + `}:${RUNNER_PORT:-` + dashboardruntime.DefaultRunnerPort + `}"`
}

func caddyNetworkBlock(vals map[string]string) string {
	if hostNetworkForTunnel(vals) {
		return "    network_mode: host\n"
	}
	return "    networks:\n      - ingress\n"
}

func tunnelNetworkBlock(vals map[string]string) string {
	if hostNetworkForTunnel(vals) {
		return "    network_mode: host\n"
	}
	return "    depends_on:\n      - caddy\n    networks:\n      - ingress\n"
}

func tunnelURL(vals map[string]string) string {
	if hostNetworkForTunnel(vals) {
		return "http://127.0.0.1:80"
	}
	return "http://caddy:80"
}

func hostNetworkForTunnel(vals map[string]string) bool {
	return false
}

func val(vals map[string]string, key string) string {
	return strings.TrimSpace(vals[key])
}

func valDefault(vals map[string]string, key, fallback string) string {
	if value := strings.TrimSpace(vals[key]); value != "" {
		return value
	}
	return fallback
}

func defaultIfEmpty(vals map[string]string, key, fallback string) {
	if strings.TrimSpace(vals[key]) == "" {
		vals[key] = fallback
	}
}

func containerMode(backend, mode string) string {
	if backend == dashboardruntime.DefaultHostBackend {
		return ""
	}
	return mode
}

func homeDir() string {
	dir, _ := os.UserHomeDir()
	return dir
}
