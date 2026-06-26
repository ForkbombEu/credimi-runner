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
	SetPublicURL(string)
	Status(context.Context) RuntimeStatus
	Logs(context.Context, int) ([]LogLine, error)
}

type RuntimeStatus struct {
	Configured           bool
	RunnerRunning        bool
	ComposeRunning       bool
	PublicURL            string
	LastStartedAt        time.Time
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
	logCmd    *exec.Cmd
	logDone   chan struct{}
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
			cmd, err := m.runner.Start(context.WithoutCancel(ctx), CommandSpec{
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
		m.status.LastStartedAt = time.Now()
	}

	if len(plan.ComposeServices) > 0 {
		if err := WriteComposeFile(m.configDir, m.values); err != nil {
			m.status.LastError = err.Error()
			return err
		}
		args := []string{"compose", "--env-file", plan.EnvPath, "-f", plan.ComposePath, "up", "-d"}
		args = append(args, plan.ComposeServices...)
		if _, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: args}); err != nil {
			m.status.LastError = err.Error()
			return err
		}
		m.startComposeLogFollowerLocked(plan)
		m.status.ComposeRunning = true
		m.status.LastStartedAt = time.Now()
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
		m.stopComposeLogFollowerLocked()
		args := []string{"compose", "--env-file", plan.EnvPath, "-f", plan.ComposePath, "stop"}
		args = append(args, plan.ComposeServices...)
		if _, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: args}); err != nil {
			m.status.LastError = err.Error()
			return err
		}
	}

	m.status.RunnerRunning = false
	m.status.ComposeRunning = false
	m.status.PublicURL = ""
	return nil
}

func (m *LifecycleManager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}

func (m *LifecycleManager) Down(ctx context.Context) error {
	plan := BuildRuntimePlan(m.configDir, m.values)
	if err := m.Stop(ctx); err != nil {
		return err
	}
	if len(plan.ComposeServices) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.runner.Run(ctx, CommandSpec{
		Name: "docker",
		Args: []string{"compose", "--env-file", plan.EnvPath, "-f", plan.ComposePath, "down"},
	}); err != nil {
		m.status.LastError = err.Error()
		return err
	}
	m.status.PublicURL = ""
	return nil
}

func (m *LifecycleManager) startComposeLogFollowerLocked(plan RuntimePlan) {
	if m.logCmd != nil || len(plan.ComposeServices) == 0 {
		return
	}
	args := []string{"compose", "--env-file", plan.EnvPath, "-f", plan.ComposePath, "logs", "-f", "--tail", "80"}
	args = append(args, plan.ComposeServices...)
	cmd, err := m.runner.Start(context.Background(), CommandSpec{Name: "docker", Args: args})
	if err != nil {
		m.status.LastError = err.Error()
		return
	}
	m.logCmd = cmd
	done := make(chan struct{})
	m.logDone = done
	go func() {
		_ = cmd.Wait()
		close(done)
		m.mu.Lock()
		if m.logCmd == cmd {
			m.logCmd = nil
			m.logDone = nil
		}
		m.mu.Unlock()
	}()
}

func (m *LifecycleManager) stopComposeLogFollowerLocked() {
	if m.logCmd == nil || m.logCmd.Process == nil {
		m.logCmd = nil
		m.logDone = nil
		return
	}
	_ = m.logCmd.Process.Signal(syscall.SIGTERM)
	done := m.logDone
	if done == nil {
		m.logCmd = nil
		return
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = m.logCmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	m.logCmd = nil
	m.logDone = nil
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

func (m *LifecycleManager) SetPublicURL(publicURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.PublicURL = strings.TrimSpace(publicURL)
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
		Args: composeLogArgs(plan, tail, m.status.LastStartedAt),
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

func composeLogArgs(plan RuntimePlan, tail int, since time.Time) []string {
	args := []string{"compose", "--env-file", plan.EnvPath, "-f", plan.ComposePath, "logs"}
	if !since.IsZero() {
		args = append(args, "--since", since.UTC().Format(time.RFC3339))
	}
	args = append(args, "--tail", fmt.Sprintf("%d", tail))
	return args
}
