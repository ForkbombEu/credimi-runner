//go:build !darwin

package servicemanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/atomicfile"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	controller "github.com/forkbombeu/credimi-runner/internal/controller/identity"
)

type CommandRunner interface {
	Run(context.Context, string, []string, []string) error
	Output(context.Context, string, []string, []string) ([]byte, error)
}
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, env []string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Env = env
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
func (execRunner) Output(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Env = env
	return c.CombinedOutput()
}

type DockerManager struct {
	ConfigDir    string
	BinaryPath   string
	Bootstrap    BootstrapOptions
	Runner       CommandRunner
	LoadConfig   func() (runnerconfig.Config, error)
	saveSettings func(string, bool) error
	host         HostContext
	hostErr      error
}

func NewDockerManager(dir, binary string) *DockerManager {
	return NewDockerManagerWithBootstrap(dir, binary, BootstrapOptions{})
}

func NewDockerManagerWithBootstrap(dir, binary string, bootstrap BootstrapOptions) *DockerManager {
	host, err := ResolveHostContext(dir)
	return &DockerManager{ConfigDir: dir, BinaryPath: binary, Bootstrap: bootstrap, Runner: execRunner{}, host: host, hostErr: err}
}
func (m *DockerManager) config() (runnerconfig.Config, error) {
	if m.LoadConfig != nil {
		return m.LoadConfig()
	}
	return runnerconfig.LoadFile(filepath.Join(m.ConfigDir, "config.toml"))
}
func (m *DockerManager) Start(ctx context.Context) error {
	cfg, err := m.config()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		cfg = runnerconfig.Bootstrap()
		if image := strings.TrimSpace(m.Bootstrap.Image); image != "" {
			cfg.Android.RunnerImage = image
		}
		if policy := strings.TrimSpace(m.Bootstrap.PullPolicy); policy != "" {
			if policy != "always" && policy != "if-not-present" && policy != "never" {
				return fmt.Errorf("invalid bootstrap pull policy %q: use always, if-not-present, or never", policy)
			}
			cfg.Android.PullPolicy = policy
		}
	}
	return m.startWithConfig(ctx, cfg)
}

func (m *DockerManager) startWithConfig(ctx context.Context, cfg runnerconfig.Config) error {
	if host, err := ResolveHostContext(m.ConfigDir); err != nil {
		return err
	} else {
		m.host = host
		m.host.Bootstrap = m.Bootstrap
		m.hostErr = nil
	}
	if m.hostErr != nil {
		return m.hostErr
	}
	autostart, err := loadAutostart(m.ConfigDir)
	if err != nil {
		return err
	}
	m.host.Bootstrap = m.Bootstrap
	m.host = ResolveServiceHostContext(cfg, m.host)
	spec, err := BuildServiceSpecWithAutostart(cfg, m.host, autostart)
	if err != nil {
		return err
	}
	if err := WriteServiceComposeWithHostAndAutostart(m.ConfigDir, cfg, m.host, autostart); err != nil {
		return err
	}
	if err := m.preflightLegacy(ctx, cfg, spec); err != nil {
		return err
	}
	if err := m.compose(ctx, "up", "-d", "runner"); err != nil {
		return err
	}
	return m.recordAppliedImageWithPolicy(ctx, cfg.Android.RunnerImage, cfg.Android.PullPolicy)
}
func (m *DockerManager) Stop(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(m.ConfigDir, "service-compose.yaml")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return m.compose(ctx, "stop", "--timeout", "30", "runner")
}
func (m *DockerManager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}

// RestartWithConfig applies an already parsed configuration snapshot. It is
// intentionally outside Manager: only attached-host restart coordination
// needs to bind a service replacement to verified config bytes.
func (m *DockerManager) RestartWithConfig(ctx context.Context, cfg runnerconfig.Config) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.startWithConfig(ctx, cfg)
}

