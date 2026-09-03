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
	ConfigDir  string
	BinaryPath string
	Bootstrap  BootstrapOptions
	Runner     CommandRunner
	LoadConfig func() (runnerconfig.Config, error)
	host       HostContext
	hostErr    error
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
	if host, err := ResolveHostContext(m.ConfigDir); err != nil {
		return err
	} else {
		m.host = host
		m.hostErr = nil
	}
	if m.hostErr != nil {
		return m.hostErr
	}
	autostart, err := loadAutostart(m.ConfigDir)
	if err != nil {
		return err
	}
	cfg, err := m.config()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
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
	if err := WriteServiceComposeWithHostAndAutostart(m.ConfigDir, cfg, m.host, autostart); err != nil {
		return err
	}
	if err := m.preflightLegacy(ctx); err != nil {
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
	return m.compose(ctx, "down", "--timeout", "30")
}
func (m *DockerManager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}

func (m *DockerManager) Enable(ctx context.Context) error {
	return m.setAutostart(ctx, true)
}

func (m *DockerManager) Disable(ctx context.Context) error {
	return m.setAutostart(ctx, false)
}

func (m *DockerManager) setAutostart(ctx context.Context, enabled bool) error {
	if _, err := loadAutostart(m.ConfigDir); err != nil {
		return err
	}
	if containerID, err := m.runningContainerID(ctx); err != nil {
		return err
	} else if containerID != "" {
		policy := "on-failure"
		if enabled {
			policy = "unless-stopped"
		}
		if err := m.Runner.Run(ctx, "docker", []string{"update", "--restart", policy, containerID}, os.Environ()); err != nil {
			return fmt.Errorf("update runner restart policy: %w", err)
		}
	}
	return saveAutostart(m.ConfigDir, enabled)
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
	args := []string{"compose", "--project-name", ProjectName(m.ConfigDir, m.host.UID), "-f", filepath.Join(m.ConfigDir, "service-compose.yaml"), "ps", "-q", "runner"}
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
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image      string            `json:"Image"`
		Cmd        []string          `json:"Cmd"`
		Entrypoint []string          `json:"Entrypoint"`
		Env        []string          `json:"Env"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
	Mounts []struct {
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
}

const (
	historicalPhoneImage    = "ghcr.io/forkbombeu/credimi-runner-phone"
	historicalEmulatorImage = "ghcr.io/forkbombeu/credimi-runner-emulator"
)

// preflightLegacy protects the generated service from the pre-unified runner
// containers. History shows those services used the phone/emulator images and
// the --inventory command. Those traits identify a likely old runner; the
// config-dir mount or current project label identifies this installation.
func (m *DockerManager) preflightLegacy(ctx context.Context) error {
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
		return fmt.Errorf("existing container %s (%s) appears to conflict with the Credimi Runner service but could not be proven to belong to this installation; inspect it with `docker inspect %s` and remove it manually if appropriate", id, displayContainerName(container), id)
	}
	return nil
}

func isHistoricalRunner(container inspectedContainer) bool {
	image := strings.TrimSpace(strings.Split(container.Config.Image, "@")[0])
	if image != historicalPhoneImage && image != historicalEmulatorImage &&
		!strings.HasPrefix(image, historicalPhoneImage+":") && !strings.HasPrefix(image, historicalEmulatorImage+":") {
		return false
	}
	command := strings.Join(append(append([]string{}, container.Config.Entrypoint...), container.Config.Cmd...), " ")
	return strings.Contains(command, "--inventory")
}

func containerBelongsToInstallation(container inspectedContainer, configDir, project string) bool {
	if container.Config.Labels[serviceProjectLabel] == project {
		return true
	}
	want := normalizedPath(configDir)
	for _, mount := range container.Mounts {
		if normalizedPath(mount.Source) == want {
			return true
		}
	}
	for _, value := range container.Config.Env {
		key, value, ok := strings.Cut(value, "=")
		if ok && key == "CREDIMI_RUNNER_CONFIG_DIR" && normalizedPath(value) == want {
			return true
		}
	}
	return false
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
	if cfg, cfgErr := m.config(); cfgErr == nil {
		if desiredSpec, desiredErr := BuildServiceSpec(cfg, m.host); desiredErr == nil {
			status.DashboardURL = dashboardURLForServiceNetwork(cfg, desiredSpec.NetworkMode)
			desired := desiredSpec.Fingerprint()
			if running, runErr := m.Runner.Output(ctx, "docker", []string{"inspect", "--format", "{{ index .Config.Labels \"io.credimi.runner.service-fingerprint\" }}", strings.TrimSpace(string(out))}, os.Environ()); runErr == nil {
				status.ServiceRestartRequired = strings.TrimSpace(string(running)) != desired
			}
		} else {
			status.DashboardURL = desiredDashboardURL(cfg)
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

func (m *DockerManager) recordAppliedImage(ctx context.Context, configured string) error {
	return m.recordAppliedImageWithPolicy(ctx, configured, "")
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
