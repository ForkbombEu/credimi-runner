package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func WriteComposeFile(dir string, values Values) error {
	values = cloneValues(values)
	plan := BuildRuntimePlan(dir, values)
	values["CREDIMI_COMPOSE_PROJECT"] = plan.ComposeProject
	values["CREDIMI_CONFIG_FINGERPRINT"] = plan.ConfigFingerprint
	content, err := ComposeYAML(values, runtime.GOOS)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmpPath := filepath.Join(dir, "docker-compose.yaml.tmp")
	if err := os.WriteFile(tmpPath, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(dir, "docker-compose.yaml"))
}

func ComposeYAML(values Values, goos string) (string, error) {
	normalized, err := NormalizeValues(values, goos)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	builder.WriteString("services:\n")
	writeRunnerService(&builder, normalized, goos)
	writeRunnerHostService(&builder)
	writeCaddyService(&builder, normalized, goos)
	writeTunnelService(&builder, normalized, goos)
	writeNamedTunnelService(&builder)
	builder.WriteString(`
networks:
  ingress:
    name: ${CADDY_INGRESS_NETWORKS:-credimi-runner-ingress}

volumes:
  adbkeys:
  caddy_data:
  caddy_config:
`)
	return builder.String(), nil
}

func writeRunnerService(builder *strings.Builder, values Values, goos string) {
	mode := values["CREDIMI_CONTAINER_MODE"]
	image := defaultIfEmpty(values["RUNNER_IMAGE"], DefaultPhoneImage)
	pullPolicy := defaultIfEmpty(values["RUNNER_IMAGE_PULL_POLICY"], DefaultRunnerImagePullPolicy)
	networkMode := runnerNetworkMode(values, goos)
	fmt.Fprintf(builder, "  runner:\n    image: %s\n    pull_policy: %s\n    restart: \"no\"\n", image, pullPolicy)
	switch mode {
	case "wifi":
		fmt.Fprintf(builder, "    command:\n      - \"${CREDIMI_RUNNER_WIFI_IP}:${CREDIMI_RUNNER_WIFI_PORT:-%s}\"\n", DefaultWiFiPort)
	case "emulator":
		builder.WriteString("    command:\n      - --emulator\n")
	case "no_device":
		builder.WriteString("    command:\n      - --no-device\n")
	default:
		builder.WriteString("    command:\n      - --host-adb\n      - --usb\n")
	}
	builder.WriteString("    env_file:\n      - .env\n")
	fmt.Fprintf(builder, "    environment:\n      PORT: \"${RUNNER_PORT:-%s}\"\n      ANDROID_SERIAL: \"${CREDIMI_RUNNER_SERIAL:-}\"\n", DefaultRunnerPort)
	switch mode {
	case "emulator":
		builder.WriteString("      BASE_NAME: \"${BASE_NAME:-credimi}\"\n")
		builder.WriteString("      GOLDEN_PATH: \"${GOLDEN_PATH:-/avd-golden/credimi-golden}\"\n")
		builder.WriteString("    devices:\n      - /dev/kvm:/dev/kvm\n")
		builder.WriteString("    volumes:\n      - ${ANDROID_KEYS_DIR}:/root/.android\n      - ${HOST_AVD_HOME_PATH}:/avd-home\n      - ${HOST_AVD_GOLDEN_PATH}:/avd-golden\n")
	case "no_device":
		if values["AVDCTL_SSH_TARGET"] != "" && values["AVDCTL_SSH_KNOWN_HOSTS_PATH"] != "" {
			builder.WriteString("    volumes:\n      - ${AVDCTL_SSH_KNOWN_HOSTS_PATH}:/root/.ssh/known_hosts:ro\n")
		}
	case "usb":
		builder.WriteString("      ADB_SERVER_SOCKET: \"${ADB_SERVER_SOCKET:-tcp:host.docker.internal:5037}\"\n")
		builder.WriteString("    volumes:\n      - adbkeys:/root/.android\n")
		if goos == "linux" {
			builder.WriteString("    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n")
		}
	}
	if networkMode == "host" {
		builder.WriteString("    network_mode: host\n")
	} else {
		builder.WriteString("    expose:\n")
		fmt.Fprintf(builder, "      - \"${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort)
		if normalizeServiceMode(values["CREDIMI_SERVICE_MODE"]) == "manual" {
			builder.WriteString("    ports:\n")
			fmt.Fprintf(builder, "      - \"127.0.0.1:${RUNNER_PORT:-%s}:${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort, DefaultRunnerPort)
		}
	}
	builder.WriteString("    labels:\n      caddy: \"${RUNNER_CADDY_SITE:-:80}\"\n")
	writeControllerLabels(builder)
	if networkMode == "host" {
		fmt.Fprintf(builder, "      caddy.reverse_proxy: \"127.0.0.1:${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort)
	} else {
		fmt.Fprintf(builder, "      caddy.reverse_proxy: \"{{upstreams ${RUNNER_PORT:-%s}}}\"\n", DefaultRunnerPort)
	}
	if networkMode != "host" {
		builder.WriteString("    networks:\n      - ingress\n")
	}
}

func runnerNetworkMode(values Values, goos string) string {
	return "bridge"
}

func writeRunnerHostService(builder *strings.Builder) {
	builder.WriteString(`
  runner_host:
    image: alpine:3.21
    restart: "no"
    command:
      - /bin/sh
      - -c
      - "trap : TERM INT; while true; do sleep 3600; done"
    extra_hosts:
      - "host.docker.internal:host-gateway"
    labels:
      caddy: "${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "host.docker.internal:${RUNNER_PORT:-` + DefaultRunnerPort + `}"
      io.credimi.runner.managed: "true"
      io.credimi.runner.project: "${CREDIMI_COMPOSE_PROJECT:-credimi-runner}"
      io.credimi.runner.config-fingerprint: "${CREDIMI_CONFIG_FINGERPRINT:-unknown}"
    networks:
      - ingress
`)
}

func writeCaddyService(builder *strings.Builder, values Values, goos string) {
	builder.WriteString(`
  caddy:
    image: lucaslorentz/caddy-docker-proxy:2.9-alpine
    restart: "no"
    environment:
      CADDY_INGRESS_NETWORKS: ${CADDY_INGRESS_NETWORKS:-credimi-runner-ingress}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - caddy_data:/data
      - caddy_config:/config
    labels:
      io.credimi.runner.managed: "true"
      io.credimi.runner.project: "${CREDIMI_COMPOSE_PROJECT:-credimi-runner}"
      io.credimi.runner.config-fingerprint: "${CREDIMI_CONFIG_FINGERPRINT:-unknown}"
`)
	if normalizeServiceMode(values["CREDIMI_SERVICE_MODE"]) == "auto" && runnerNetworkMode(values, goos) == "host" {
		builder.WriteString("    network_mode: host\n")
		return
	}
	builder.WriteString("    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n    networks:\n      - ingress\n")
}

func writeTunnelService(builder *strings.Builder, values Values, goos string) {
	builder.WriteString(`
  tunnel:
    image: cloudflare/cloudflared:latest
    restart: "no"
    command: tunnel --no-autoupdate --url ${CREDIMI_TUNNEL_URL:-`)
	if normalizeServiceMode(values["CREDIMI_SERVICE_MODE"]) == "auto" && runnerNetworkMode(values, goos) == "host" {
		builder.WriteString("http://127.0.0.1:80")
	} else {
		builder.WriteString("http://caddy:80")
	}
	builder.WriteString("}\n")
	builder.WriteString("    labels:\n      io.credimi.runner.managed: \"true\"\n      io.credimi.runner.project: \"${CREDIMI_COMPOSE_PROJECT:-credimi-runner}\"\n      io.credimi.runner.config-fingerprint: \"${CREDIMI_CONFIG_FINGERPRINT:-unknown}\"\n")
	if normalizeServiceMode(values["CREDIMI_SERVICE_MODE"]) == "auto" && runnerNetworkMode(values, goos) == "host" {
		builder.WriteString("    network_mode: host\n")
		return
	}
	builder.WriteString("    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n    depends_on:\n      - caddy\n    networks:\n      - ingress\n")
}

func writeNamedTunnelService(builder *strings.Builder) {
	builder.WriteString(`
  tunnel_named:
    image: cloudflare/cloudflared:latest
    restart: "no"
    command: tunnel --no-autoupdate run
    environment:
      TUNNEL_TOKEN: ${CLOUDFLARE_TUNNEL_TOKEN:-}
    labels:
      io.credimi.runner.managed: "true"
      io.credimi.runner.project: "${CREDIMI_COMPOSE_PROJECT:-credimi-runner}"
      io.credimi.runner.config-fingerprint: "${CREDIMI_CONFIG_FINGERPRINT:-unknown}"
    depends_on:
      - caddy
    networks:
      - ingress
`)
}

func writeControllerLabels(builder *strings.Builder) {
	builder.WriteString("      io.credimi.runner.managed: \"true\"\n      io.credimi.runner.project: \"${CREDIMI_COMPOSE_PROJECT:-credimi-runner}\"\n      io.credimi.runner.config-fingerprint: \"${CREDIMI_CONFIG_FINGERPRINT:-unknown}\"\n")
}
