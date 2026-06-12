package dashboard

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	defaultPhoneImage      = "ghcr.io/forkbombeu/credimi-runner-phone:latest"
	defaultEmulatorImage   = "ghcr.io/forkbombeu/credimi-runner-emulator:latest"
	defaultAndroidWiFiPort = "5555"
)

func normalizeWizardValues(vals map[string]string) {
	runnerType := val(vals, "CREDIMI_RUNNER_TYPE")
	backend := val(vals, "CREDIMI_RUNNER_BACKEND")
	if backend == "" {
		backend = "container"
		vals["CREDIMI_RUNNER_BACKEND"] = backend
	}
	if val(vals, "RUNNER_PORT") == "" {
		vals["RUNNER_PORT"] = "8050"
	}
	if val(vals, "RUNNER_HOST") == "" {
		vals["RUNNER_HOST"] = "0.0.0.0"
	}
	if val(vals, "CREDIMI_RUNNER_WIFI_PORT") == "" {
		vals["CREDIMI_RUNNER_WIFI_PORT"] = defaultAndroidWiFiPort
	}

	switch runnerType {
	case "android_emulator":
		vals["CREDIMI_RUNNER_SERIAL"] = ""
		vals["CREDIMI_CONTAINER_MODE"] = containerMode(backend, "emulator")
		defaultIfEmpty(vals, "RUNNER_IMAGE", defaultEmulatorImage)
		defaultIfEmpty(vals, "ANDROID_KEYS_DIR", filepath.Join(homeDir(), ".android"))
		defaultIfEmpty(vals, "HOST_AVD_HOME_PATH", filepath.Join(homeDir(), ".android", "avd"))
		defaultIfEmpty(vals, "HOST_AVD_GOLDEN_PATH", filepath.Join(homeDir(), "avd-golden"))
		defaultIfEmpty(vals, "BASE_NAME", "credimi")
		defaultIfEmpty(vals, "GOLDEN_PATH", "/avd-golden/credimi-golden")
	case "ios_simulator":
		vals["CREDIMI_RUNNER_SERIAL"] = ""
		vals["CREDIMI_CONTAINER_MODE"] = containerMode(backend, "no_device")
		defaultIfEmpty(vals, "RUNNER_IMAGE", defaultPhoneImage)
		defaultIfEmpty(vals, "BASE_NAME", "credimi")
	case "ios_phone":
		vals["CREDIMI_CONTAINER_MODE"] = containerMode(backend, "no_device")
		defaultIfEmpty(vals, "RUNNER_IMAGE", defaultPhoneImage)
	case "redroid":
		vals["CREDIMI_RUNNER_DEVICE_MODE"] = "no_device"
		vals["CREDIMI_CONTAINER_MODE"] = containerMode(backend, "no_device")
		defaultIfEmpty(vals, "RUNNER_IMAGE", defaultPhoneImage)
		defaultIfEmpty(vals, "REDROID_DATA_DIR", "/home/credimi/redroid-data")
		defaultIfEmpty(vals, "REDROID_DATA_TAR", "/home/credimi/redroid-data.tar")
	default:
		mode := val(vals, "CREDIMI_RUNNER_DEVICE_MODE")
		if mode == "" {
			mode = "usb"
			vals["CREDIMI_RUNNER_DEVICE_MODE"] = mode
		}
		defaultIfEmpty(vals, "RUNNER_IMAGE", defaultPhoneImage)
		switch mode {
		case "wifi":
			vals["CREDIMI_CONTAINER_MODE"] = containerMode(backend, "wifi")
			if ip := val(vals, "CREDIMI_RUNNER_WIFI_IP"); ip != "" {
				vals["CREDIMI_RUNNER_SERIAL"] = ip + ":" + val(vals, "CREDIMI_RUNNER_WIFI_PORT")
			}
		default:
			vals["CREDIMI_CONTAINER_MODE"] = containerMode(backend, "usb")
			vals["CREDIMI_RUNNER_WIFI_IP"] = ""
			vals["CREDIMI_RUNNER_WIFI_PORT"] = ""
		}
	}
}