// ServiceMatchesConfig verifies the running service against an explicit
// configuration snapshot rather than whatever config.toml currently contains.
func (m *DockerManager) ServiceMatchesConfig(ctx context.Context, cfg runnerconfig.Config) (bool, error) {
	host, err := ResolveHostContext(m.ConfigDir)
	if err != nil {
		return false, err
	}
	host.Bootstrap = m.Bootstrap
	host = ResolveServiceHostContext(cfg, host)
	spec, err := BuildServiceSpec(cfg, host)
	if err != nil {
		return false, err
	}
	if m.Runner == nil {
		m.Runner = execRunner{}
	}
	composeArgs := []string{"compose", "--project-name", ProjectName(m.ConfigDir, host.UID), "-f", filepath.Join(m.ConfigDir, "service-compose.yaml"), "ps", "-q", "runner"}
	out, err := m.Runner.Output(ctx, "docker", composeArgs, os.Environ())
	if err != nil {
		return false, err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return false, nil
	}
	label, err := m.Runner.Output(ctx, "docker", []string{"inspect", "--format", "{{ index .Config.Labels \"io.credimi.runner.service-fingerprint\" }}", id}, os.Environ())
	if err != nil {
		return false, err
	}
	m.host = host
	return strings.TrimSpace(string(label)) == spec.Fingerprint(), nil
}

func (m *DockerManager) Enable(ctx context.Context) error {
	return m.setAutostart(ctx, true)
}

func (m *DockerManager) Disable(ctx context.Context) error {
	return m.setAutostart(ctx, false)
}

func (m *DockerManager) setAutostart(ctx context.Context, enabled bool) error {
	previous, err := loadAutostart(m.ConfigDir)
	if err != nil {
		return err
	}
	containerID, err := m.runningContainerID(ctx)
	if err != nil {
		return err
	}
	save := m.saveSettings
	if save == nil {
		save = saveAutostart
	}
	if err := save(m.ConfigDir, enabled); err != nil {
		return fmt.Errorf("save autostart setting: %w", err)
	}
	if containerID != "" {
		policy := "on-failure"
		if enabled {
			policy = "always"
		}
		if err := m.Runner.Run(ctx, "docker", []string{"update", "--restart", policy, containerID}, os.Environ()); err != nil {
			rollbackErr := save(m.ConfigDir, previous)
			if rollbackErr != nil {
				return fmt.Errorf("update runner restart policy: %w (restore autostart setting: %v)", err, rollbackErr)
			}
			return fmt.Errorf("update runner restart policy: %w", err)
		}
	}
	return nil
}

