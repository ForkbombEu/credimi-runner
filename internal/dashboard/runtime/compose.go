package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func WriteComposeFile(dir string, values Values) error {
	return WriteComposeFileForOS(dir, values, runtime.GOOS)
}

func WriteComposeFileForOS(dir string, values Values, goos string) error {
	values = cloneValues(values)
	plan := BuildRuntimePlanForOS(dir, values, goos)
	values["CREDIMI_COMPOSE_PROJECT"] = plan.ComposeProject
	values["CREDIMI_CONFIG_FINGERPRINT"] = plan.ConfigFingerprint
	values["CREDIMI_CONFIG_DIR"] = dir
	content, err := ComposeYAML(values, goos)
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
	plan := BuildRuntimePlanForOS("", normalized, goos)
	spec, err := sharedRunnerSpec(normalized, goos)
	if err != nil {
		// Host runners do not use the generated runner service; they retain a
		// Compose file only for Caddy/tunnel services.
		if plan.Backend != DefaultHostBackend {
			return "", err
		}
		spec = sharedRunnerRuntime{Image: normalized["ANDROID_RUNNER_IMAGE"], PullPolicy: normalized["ANDROID_PULL_POLICY"], NetworkMode: "bridge"}
	}

	var builder strings.Builder
	// A Linux USB runner uses the host network so it can talk to the host ADB
	// daemon. Caddy must use that namespace too: reaching the host through
	// host.docker.internal resolves to Docker's bridge gateway, which is not a
	// reliable route back to a host-networked runner on every Docker setup.
	caddyOnHost := spec.NetworkMode == "host" || plan.Backend == DefaultHostBackend
	builder.WriteString("services:\n")
	if plan.Backend != DefaultHostBackend {
		writeRunnerService(&builder, goos, normalizeServiceMode(normalized["CREDIMI_SERVICE_MODE"]), spec, caddyOnHost, normalized["CREDIMI_CONFIG_DIR"], normalized["DASHBOARD_PORT"])
	}
	writeCaddyService(&builder, caddyOnHost, plan.Backend == DefaultHostBackend)
	writeTunnelService(&builder, caddyOnHost)
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

func writeRunnerService(builder *strings.Builder, goos, serviceMode string, spec sharedRunnerRuntime, caddyOnHost bool, configDir, dashboardPort string) {
	fmt.Fprintf(builder, "  runner:\n    image: %s\n    pull_policy: %s\n    restart: \"no\"\n", spec.Image, spec.PullPolicy)
	builder.WriteString("    command:\n      - internal-runtime\n")
	// Configuration is TOML and is mounted by the unified runner container;
	// Compose must not treat it as a dotenv file.
	fmt.Fprintf(builder, "    environment:\n      CREDIMI_RUNNER_CONFIG_DIR: /etc/credimi-runner\n      PORT: \"${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort)
	if spec.HasADB && spec.NetworkMode != "host" {
		builder.WriteString("      ADB_SERVER_SOCKET: \"${ADB_SERVER_SOCKET:-tcp:host.docker.internal:5037}\"\n")
	}
	if spec.HasKVM {
		builder.WriteString("    devices:\n      - /dev/kvm:/dev/kvm\n")
	} else if spec.HasUSB && goos == "linux" && spec.NetworkMode != "host" {
		builder.WriteString("    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n")
	}
	if strings.TrimSpace(configDir) == "" {
		configDir = "."
	}
	builder.WriteString("    volumes:\n")
	fmt.Fprintf(builder, "      - %s:/etc/credimi-runner\n", composePath(configDir))
	if spec.ADBKeysPath != "" {
		fmt.Fprintf(builder, "      - %s:/root/.android:ro\n", spec.ADBKeysPath)
	} else {
		builder.WriteString("      - adbkeys:/root/.android\n")
	}
	fmt.Fprintf(builder, "      - %s:/var/lib/credimi-runner\n      - %s:/opt/credimi-runner/tools\n      - %s:/opt/android-sdk\n", spec.StateVolume, spec.ToolCacheVolume, spec.SDKVolume)
	if spec.NetworkMode == "host" {
		builder.WriteString("    network_mode: host\n")
	} else {
		builder.WriteString("    expose:\n")
		fmt.Fprintf(builder, "      - \"${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort)
		builder.WriteString("    ports:\n")
		if serviceMode == "manual" {
			fmt.Fprintf(builder, "      - \"${RUNNER_PORT:-%s}:${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort, DefaultRunnerPort)
		} else {
			fmt.Fprintf(builder, "      - \"127.0.0.1:${RUNNER_PORT:-%s}:${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort, DefaultRunnerPort)
		}
		fmt.Fprintf(builder, "      - \"127.0.0.1:${DASHBOARD_PORT:-%s}:${DASHBOARD_PORT:-%s}\"\n", defaultIfEmpty(dashboardPort, DefaultDashboardPort), defaultIfEmpty(dashboardPort, DefaultDashboardPort))
	}
	builder.WriteString("    labels:\n      caddy: \"${RUNNER_CADDY_SITE:-:80}\"\n")
	writeControllerLabels(builder)
	if caddyOnHost && spec.NetworkMode == "host" {
		fmt.Fprintf(builder, "      caddy.reverse_proxy: \"127.0.0.1:${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort)
	} else if spec.NetworkMode == "host" {
		fmt.Fprintf(builder, "      caddy.reverse_proxy: \"host.docker.internal:${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort)
	} else {
		fmt.Fprintf(builder, "      caddy.reverse_proxy: \"{{upstreams ${RUNNER_PORT:-%s}}}\"\n", DefaultRunnerPort)
	}
	if spec.NetworkMode != "host" {
		builder.WriteString("    networks:\n      - ingress\n")
	}
}

type sharedRunnerRuntime struct {
	Image           string
	PullPolicy      string
	NetworkMode     string
	HasADB          bool
	HasUSB          bool
	HasEmulator     bool
	HasKVM          bool
	StateVolume     string
	ToolCacheVolume string
	SDKVolume       string
	ADBKeysPath     string
}

// SharedRunnerImage reports the actual image and pull policy of the one
// container that serves the complete device inventory.
func SharedRunnerImage(values Values, goos string) (image, pullPolicy string, err error) {
	spec, err := sharedRunnerSpec(values, goos)
	if err != nil {
		return "", "", err
	}
	return spec.Image, spec.PullPolicy, nil
}

func sharedRunnerSpec(values Values, goos string) (sharedRunnerRuntime, error) {
	return sharedRunnerSpecWithKVM(values, goos, hostKVMAvailable(goos))
}

var hostKVMAvailable = func(goos string) bool {
	if goos != "linux" {
		return false
	}
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

func sharedRunnerSpecWithKVM(values Values, goos string, kvmAvailable bool) (sharedRunnerRuntime, error) {
	inventory, err := ParseRuntimeConfig(values)
	if err != nil {
		if strings.TrimSpace(values["CREDIMI_DEVICE_COUNT"]) != "" {
			return sharedRunnerRuntime{}, err
		}
		inventory = RunnerRuntimeConfig{}
	}
	spec := sharedRunnerRuntime{Image: defaultIfEmpty(values["ANDROID_RUNNER_IMAGE"], DefaultAndroidRunnerImage), PullPolicy: defaultIfEmpty(values["ANDROID_PULL_POLICY"], DefaultAndroidPullPolicy), NetworkMode: "bridge", StateVolume: defaultIfEmpty(values["ANDROID_STATE_VOLUME"], "credimi-runner-state"), ToolCacheVolume: defaultIfEmpty(values["ANDROID_TOOL_CACHE_VOLUME"], "credimi-runner-tools"), SDKVolume: defaultIfEmpty(values["ANDROID_SDK_VOLUME"], "credimi-runner-sdk"), ADBKeysPath: values["ANDROID_ADB_KEYS_PATH"]}
	for _, device := range inventory.Devices {
		if !device.Enabled {
			continue
		}
		deviceType := device.Type
		if deviceType == "" {
			// Direct `serve` deployments need only an execution identifier. Keep
			// their minimal inventory usable by treating omitted setup metadata as
			// a physical Android target.
			deviceType = "android_phone"
		}
		deviceMode := device.Mode
		if deviceMode == "" {
			deviceMode = "usb"
		}
		if deviceType == "ios_simulator" {
			return sharedRunnerRuntime{}, fmt.Errorf("iOS simulators require the native backend")
		}
		if deviceType != "android_phone" && deviceType != "android_emulator" && deviceType != "redroid" {
			return sharedRunnerRuntime{}, fmt.Errorf("device %q has unsupported runner type %q", device.ID, deviceType)
		}
		if deviceMode != "no_device" && deviceType != "redroid" {
			spec.HasADB = true
		}
		if deviceMode == "usb" {
			spec.HasUSB = true
		}
		if deviceType == "android_emulator" {
			spec.HasEmulator = true
		}
	}
	if goos == "linux" && spec.HasUSB {
		spec.NetworkMode = "host"
	}
	spec.HasKVM = goos == "linux" && kvmAvailable
	return spec, nil
}

func composePath(path string) string {
	path = filepath.Clean(path)
	if strings.ContainsAny(path, " \t:\"") {
		return `"` + strings.ReplaceAll(path, `"`, `\\"`) + `"`
	}
	return path
}

func writeCaddyService(builder *strings.Builder, caddyOnHost, native bool) {
	if native {
		builder.WriteString(`
  caddy:
    image: caddy:2.9-alpine
    restart: "no"
    command:
      - caddy
      - reverse-proxy
      - --from
      - "${RUNNER_CADDY_SITE:-:80}"
      - --to
      - "host.docker.internal:${RUNNER_PORT:-8050}"
    extra_hosts:
      - "host.docker.internal:host-gateway"
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:2019/config/"]
      interval: 2s
      timeout: 2s
      retries: 30
      start_period: 2s
    networks:
      - ingress
`)
		return
	}
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
    healthcheck:
      # Caddy's process may be running before docker-proxy has built the
      # reverse-proxy route from the runner labels. The quick tunnel must not
      # publish a URL until that route exists.
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:2019/config/ | grep -q reverse_proxy"]
      interval: 2s
      timeout: 2s
      retries: 30
      start_period: 2s
`)
	if caddyOnHost {
		builder.WriteString("    network_mode: host\n")
		return
	}
	builder.WriteString("    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n    networks:\n      - ingress\n")
}

func writeTunnelService(builder *strings.Builder, caddyOnHost bool) {
	builder.WriteString(`
  tunnel:
    image: cloudflare/cloudflared:latest
    restart: "no"
    command: tunnel --no-autoupdate --url ${CREDIMI_TUNNEL_URL:-`)
	if caddyOnHost {
		builder.WriteString("http://127.0.0.1:80")
	} else {
		builder.WriteString("http://caddy:80")
	}
	builder.WriteString("}\n")
	builder.WriteString("    labels:\n      io.credimi.runner.managed: \"true\"\n      io.credimi.runner.project: \"${CREDIMI_COMPOSE_PROJECT:-credimi-runner}\"\n      io.credimi.runner.config-fingerprint: \"${CREDIMI_CONFIG_FINGERPRINT:-unknown}\"\n")
	builder.WriteString("    depends_on:\n      - caddy\n")
	if caddyOnHost {
		builder.WriteString("    network_mode: host\n")
		return
	}
	builder.WriteString("    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n    networks:\n      - ingress\n")
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
      caddy:
        condition: service_healthy
    networks:
      - ingress
`)
}

func writeControllerLabels(builder *strings.Builder) {
	builder.WriteString("      io.credimi.runner.managed: \"true\"\n      io.credimi.runner.project: \"${CREDIMI_COMPOSE_PROJECT:-credimi-runner}\"\n      io.credimi.runner.config-fingerprint: \"${CREDIMI_CONFIG_FINGERPRINT:-unknown}\"\n")
}
