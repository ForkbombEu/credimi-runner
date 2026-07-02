package runtime

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	Name     string
	Args     []string
	Dir      string
	Env      []string
	Detached bool
	LogPath  string
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
	if spec.Detached {
		detachCommand(cmd)
		cmd.Stdin = nil
		if spec.LogPath != "" {
			if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o700); err != nil {
				return nil, err
			}
			logFile, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return nil, err
			}
			defer logFile.Close()
			cmd.Stdout = logFile
			cmd.Stderr = logFile
		}
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
	}
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
		if runnerAddressReachable(m.values, 500*time.Millisecond) {
			m.status.RunnerRunning = true
			m.status.LastStartedAt = time.Now()
		} else if m.cmd == nil {
			cmd, err := m.runner.Start(context.WithoutCancel(ctx), CommandSpec{
				Name: m.binary,
				Args: []string{
					"serve",
					"--host", defaultIfEmpty(m.values["RUNNER_HOST"], DefaultRunnerHost),
					"--port", defaultIfEmpty(m.values["RUNNER_PORT"], DefaultRunnerPort),
				},
				Dir:      m.configDir,
				Env:      append(os.Environ(), "CREDIMI_RUNNER_CONFIG_DIR="+m.configDir),
				Detached: true,
				LogPath:  filepath.Join(m.configDir, "runner.log"),
			})
			if err != nil {
				m.status.LastError = err.Error()
				return err
			}
			m.cmd = cmd
			m.status.RunnerRunning = true
			m.status.LastStartedAt = time.Now()
		}
	}

	if len(plan.ComposeServices) > 0 {
		if err := WriteComposeFile(m.configDir, m.values); err != nil {
			m.status.LastError = err.Error()
			return err
		}
		if err := m.stopStaleComposeServicesLocked(ctx, plan); err != nil {
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

func (m *LifecycleManager) stopStaleComposeServicesLocked(ctx context.Context, plan RuntimePlan) error {
	staleServices := staleComposeServices(plan.ComposeServices)
	if len(staleServices) == 0 {
		return nil
	}
	args := []string{"compose", "--env-file", plan.EnvPath, "-f", plan.ComposePath, "rm", "-f", "-s"}
	args = append(args, staleServices...)
	_, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: args})
	return err
}

func staleComposeServices(active []string) []string {
	activeSet := make(map[string]struct{}, len(active))
	for _, service := range active {
		activeSet[service] = struct{}{}
	}
	var stale []string
	for _, service := range []string{"runner", "runner_host", "caddy", "tunnel", "tunnel_named"} {
		if _, ok := activeSet[service]; !ok {
			stale = append(stale, service)
		}
	}
	return stale
}

func (m *LifecycleManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	plan := BuildRuntimePlan(m.configDir, m.values)
	if plan.Backend == DefaultHostBackend {
		if err := m.stopHostRunnerLocked(ctx); err != nil {
			m.status.LastError = err.Error()
			return err
		}
	}

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

func (m *LifecycleManager) stopHostRunnerLocked(ctx context.Context) error {
	if m.cmd != nil && m.cmd.Process != nil {
		err := stopStartedCommand(ctx, m.cmd)
		m.cmd = nil
		return err
	}
	m.cmd = nil
	pid, err := discoverRunnerPID(m.values)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return stopPID(ctx, pid)
}

func stopStartedCommand(ctx context.Context, cmd *exec.Cmd) error {
	_ = cmd.Process.Signal(syscall.SIGTERM)
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()
	select {
	case err := <-waitDone:
		if err != nil && !strings.Contains(err.Error(), "signal") {
			return err
		}
	case <-time.After(10 * time.Second):
		if err := cmd.Process.Kill(); err != nil {
			return err
		}
		_, _ = cmd.Process.Wait()
	case <-ctx.Done():
		if err := cmd.Process.Kill(); err != nil {
			return err
		}
		_, _ = cmd.Process.Wait()
		return ctx.Err()
	}
	return nil
}

func stopPID(ctx context.Context, pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if !processRunning(pid) {
		return nil
	}
	if !processCommandMatches(pid) {
		return fmt.Errorf("refusing to stop PID %d because it is not credimi-runner serve", pid)
	}
	_ = process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() {
		for {
			if !processRunning(pid) {
				done <- nil
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		return process.Kill()
	case <-ctx.Done():
		_ = process.Kill()
		return ctx.Err()
	}
}

func processRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func runnerAddressReachable(values Values, timeout time.Duration) bool {
	host, port := runnerListenTarget(values)
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func runnerListenTarget(values Values) (string, string) {
	host := strings.TrimSpace(values["RUNNER_HOST"])
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := strings.TrimSpace(values["RUNNER_PORT"])
	if port == "" {
		port = DefaultRunnerPort
	}
	return host, port
}

func discoverRunnerPID(values Values) (int, error) {
	_, port := runnerListenTarget(values)
	portNumber, err := strconv.ParseInt(port, 10, 64)
	if err != nil {
		return 0, err
	}
	if portNumber < 1 || portNumber > 65535 {
		return 0, fmt.Errorf("invalid runner port %q", port)
	}
	if runtime.GOOS == "linux" {
		pid, err := discoverRunnerPIDFromProc(uint16(portNumber))
		if err == nil {
			return pid, nil
		}
		if !os.IsNotExist(err) {
			return 0, err
		}
	}
	return discoverRunnerPIDFromLsof(port)
}

func discoverRunnerPIDFromProc(port uint16) (int, error) {
	inodes, err := listeningSocketInodes(port)
	if err != nil {
		return 0, err
	}
	for inode := range inodes {
		pid, err := pidForSocketInode(inode)
		if err == nil {
			return pid, nil
		}
	}
	return 0, os.ErrNotExist
}

func discoverRunnerPIDFromLsof(port string) (int, error) {
	output, err := exec.Command("lsof", "-nP", "-tiTCP:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return 0, os.ErrNotExist
	}
	for _, line := range strings.Split(string(output), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		if processCommandMatches(pid) {
			return pid, nil
		}
	}
	return 0, os.ErrNotExist
}

func listeningSocketInodes(port uint16) (map[string]struct{}, error) {
	inodes := map[string]struct{}{}
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			_, rawPort, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			parsedPort, err := strconv.ParseUint(rawPort, 16, 16)
			if err == nil && uint16(parsedPort) == port {
				inodes[fields[9]] = struct{}{}
			}
		}
	}
	if len(inodes) == 0 {
		return nil, os.ErrNotExist
	}
	return inodes, nil
}

func pidForSocketInode(inode string) (int, error) {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	want := "socket:[" + inode + "]"
	for _, proc := range procs {
		if !proc.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(proc.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", proc.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || target != want {
				continue
			}
			if processCommandMatches(pid) {
				return pid, nil
			}
		}
	}
	return 0, os.ErrNotExist
}

func processCommandMatches(pid int) bool {
	cmdline, err := processCommandLine(pid)
	if err != nil {
		return false
	}
	return strings.Contains(cmdline, "credimi-runner") && strings.Contains(" "+cmdline+" ", " serve ")
}

func processCommandLine(pid int) (string, error) {
	if runtime.GOOS != "linux" {
		return processCommandLineFromPS(pid)
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return processCommandLineFromPS(pid)
	}
	cmdline := strings.ReplaceAll(string(raw), "\x00", " ")
	if strings.TrimSpace(cmdline) == "" {
		return processCommandLineFromPS(pid)
	}
	return cmdline, nil
}

func processCommandLineFromPS(pid int) (string, error) {
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
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