func (m *DockerManager) runningContainerID(ctx context.Context) (string, error) {
	if _, err := os.Stat(filepath.Join(m.ConfigDir, "service-compose.yaml")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if m.Runner == nil {
		m.Runner = execRunner{}
	}
	args := []string{"compose", "--project-name", ProjectName(m.ConfigDir, m.host.UID), "-f", filepath.Join(m.ConfigDir, "service-compose.yaml"), "ps", "-aq", "runner"}
	out, err := m.Runner.Output(ctx, "docker", args, os.Environ())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
func (m *DockerManager) compose(ctx context.Context, args ...string) error {
	if m.Runner == nil {
		m.Runner = execRunner{}
	}
	env := os.Environ()
	composeArgs := []string{"compose", "--project-name", ProjectName(m.ConfigDir, m.host.UID), "-f", filepath.Join(m.ConfigDir, "service-compose.yaml")}
	if err := m.Runner.Run(ctx, "docker", append(composeArgs, args...), env); err != nil {
		return fmt.Errorf("docker compose %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

type inspectedContainer struct {
	ID     string         `json:"Id"`
	Name   string         `json:"Name"`
	State  containerState `json:"State"`
	Config struct {
		Image      string            `json:"Image"`
		Cmd        []string          `json:"Cmd"`
		Entrypoint []string          `json:"Entrypoint"`
		Env        []string          `json:"Env"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig      containerHostConfig      `json:"HostConfig"`
	NetworkSettings containerNetworkSettings `json:"NetworkSettings"`
}

type containerState struct {
	Running bool `json:"Running"`
}

type containerPortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type containerHostConfig struct {
	NetworkMode  string                            `json:"NetworkMode"`
	PortBindings map[string][]containerPortBinding `json:"PortBindings"`
}

type containerNetworkSettings struct {
	Ports map[string][]containerPortBinding `json:"Ports"`
}

const (
	historicalPhoneImage    = "ghcr.io/forkbombeu/credimi-runner-phone"
	historicalEmulatorImage = "ghcr.io/forkbombeu/credimi-runner-emulator"
)

// preflightLegacy protects the generated service from runner containers made
// by the old installer and pre-unified Compose files. The installer history
// contains phone/emulator images with --usb, --emulator, and --no-device;
// later Compose files use --host-adb with those modes or Wi-Fi address args.
// The earlier shared-inventory Compose form also used --inventory, so it is
// retained as one additional historical command shape.
func (m *DockerManager) preflightLegacy(ctx context.Context, cfg runnerconfig.Config, desired ServiceSpec) error {
	if m.Runner == nil {
		m.Runner = execRunner{}
	}
	idsRaw, err := m.Runner.Output(ctx, "docker", []string{"ps", "-aq"}, os.Environ())
	if err != nil {
		return fmt.Errorf("inspect existing Docker containers: %w", err)
	}
	var ids []string
	for _, line := range strings.Split(string(idsRaw), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	inspectArgs := append([]string{"inspect"}, ids...)
	inspectedRaw, err := m.Runner.Output(ctx, "docker", inspectArgs, os.Environ())
	if err != nil {
		return fmt.Errorf("inspect existing Docker containers: %w", err)
	}
	var containers []inspectedContainer
	if err := json.Unmarshal(inspectedRaw, &containers); err != nil {
		return fmt.Errorf("decode existing Docker containers: %w", err)
	}
	project := ProjectName(m.ConfigDir, m.host.UID)
	for _, container := range containers {
		if container.Config.Labels[serviceManagedLabel] == "true" && container.Config.Labels[serviceProjectLabel] == project {
			continue
		}
		if !isHistoricalRunner(container) {
			continue
		}
		if !container.State.Running {
			continue
		}
		if !containerConflictsWithService(container, cfg, desired) {
			continue
		}
		if containerBelongsToInstallation(container, m.ConfigDir, project) {
			id := strings.TrimSpace(container.ID)
			if id == "" {
				continue
			}
			fmt.Fprintf(os.Stderr, "Retiring legacy Credimi Runner container %s (%s)\n", id, displayContainerName(container))
			if err := m.Runner.Run(ctx, "docker", []string{"rm", "-f", id}, os.Environ()); err != nil {
				return fmt.Errorf("remove legacy Credimi Runner container %s: %w", id, err)
			}
			continue
		}
		id := strings.TrimSpace(container.ID)
		return fmt.Errorf("existing container %s (%s) conflicts with the Credimi Runner service but could not be proven to belong to this installation; inspect it with `docker inspect %s` and remove it manually if appropriate", id, displayContainerName(container), id)
	}
	return nil
}

func isHistoricalRunner(container inspectedContainer) bool {
	image := strings.TrimSpace(strings.Split(container.Config.Image, "@")[0])
	if image != historicalPhoneImage && image != historicalEmulatorImage &&
		!strings.HasPrefix(image, historicalPhoneImage+":") && !strings.HasPrefix(image, historicalEmulatorImage+":") {
		return false
	}
	args := append(append([]string{}, container.Config.Entrypoint...), container.Config.Cmd...)
	for _, arg := range args {
		if arg == "--usb" || arg == "--emulator" || arg == "--no-device" || arg == "--inventory" {
			return true
		}
	}
	for _, arg := range args {
		if strings.Contains(arg, ":") && !strings.HasPrefix(arg, "-") {
			return true
		}
	}
	return false
}

func containerBelongsToInstallation(container inspectedContainer, configDir, project string) bool {
	labels := container.Config.Labels
	if labels[serviceProjectLabel] == project && labels[serviceManagedLabel] == "true" {
		return false
	}
	want := normalizedPath(configDir)
	if normalizedPath(labels["com.docker.compose.project.working_dir"]) == want && historicalComposeService(labels["com.docker.compose.service"]) {
		return true
	}
	for _, path := range strings.Split(labels["com.docker.compose.project.config_files"], ",") {
		path = normalizedPath(path)
		if path == filepath.Join(want, "docker-compose.yml") || path == filepath.Join(want, "docker-compose.yaml") {
			return true
		}
	}
	for _, value := range container.Config.Env {
		key, value, ok := strings.Cut(value, "=")
		if ok && key == "CREDIMI_RUNNER_CONFIG_DIR" && value != "/app" && normalizedPath(value) == want {
			return true
		}
	}
	return false
}

func historicalComposeService(service string) bool {
	return service == "runner" || service == "runner_emulator"
}

func containerConflictsWithService(container inspectedContainer, cfg runnerconfig.Config, desired ServiceSpec) bool {
	if !container.State.Running {
		return false
	}
	desiredPorts := serviceHostPorts(desired)
	if desired.NetworkMode == "host" {
		if _, port := listenPort(cfg.Server.APIListen, "8050"); port != "" && port != "0" {
			desiredPorts[port] = struct{}{}
		}
	}
	if strings.EqualFold(container.HostConfig.NetworkMode, "host") {
		for port := range legacyRunnerPorts(container) {
			if _, ok := desiredPorts[port]; ok {
				return true
			}
		}
	}
	for _, bindings := range []map[string][]containerPortBinding{container.HostConfig.PortBindings, container.NetworkSettings.Ports} {
		for _, entries := range bindings {
			for _, entry := range entries {
				if _, ok := desiredPorts[entry.HostPort]; ok {
					return true
				}
			}
		}
	}
	return false
}

func legacyRunnerPorts(container inspectedContainer) map[string]struct{} {
	var port, runnerPort string
	for _, value := range container.Config.Env {
		key, value, ok := strings.Cut(value, "=")
		if !ok || (key != "PORT" && key != "RUNNER_PORT") {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch key {
		case "PORT":
			port = value
		case "RUNNER_PORT":
			runnerPort = value
		}
	}
	if port != "" {
		return map[string]struct{}{port: {}}
	}
	if runnerPort != "" {
		return map[string]struct{}{runnerPort: {}}
	}
	return map[string]struct{}{"8050": {}}
}

func serviceHostPorts(spec ServiceSpec) map[string]struct{} {
	ports := make(map[string]struct{}, len(spec.Ports))
	for _, port := range spec.Ports {
		if value := strings.TrimSpace(port.HostPort); value != "" {
			ports[value] = struct{}{}
		}
	}
	return ports
}

func normalizedPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func displayContainerName(container inspectedContainer) string {
	name := strings.TrimPrefix(strings.TrimSpace(container.Name), "/")
	if name == "" {
		return "unknown"
	}
	return name
}
func (m *DockerManager) Status(ctx context.Context) (Status, error) {
	autostart, err := loadAutostart(m.ConfigDir)
	if err != nil {
		return Status{}, err
	}
	composePath := filepath.Join(m.ConfigDir, "service-compose.yaml")
	if _, err := os.Stat(composePath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Status{}, err
		}
		status := Status{Autostart: autostart, DashboardURL: "http://127.0.0.1:8051"}
		if cfg, cfgErr := m.config(); cfgErr == nil {
			if desiredSpec, desiredErr := BuildServiceSpec(cfg, m.host); desiredErr == nil {
				status.DashboardURL = dashboardURLForServiceNetwork(cfg, desiredSpec.NetworkMode)
			} else {
				status.DashboardURL = desiredDashboardURL(cfg)
			}
		} else if !errors.Is(cfgErr, os.ErrNotExist) {
			return Status{}, cfgErr
		}
		populateRuntimeState(m.ConfigDir, &status)
		return status, nil
	}
	if m.Runner == nil {
		m.Runner = execRunner{}
	}
	composeArgs := []string{"compose", "--project-name", ProjectName(m.ConfigDir, m.host.UID), "-f", filepath.Join(m.ConfigDir, "service-compose.yaml"), "ps", "-q", "runner"}
	out, err := m.Runner.Output(ctx, "docker", composeArgs, os.Environ())
	if err != nil {
		return Status{}, err
	}
	status := Status{Autostart: autostart, Running: strings.TrimSpace(string(out)) != "", DashboardURL: "http://127.0.0.1:8051"}
	cfg, err := m.config()
	if err != nil {
		return Status{}, fmt.Errorf("load desired service configuration: %w", err)
	}
	baseHost, err := ResolveHostContext(m.ConfigDir)
	if err != nil {
		return Status{}, fmt.Errorf("resolve host service topology: %w", err)
	}
	baseHost.Bootstrap = m.Bootstrap
	m.host = ResolveServiceHostContext(cfg, baseHost)
	desiredSpec, err := BuildServiceSpec(cfg, m.host)
	if err != nil {
		return Status{}, fmt.Errorf("build desired service specification: %w", err)
	}
	status.DashboardURL = dashboardURLForServiceNetwork(cfg, desiredSpec.NetworkMode)
	if status.Running {
		id := strings.TrimSpace(string(out))
		running, environment, err := m.containerMetadata(ctx, id)
		if err != nil {
			return Status{}, fmt.Errorf("inspect runner service metadata: %w", err)
		}
		if capabilities, present, valid := ServiceCapabilitiesFromEnvironment(environment); present {
			if !valid || !completeAppliedServiceCapabilities(environment) {
				return Status{}, errors.New("inspect runner service metadata: applied capability metadata is incomplete or invalid")
			}
			applied := strings.TrimSpace(environment[AppliedServiceConfigFingerprintEnv])
			if applied == "" {
				return Status{}, errors.New("inspect runner service metadata: applied service fingerprint is empty")
			}
			status.ServiceRestartRequired = !ServiceConfigCompatibleWithFingerprint(cfg, true, applied, capabilities)
		} else {
			status.ServiceRestartRequired = strings.TrimSpace(string(running)) != desiredSpec.Fingerprint()
		}
	}
	if status.Running {
		probeCtx, cancel := context.WithTimeout(ctx, controller.ProbeTimeout)
		if live, liveErr := readLiveController(probeCtx, m.ConfigDir); liveErr == nil {
			status.DashboardURL = strings.TrimRight(live.PublicURL, "/")
		}
		cancel()
	}
	populateRuntimeState(m.ConfigDir, &status)
	return status, nil
}

func completeAppliedServiceCapabilities(values map[string]string) bool {
	for _, key := range []string{
		AppliedServiceConfigFingerprintEnv,
		AppliedServiceNeedsHostADBEnv,
		AppliedServiceNeedsUSBEnv,
		AppliedServiceNeedsEmulatorEnv,
		AppliedServiceRedroidKnownHostsEnv,
		ServiceNetworkModeEnv,
		AppliedServiceResolvedHostsEnv,
	} {
		value, ok := values[key]
		if !ok || (key == AppliedServiceConfigFingerprintEnv || key == ServiceNetworkModeEnv) && strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func (m *DockerManager) containerMetadata(ctx context.Context, id string) ([]byte, map[string]string, error) {
	raw, err := m.Runner.Output(ctx, "docker", []string{"inspect", "--format", "{{ index .Config.Labels \"io.credimi.runner.service-fingerprint\" }}{{printf \"\\n\"}}{{json .Config.Env}}", id}, os.Environ())
	if err != nil {
		return nil, nil, err
	}
	parts := strings.SplitN(string(raw), "\n", 2)
	label := []byte(parts[0])
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return label, map[string]string{}, nil
	}
	var entries []string
	if err := json.Unmarshal([]byte(parts[1]), &entries); err != nil {
		return nil, nil, fmt.Errorf("decode container environment: %w", err)
	}
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return label, values, nil
}

func (m *DockerManager) UpgradeImage(ctx context.Context, progress func(string)) error {
	composePath := filepath.Join(m.ConfigDir, "service-compose.yaml")
	if _, err := os.Stat(composePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("persistent service has not been created; run credimi-runner service start")
		}
		return err
	}
	cfg, err := m.config()
	if err != nil {
		return err
	}
	status, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Android.PullPolicy) != "never" {
		if progress != nil {
			progress("Pulling runner image")
		}
		if err := m.compose(ctx, "pull", "runner"); err != nil {
			return err
		}
	}
	if !status.Running {
		return nil
	}
	if progress != nil {
		progress("Recreating runner service")
	}
	if err := m.compose(ctx, "up", "-d", "--force-recreate", "runner"); err != nil {
		return err
	}
	return m.recordAppliedImageWithPolicy(ctx, cfg.Android.RunnerImage, cfg.Android.PullPolicy)
}

func (m *DockerManager) recordAppliedImageWithPolicy(ctx context.Context, configured, pullPolicy string) error {
	if strings.TrimSpace(configured) == "" {
		configured = defaultServiceImage
	}
	args := []string{"compose", "--project-name", ProjectName(m.ConfigDir, m.host.UID), "-f", filepath.Join(m.ConfigDir, "service-compose.yaml"), "ps", "-q", "runner"}
	out, err := m.Runner.Output(ctx, "docker", args, os.Environ())
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return errors.New("runner service container unavailable")
	}
	localID, err := m.Runner.Output(ctx, "docker", []string{"inspect", "--format", "{{.Image}}", id}, os.Environ())
	if err != nil {
		return err
	}
	var repoDigests []string
	imageRaw, err := m.Runner.Output(ctx, "docker", []string{"image", "inspect", "--format", "{{json .RepoDigests}}", strings.TrimSpace(string(localID))}, os.Environ())
	if err != nil {
		return err
	}
	if err := json.Unmarshal(imageRaw, &repoDigests); err != nil {
		return fmt.Errorf("decode runner image RepoDigests: %w", err)
	}
	configuredRepo := configured
	if at := strings.LastIndex(configuredRepo, "@"); at >= 0 {
		configuredRepo = configuredRepo[:at]
	}
	if colon := strings.LastIndex(configuredRepo, ":"); colon > strings.LastIndex(configuredRepo, "/") {
		configuredRepo = configuredRepo[:colon]
	}
	var digest string
	for _, candidate := range repoDigests {
		if at := strings.LastIndex(candidate, "@"); at >= 0 && strings.TrimSuffix(candidate[:at], ":") == configuredRepo {
			digest = candidate[at+1:]
			break
		}
	}
	digest = strings.TrimSpace(digest)
	registryTrackable := true
	if digest == "" {
		if strings.TrimSpace(pullPolicy) == "never" {
			registryTrackable = false
		} else if len(repoDigests) == 0 {
			return errors.New("runner image has no RepoDigests")
		} else {
			return fmt.Errorf("no RepoDigest matches configured image repository %q", configuredRepo)
		}
	} else if !validSHA256Digest(digest) {
		return fmt.Errorf("invalid runner image RepoDigest %q", digest)
	}
	state := struct {
		Image             string    `json:"image"`
		ImageID           string    `json:"image_id"`
		Digest            string    `json:"digest"`
		RegistryTrackable bool      `json:"registry_trackable"`
		UpdatedAt         time.Time `json:"updated_at"`
	}{configured, strings.TrimSpace(string(localID)), digest, registryTrackable, time.Now().UTC()}
	raw, _ := json.Marshal(state)
	return atomicfile.WriteAtomic(filepath.Join(m.ConfigDir, "service-image-state.json"), 0o600, atomicfile.FromEnvironment(), func(w io.Writer) error { _, err := w.Write(raw); return err })
}

func validSHA256Digest(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "sha256:") && len(strings.TrimPrefix(value, "sha256:")) > 0
}
func (m *DockerManager) Logs(ctx context.Context, opts LogOptions) error {
	lines := opts.Lines
	if lines <= 0 {
		lines = 200
	}
	args := []string{"compose", "--project-name", ProjectName(m.ConfigDir, m.host.UID), "-f", filepath.Join(m.ConfigDir, "service-compose.yaml"), "logs", "--tail", fmt.Sprint(lines)}
	if opts.Follow {
		args = append(args, "-f")
	}
	args = append(args, "runner")
	return m.Runner.Run(ctx, "docker", args, os.Environ())
}
