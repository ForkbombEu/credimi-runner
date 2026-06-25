package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Manager interface {
	Start(context.Context) error
	Stop(context.Context) error
	Restart(context.Context) error
	Down(context.Context) error
	UpdateImage(context.Context) error
	Configure(Values)
	Status(context.Context) RuntimeStatus
	Logs(context.Context, int) ([]LogLine, error)
}

type RuntimeStatus struct {
	Configured           bool
	RunnerRunning        bool
	ComposeRunning       bool
	PublicURL            string
	LastError            string
	PendingRestart       bool
	PendingRecreate      bool
	PendingCredimiUpdate bool
}

type LogLine struct {
	Message string
}

type CommandSpec struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

type Runner interface {
	Run(context.Context, CommandSpec) ([]byte, error)
	Start(context.Context, CommandSpec) (*exec.Cmd, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, spec CommandSpec) ([]byte, error) {
	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	if len(spec.Env) > 0 {
		cmd.Env = spec.Env
	}
	return cmd.CombinedOutput()
}

func (ExecRunner) Start(ctx context.Context, spec CommandSpec) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	if len(spec.Env) > 0 {
		cmd.Env = spec.Env
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

type LifecycleManager struct {
	mu        sync.Mutex
	binary    string
	configDir string
	values    Values
	runner    Runner
	cmd       *exec.Cmd
	status    RuntimeStatus
}

func NewLifecycleManager(binary, configDir string, values Values, runner Runner) *LifecycleManager {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &LifecycleManager{
		binary:    binary,
		configDir: configDir,
		values:    cloneValues(values),
		runner:    runner,
		status: RuntimeStatus{
			Configured: strings.TrimSpace(values["CREDIMI_RUNNER_ID"]) != "",
		},
	}
}

func (m *LifecycleManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	plan := BuildRuntimePlan(m.configDir, m.values)
	if plan.Backend == DefaultHostBackend {
		if m.cmd == nil {
			cmd, err := m.runner.Start(ctx, CommandSpec{
				Name: m.binary,
				Args: []string{
					"serve",
					"--host", defaultIfEmpty(m.values["RUNNER_HOST"], DefaultRunnerHost),
					"--port", defaultIfEmpty(m.values["RUNNER_PORT"], DefaultRunnerPort),
				},
				Dir: m.configDir,
				Env: append(os.Environ(), "CREDIMI_RUNNER_CONFIG_DIR="+m.configDir),
			})
			if err != nil {
				m.status.LastError = err.Error()
				return err
			}
			m.cmd = cmd
		}
		m.status.RunnerRunning = true
	}

	if len(plan.ComposeServices) > 0 {
		args := []string{"compose", "--env-file", plan.EnvPath, "-f", plan.ComposePath, "up", "-d"}
		args = append(args, plan.ComposeServices...)
		if _, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: args}); err != nil {
			m.status.LastError = err.Error()
			return err
		}
		m.status.ComposeRunning = true
	}

	m.status.PublicURL = resolvedRunnerPublicURL(m.values, "")
	return nil
}

func (m *LifecycleManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Signal(syscall.SIGTERM)
		waitDone := make(chan error, 1)
		go func(cmd *exec.Cmd) {
			waitDone <- cmd.Wait()
		}(m.cmd)
		select {
		case err := <-waitDone:
			if err != nil && !strings.Contains(err.Error(), "signal") {
				m.status.LastError = err.Error()
				return err
			}
		case <-time.After(10 * time.Second):
			if err := m.cmd.Process.Kill(); err != nil {
				m.status.LastError = err.Error()
				return err
			}
			_, _ = m.cmd.Process.Wait()
		case <-ctx.Done():
			if err := m.cmd.Process.Kill(); err != nil {
				m.status.LastError = err.Error()
				return err
			}
			_, _ = m.cmd.Process.Wait()
			return ctx.Err()
		}
		m.cmd = nil
	}

	plan := BuildRuntimePlan(m.configDir, m.values)
	if len(plan.ComposeServices) > 0 {
		args := []string{"compose", "--env-file", plan.EnvPath, "-f", plan.ComposePath, "stop"}
		args = append(args, plan.ComposeServices...)
		if _, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: args}); err != nil {
			m.status.LastError = err.Error()
			return err
		}
	}

	m.status.RunnerRunning = false
	m.status.ComposeRunning = false
	return nil
}

func (m *LifecycleManager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}

func (m *LifecycleManager) Down(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	plan := BuildRuntimePlan(m.configDir, m.values)
	if _, err := m.runner.Run(ctx, CommandSpec{
		Name: "docker",
		Args: []string{"compose", "--env-file", plan.EnvPath, "-f", plan.ComposePath, "down"},
	}); err != nil {
		m.status.LastError = err.Error()
		return err
	}
	return nil
}

func (m *LifecycleManager) UpdateImage(ctx context.Context) error {
	image := strings.TrimSpace(m.values["RUNNER_IMAGE"])
	if image == "" {
		return fmt.Errorf("RUNNER_IMAGE is required")
	}
	if _, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: []string{"pull", image}}); err != nil {
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.mu.Unlock()
		return err
	}
	return nil
}

func (m *LifecycleManager) Configure(values Values) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values = cloneValues(values)
	m.status.Configured = strings.TrimSpace(values["CREDIMI_RUNNER_ID"]) != ""
}

func (m *LifecycleManager) Status(context.Context) RuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *LifecycleManager) Logs(ctx context.Context, tail int) ([]LogLine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan := BuildRuntimePlan(m.configDir, m.values)
	output, err := m.runner.Run(ctx, CommandSpec{
		Name: "docker",
		Args: []string{"compose", "--env-file", plan.EnvPath, "-f", plan.ComposePath, "logs", "--tail", fmt.Sprintf("%d", tail)},
	})
	if err != nil {
		return nil, err
	}
	rawLines := bytes.Split(bytes.TrimSpace(output), []byte("\n"))
	lines := make([]LogLine, 0, len(rawLines))
	for _, line := range rawLines {
		if len(line) == 0 {
			continue
		}
		lines = append(lines, LogLine{Message: string(line)})
	}
	return lines, nil
}
