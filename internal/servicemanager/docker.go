//go:build !darwin

package servicemanager

import (
	"context"
	"encoding/json"
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
	Runner     CommandRunner
	LoadConfig func() (runnerconfig.Config, error)
}

func NewDockerManager(dir, binary string) *DockerManager {
	return &DockerManager{ConfigDir: dir, BinaryPath: binary, Runner: execRunner{}}
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
	if err := WriteServiceCompose(m.ConfigDir, cfg); err != nil {
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
	if err := m.Runner.Run(ctx, "docker", append([]string{"compose", "-f", filepath.Join(m.ConfigDir, "service-compose.yaml")}, args...), env); err != nil {
		return fmt.Errorf("docker compose %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
func (m *DockerManager) Status(ctx context.Context) (Status, error) {
	if m.Runner == nil {
		m.Runner = execRunner{}
	}
	out, err := m.Runner.Output(ctx, "docker", []string{"compose", "-f", filepath.Join(m.ConfigDir, "service-compose.yaml"), "ps", "-q", "runner"}, os.Environ())
	if err != nil {
		return Status{}, err
	}
	status := Status{Running: strings.TrimSpace(string(out)) != "", DashboardURL: "http://127.0.0.1:8051"}
	if cfg, cfgErr := m.config(); cfgErr == nil {
		if desired, desiredErr := WriteServiceSpecFingerprint(m.ConfigDir, cfg); desiredErr == nil {
			if running, runErr := m.Runner.Output(ctx, "docker", []string{"inspect", "--format", "{{ index .Config.Labels \"io.credimi.runner.service-fingerprint\" }}", strings.TrimSpace(string(out))}, os.Environ()); runErr == nil {
				status.ServiceRestartRequired = strings.TrimSpace(string(running)) != desired
			}
		}
	}
	if raw, readErr := os.ReadFile(filepath.Join(m.ConfigDir, "runtime-state.json")); readErr == nil {
		var state struct {
			Desired   string `json:"desired"`
			Actual    string `json:"actual"`
			LastError string `json:"last_error"`
		}
		if json.Unmarshal(raw, &state) == nil {
			status.RuntimeDesired, status.RuntimeActual, status.RuntimeError = state.Desired, state.Actual, state.LastError
		}
	}
	return status, nil
}
func (m *DockerManager) Logs(ctx context.Context, opts LogOptions) error {
	lines := opts.Lines
	if lines <= 0 {
		lines = 200
	}
	args := []string{"compose", "-f", filepath.Join(m.ConfigDir, "service-compose.yaml"), "logs", "--tail", fmt.Sprint(lines)}
	if opts.Follow {
		args = append(args, "-f")
	}
	args = append(args, "runner")
	return m.Runner.Run(ctx, "docker", args, os.Environ())
}
