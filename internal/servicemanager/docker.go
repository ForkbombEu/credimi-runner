//go:build !darwin

package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
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
	if err := WriteServiceComposeWithHost(m.ConfigDir, cfg, m.host); err != nil {
		return err
	}
	return m.compose(ctx, "up", "-d", "runner")
}
func (m *DockerManager) Stop(ctx context.Context) error {
	return m.compose(ctx, "down", "--timeout", "30")
}
func (m *DockerManager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
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
func (m *DockerManager) Status(ctx context.Context) (Status, error) {
	if m.Runner == nil {
		m.Runner = execRunner{}
	}
	composeArgs := []string{"compose", "--project-name", ProjectName(m.ConfigDir, m.host.UID), "-f", filepath.Join(m.ConfigDir, "service-compose.yaml"), "ps", "-q", "runner"}
	out, err := m.Runner.Output(ctx, "docker", composeArgs, os.Environ())
	if err != nil {
		return Status{}, err
	}
	status := Status{Running: strings.TrimSpace(string(out)) != "", DashboardURL: "http://127.0.0.1:8051"}
	if cfg, cfgErr := m.config(); cfgErr == nil {
		status.DashboardURL = desiredDashboardURL(cfg)
		if desiredSpec, desiredErr := BuildServiceSpec(cfg, m.host); desiredErr == nil {
			desired := desiredSpec.Fingerprint()
			if running, runErr := m.Runner.Output(ctx, "docker", []string{"inspect", "--format", "{{ index .Config.Labels \"io.credimi.runner.service-fingerprint\" }}", strings.TrimSpace(string(out))}, os.Environ()); runErr == nil {
				status.ServiceRestartRequired = strings.TrimSpace(string(running)) != desired
			}
		}
	}
	if live := liveDashboardURL(ctx, m.ConfigDir); live != "" {
		status.DashboardURL = live
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
	status, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if progress != nil {
		progress("Pulling runner image")
	}
	if err := m.compose(ctx, "pull", "runner"); err != nil {
		return err
	}
	if !status.Running {
		return nil
	}
	if progress != nil {
		progress("Recreating runner service")
	}
	return m.compose(ctx, "up", "-d", "--force-recreate", "runner")
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