func WriteComposeFile(dir string, vals map[string]string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	normalizeWizardValues(vals)

	var b strings.Builder
	mode := valDefault(vals, "CREDIMI_CONTAINER_MODE", "usb")
	image := valDefault(vals, "RUNNER_IMAGE", defaultPhoneImage)
	runnerConnectivity := runnerConnectivityBlock(vals)

	b.WriteString("services:\n")
	switch mode {
	case "wifi":
		fmt.Fprintf(&b, `  runner:
    image: %s
    restart: unless-stopped
    command:
      - "${CREDIMI_RUNNER_WIFI_IP}:${CREDIMI_RUNNER_WIFI_PORT:-%s}"
    env_file:
      - .env
    environment:
      PORT: "${RUNNER_PORT:-8050}"
    volumes:
      - adbkeys:/root/.android
    network_mode: host
    labels:
      caddy: "${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "127.0.0.1:${RUNNER_PORT:-8050}"
`, image, defaultAndroidWiFiPort)
	case "emulator":
		fmt.Fprintf(&b, `  runner:
    image: %s
    restart: unless-stopped
    command:
      - --emulator
    env_file:
      - .env
    environment:
      PORT: "${RUNNER_PORT:-8050}"
      BASE_NAME: "${BASE_NAME:-credimi}"
      GOLDEN_PATH: "${GOLDEN_PATH:-/avd-golden/${BASE_NAME:-credimi}-golden}"
    devices:
      - /dev/kvm:/dev/kvm
    volumes:
      - ${ANDROID_KEYS_DIR}:/root/.android
      - ${HOST_AVD_HOME_PATH}:/avd-home
      - ${HOST_AVD_GOLDEN_PATH}:/avd-golden
%s
`, image, runnerConnectivity)
	case "no_device":
		fmt.Fprintf(&b, `  runner:
    image: %s
    restart: unless-stopped
    command:
      - --no-device
    env_file:
      - .env
    environment:
      PORT: "${RUNNER_PORT:-8050}"
%s
`, image, runnerConnectivity)
	default:
		fmt.Fprintf(&b, `  runner:
    image: %s
    restart: unless-stopped
    command:
      - --host-adb
      - --usb
    env_file:
      - .env
    environment:
      PORT: "${RUNNER_PORT:-8050}"
      ADB_SERVER_SOCKET: "${ADB_SERVER_SOCKET:-tcp:127.0.0.1:5037}"
    volumes:
      - adbkeys:/root/.android
    network_mode: host
    labels:
      caddy: "${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "127.0.0.1:${RUNNER_PORT:-8050}"
`, image)
	}

	b.WriteString(`
  runner_host:
    image: alpine:3.21
    restart: unless-stopped
    command:
      - /bin/sh
      - -c
      - "trap : TERM INT; while true; do sleep 3600; done"
    extra_hosts:
      - "host.docker.internal:host-gateway"
    labels:
      caddy: "${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "host.docker.internal:${RUNNER_PORT:-8050}"
    networks:
      - ingress

  caddy:
    image: lucaslorentz/caddy-docker-proxy:2.9-alpine
    restart: unless-stopped
    environment:
      CADDY_INGRESS_NETWORKS: ${CADDY_INGRESS_NETWORKS:-credimi-runner-ingress}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - caddy_data:/data
      - caddy_config:/config
`)
	b.WriteString(caddyNetworkBlock(vals))
	b.WriteString(`

  tunnel:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    command: tunnel --no-autoupdate --url ${CREDIMI_TUNNEL_URL:-`)
	b.WriteString(tunnelURL(vals))
	b.WriteString(`}
`)
	b.WriteString(tunnelNetworkBlock(vals))
	b.WriteString(`

  tunnel_named:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    command: tunnel --no-autoupdate run
    environment:
      TUNNEL_TOKEN: ${CLOUDFLARE_TUNNEL_TOKEN:-}
    depends_on:
      - caddy
    networks:
      - ingress

networks:
  ingress:
    name: ${CADDY_INGRESS_NETWORKS:-credimi-runner-ingress}

volumes:
  adbkeys:
  caddy_data:
  caddy_config:
`)

	path := filepath.Join(dir, "docker-compose.yaml")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ComposeServices(vals map[string]string) []string {
	backend := valDefault(vals, "CREDIMI_RUNNER_BACKEND", "container")
	mode := valDefault(vals, "CREDIMI_SERVICE_MODE", "auto")
	var services []string
	if backend == "host" {
		services = []string{"runner_host", "caddy"}
	} else {
		services = []string{"runner", "caddy"}
	}
	switch mode {
	case "auto":
		return append(services, "tunnel")
	case "cloudflare-managed":
		return append(services, "tunnel_named")
	case "manual":
		if backend == "host" {
			return nil
		}
		return []string{"runner"}
	default:
		return services
	}
}

func runnerConnectivityBlock(vals map[string]string) string {
	if valDefault(vals, "CREDIMI_RUNNER_BACKEND", "container") == "container" &&
		valDefault(vals, "CREDIMI_SERVICE_MODE", "auto") == "manual" &&
		runtime.GOOS == "linux" {
		return "    network_mode: host"
	}
	return `    expose:
      - "8050"
    labels:
      caddy: "${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "{{upstreams 8050}}"
    networks:
      - ingress`
}

func caddyNetworkBlock(vals map[string]string) string {
	if hostNetworkForTunnel(vals) {
		return `    network_mode: host`
	}
	return `    extra_hosts:
      - "host.docker.internal:host-gateway"
    networks:
      - ingress`
}

func tunnelNetworkBlock(vals map[string]string) string {
	if hostNetworkForTunnel(vals) {
		return `    network_mode: host`
	}
	return `    extra_hosts:
      - "host.docker.internal:host-gateway"
    networks:
      - ingress`
}

func tunnelURL(vals map[string]string) string {
	if hostNetworkForTunnel(vals) {
		return "http://127.0.0.1:80"
	}
	return "http://caddy:80"
}

func hostNetworkForTunnel(vals map[string]string) bool {
	return runtime.GOOS == "linux" &&
		valDefault(vals, "CREDIMI_RUNNER_BACKEND", "container") == "container" &&
		valDefault(vals, "CREDIMI_SERVICE_MODE", "auto") == "auto" &&
		(valDefault(vals, "CREDIMI_CONTAINER_MODE", "usb") == "usb" || val(vals, "CREDIMI_CONTAINER_MODE") == "wifi")
}

func containerMode(backend, mode string) string {
	if backend == "host" {
		return ""
	}
	return mode
}

func val(vals map[string]string, key string) string {
	return strings.TrimSpace(vals[key])
}

func valDefault(vals map[string]string, key, fallback string) string {
	if v := val(vals, key); v != "" {
		return v
	}
	return fallback
}

func defaultIfEmpty(vals map[string]string, key, value string) {
	if val(vals, key) == "" {
		vals[key] = value
	}
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
