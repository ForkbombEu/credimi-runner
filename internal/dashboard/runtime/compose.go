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
	spec, err := sharedRunnerSpec(normalized, goos)
	if err != nil {
		// Host runners do not use the generated runner service; they retain a
		// Compose file only for Caddy/tunnel services.
		if BuildRuntimePlan("", normalized).Backend != DefaultHostBackend {
			return "", err
		}
		spec = sharedRunnerRuntime{Image: DefaultPhoneImage, PullPolicy: DefaultRunnerImagePullPolicy, NetworkMode: "bridge"}
	}

	var builder strings.Builder
	// A Linux USB runner uses the host network so it can talk to the host ADB
	// daemon. Caddy must use that namespace too: reaching the host through
	// host.docker.internal resolves to Docker's bridge gateway, which is not a
	// reliable route back to a host-networked runner on every Docker setup.
	caddyOnHost := goos == "linux" && (spec.NetworkMode == "host" || BuildRuntimePlan("", normalized).Backend == DefaultHostBackend)
	builder.WriteString("services:\n")
	writeRunnerService(&builder, goos, normalizeServiceMode(normalized["CREDIMI_SERVICE_MODE"]), spec, caddyOnHost)
	writeRunnerHostService(&builder, caddyOnHost)
	writeCaddyService(&builder, caddyOnHost)
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

func writeRunnerService(builder *strings.Builder, goos, serviceMode string, spec sharedRunnerRuntime, caddyOnHost bool) {
	fmt.Fprintf(builder, "  runner:\n    image: %s\n    pull_policy: %s\n    restart: \"no\"\n", spec.Image, spec.PullPolicy)
	builder.WriteString("    command:\n      - --inventory\n")
	builder.WriteString("    env_file:\n      - .env\n")
	fmt.Fprintf(builder, "    environment:\n      PORT: \"${RUNNER_PORT:-%s}\"\n", DefaultRunnerPort)
	if spec.HasADB && spec.NetworkMode != "host" {
		builder.WriteString("      ADB_SERVER_SOCKET: \"${ADB_SERVER_SOCKET:-tcp:host.docker.internal:5037}\"\n")
	}
	if spec.HasEmulator {
		builder.WriteString("      CREDIMI_RUNNER_CONFIG_DIR: /app\n")
		builder.WriteString("    devices:\n      - /dev/kvm:/dev/kvm\n")
		fmt.Fprintf(builder, "    volumes:\n      - ${CREDIMI_DEVICE_%d_ANDROID_KEYS_DIR}:/root/.android\n      - ${CREDIMI_DEVICE_%d_HOST_AVD_HOME_PATH}:/avd-home\n      - ${CREDIMI_DEVICE_%d_HOST_AVD_GOLDEN_PATH}:/avd-golden\n", spec.EmulatorIndex, spec.EmulatorIndex, spec.EmulatorIndex)
	} else if spec.HasUSB {
		builder.WriteString("    volumes:\n      - adbkeys:/root/.android\n")
		if goos == "linux" && spec.NetworkMode != "host" {
			builder.WriteString("    extra_hosts:\n      - \"host.docker.internal:host-gateway\"\n")
		}
	}
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
	Image         string
	PullPolicy    string
	NetworkMode   string
	HasADB        bool
	HasUSB        bool
	HasEmulator   bool
	EmulatorIndex int
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
	inventory, err := ParseRuntimeConfig(values)
	if err != nil {
		return sharedRunnerRuntime{}, err
	}
	spec := sharedRunnerRuntime{Image: DefaultPhoneImage, PullPolicy: DefaultRunnerImagePullPolicy, NetworkMode: "bridge"}
	customImages := map[string]string{}
	customPolicies := map[string]string{}
	var avdHome, avdGolden, adbKeys string
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
			return sharedRunnerRuntime{}, fmt.Errorf("iOS simulators require the host runner backend; Docker runner containers cannot access CoreSimulator")
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
			if spec.EmulatorIndex == 0 {
				spec.EmulatorIndex = device.Index
				avdHome, avdGolden, adbKeys = device.Values["HOST_AVD_HOME_PATH"], device.Values["HOST_AVD_GOLDEN_PATH"], device.Values["ANDROID_KEYS_DIR"]
			} else if avdHome != device.Values["HOST_AVD_HOME_PATH"] || avdGolden != device.Values["HOST_AVD_GOLDEN_PATH"] || adbKeys != device.Values["ANDROID_KEYS_DIR"] {
				return sharedRunnerRuntime{}, fmt.Errorf("all Android emulators on one container runner must share Android keys and AVD root paths")
			}
		}
		image := strings.TrimSpace(device.Values["RUNNER_IMAGE"])
		defaultImage := DefaultPhoneImage
		if deviceType == "android_emulator" {
			defaultImage = DefaultEmulatorImage
		}
		policy := defaultIfEmpty(device.Values["RUNNER_IMAGE_PULL_POLICY"], DefaultRunnerImagePullPolicy)
		if image != "" && (image != defaultImage || policy != DefaultRunnerImagePullPolicy) {
			if existingPolicy, exists := customPolicies[image]; exists && existingPolicy != policy {
				return sharedRunnerRuntime{}, fmt.Errorf("a shared runner image must use one pull policy; configured device policies differ")
			}
			customImages[image] = device.ID
			customPolicies[image] = policy
		}
	}
	if len(customImages) > 1 {
		return sharedRunnerRuntime{}, fmt.Errorf("a shared runner container supports one runtime image; configured device image overrides differ")
	}
	if spec.HasEmulator {
		spec.Image = DefaultEmulatorImage
	}
	for image := range customImages {
		spec.Image = image
		spec.PullPolicy = customPolicies[image]
	}
	if goos == "linux" && spec.HasUSB {
		spec.NetworkMode = "host"
	}
	return spec, nil
}

func writeRunnerHostService(builder *strings.Builder, caddyOnHost bool) {
	upstreamHost := "host.docker.internal"
	if caddyOnHost {
		upstreamHost = "127.0.0.1"
	}
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
      caddy.reverse_proxy: "` + upstreamHost + `:${RUNNER_PORT:-` + DefaultRunnerPort + `}"
      io.credimi.runner.managed: "true"
      io.credimi.runner.project: "${CREDIMI_COMPOSE_PROJECT:-credimi-runner}"
      io.credimi.runner.config-fingerprint: "${CREDIMI_CONFIG_FINGERPRINT:-unknown}"
    networks:
      - ingress
`)
}

func writeCaddyService(builder *strings.Builder, caddyOnHost bool) {
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
		builder.WriteString("http://host.docker.internal:80")
	} else {
		builder.WriteString("http://caddy:80")
	}
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
