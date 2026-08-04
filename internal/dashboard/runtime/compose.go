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
	mode := values["CREDIMI_DEVICE_1_MODE"]
	image := defaultIfEmpty(values["CREDIMI_DEVICE_1_RUNNER_IMAGE"], DefaultPhoneImage)
	pullPolicy := defaultIfEmpty(values["CREDIMI_DEVICE_1_RUNNER_IMAGE_PULL_POLICY"], DefaultRunnerImagePullPolicy)
	networkMode := runnerNetworkMode(mode, goos)
	fmt.Fprintf(builder, "  runner:\n    image: %s\n    pull_policy: %s\n    restart: \"no\"\n", image, pullPolicy)
	switch mode {
	case "wifi":
		fmt.Fprintf(builder, "    command:\n      - \"${CREDIMI_DEVICE_1_WIFI_IP}:${CREDIMI_DEVICE_1_WIFI_PORT:-%s}\"\n", DefaultWiFiPort)
	case "emulator":
		builder.WriteString("    command:\n      - --emulator\n")
	case "no_device":
		builder.WriteString("    command:\n      - --no-device\n")
	default:
		builder.WriteString("    command:\n      - --host-adb\n      - --usb\n")
	}
	builder.WriteString("    env_file:\n      - .env\n")
	fmt.Fprintf(builder, "    environment:\n      PORT: \"${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort)
	if mode == "usb" && networkMode != "host" {
		builder.WriteString("      ADB_SERVER_SOCKET: \"${ADB_SERVER_SOCKET:-tcp:host.docker.internal:5037}\"\n")
	}
	switch mode {
	case "emulator":
		builder.WriteString("      CREDIMI_RUNNER_CONFIG_DIR: /app\n")
		builder.WriteString("    devices:\n      - /dev/kvm:/dev/kvm\n")
		builder.WriteString("    volumes:\n      - ${CREDIMI_DEVICE_1_ANDROID_KEYS_DIR}:/root/.android\n      - ${CREDIMI_DEVICE_1_HOST_AVD_HOME_PATH}:/avd-home\n      - ${CREDIMI_DEVICE_1_HOST_AVD_GOLDEN_PATH}:/avd-golden\n")
	case "no_device":
		if values["CREDIMI_DEVICE_1_AVDCTL_SSH_TARGET"] != "" && values["CREDIMI_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH"] != "" {
			builder.WriteString("    volumes:\n      - ${CREDIMI_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH}:/root/.ssh/known_hosts:ro\n")
		}
	case "usb":
		builder.WriteString("    volumes:\n      - adbkeys:/root/.android\n")
		if goos == "linux" && networkMode != "host" {
			builder.WriteString("    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n")
		}
	}
	if networkMode == "host" {
		builder.WriteString("    network_mode: host\n")
	} else {
		builder.WriteString("    expose:\n")
		fmt.Fprintf(builder, "      - \"${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort)
		builder.WriteString("    ports:\n")
		if normalizeServiceMode(values["CREDIMI_SERVICE_MODE"]) == "manual" {
			fmt.Fprintf(builder, "      - \"${RUNNER_PORT:-%s}:${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort, DefaultRunnerPort)
		} else {
			fmt.Fprintf(builder, "      - \"127.0.0.1:${RUNNER_PORT:-%s}:${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort, DefaultRunnerPort)
		}
	}
	builder.WriteString("    labels:\n      caddy: \"${RUNNER_CADDY_SITE:-:80}\"\n")
	writeControllerLabels(builder)
	if networkMode == "host" {
		fmt.Fprintf(builder, "      caddy.reverse_proxy: \"host.docker.internal:${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort)
	} else {
		fmt.Fprintf(builder, "      caddy.reverse_proxy: \"{{upstreams ${RUNNER_PORT:-%s}}}\"\n", DefaultRunnerPort)
	}
	if networkMode != "host" {
		builder.WriteString("    networks:\n      - ingress\n")
	}
}

func runnerNetworkMode(mode, goos string) string {
	// A host ADB server normally listens only on 127.0.0.1. On Linux, a
	// bridge-network container reaches the host through its gateway instead,
	// where that loopback-only server is unavailable. Keep USB host-ADB in the
	// host network namespace so it uses the same local ADB socket as the host.
	if goos == "linux" && mode == "usb" {
		return "host"
	}
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
	builder.WriteString("    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n    networks:\n      - ingress\n")
}

func writeTunnelService(builder *strings.Builder, values Values, goos string) {
	builder.WriteString(`
  tunnel:
    image: cloudflare/cloudflared:latest
    restart: "no"
    command: tunnel --no-autoupdate --url ${CREDIMI_TUNNEL_URL:-`)
	builder.WriteString("http://caddy:80")
	builder.WriteString("}\n")
	builder.WriteString("    labels:\n      io.credimi.runner.managed: \"true\"\n      io.credimi.runner.project: \"${CREDIMI_COMPOSE_PROJECT:-credimi-runner}\"\n      io.credimi.runner.config-fingerprint: \"${CREDIMI_CONFIG_FINGERPRINT:-unknown}\"\n")
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
