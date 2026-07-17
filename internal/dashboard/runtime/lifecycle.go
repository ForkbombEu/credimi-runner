package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/forkbombeu/credimi-runner/internal/lifecyclelog"
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
	ObservedAt           time.Time
	Observed             bool
	DeviceReady          bool
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
	Stream   func(string)
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
	if spec.Stream != nil {
		return runStreaming(cmd, spec.Stream)
	}
	return cmd.CombinedOutput()
}

func runStreaming(cmd *exec.Cmd, stream func(string)) ([]byte, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	var wg sync.WaitGroup
	var mu sync.Mutex
	read := func(r io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		scanner.Split(scanDockerProgress)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			mu.Lock()
			out.WriteString(line)
			out.WriteByte('\n')
			mu.Unlock()
			stream(line)
		}
	}
	wg.Add(2)
	go read(stdout)
	go read(stderr)
	wg.Wait()
	err = cmd.Wait()
	return out.Bytes(), err
}

func scanDockerProgress(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
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
	lifecycle *lifecyclelog.Logger
}

func NewLifecycleManager(binary, configDir string, values Values, runner Runner) *LifecycleManager {
	if runner == nil {
		runner = ExecRunner{}
	}
	var lifecycle *lifecyclelog.Logger
	if strings.TrimSpace(configDir) != "" {
		lifecycle, _ = lifecyclelog.New(filepath.Join(configDir, "lifecycle.jsonl"), lifecyclelog.Options{})
	}
	return &LifecycleManager{
		binary:    binary,
		configDir: configDir,
		values:    cloneValues(values),
		runner:    runner,
		lifecycle: lifecycle,
		status: RuntimeStatus{
			Configured: strings.TrimSpace(values["CREDIMI_RUNNER_ID"]) != "",
		},
	}
}

// Close releases the lifecycle log file. It is safe to call more than once.
func (m *LifecycleManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lifecycle == nil {
		return nil
	}
	err := m.lifecycle.Close()
	m.lifecycle = nil
	return err
}

// EmitLifecycle records a controller/dashboard event in the bounded lifecycle
// log without exposing the runner's verbose process logs.
func (m *LifecycleManager) EmitLifecycle(event lifecyclelog.Event) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emitLifecycleLocked(event)
}

func (m *LifecycleManager) Start(ctx context.Context) error {
	return m.start(ctx, nil)
}

func (m *LifecycleManager) StartWithProgress(ctx context.Context, progress func(string)) error {
	return m.start(ctx, progress)
}

func (m *LifecycleManager) start(ctx context.Context, progress func(string)) (result error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	plan := BuildRuntimePlan(m.configDir, m.values)
	m.emitLifecycleLocked(lifecyclelog.Event{
		Level: lifecyclelog.LevelInfo, Event: "operation.started",
		Message: "runtime start requested", Component: "runtime", Phase: "starting",
		Fields: map[string]any{"backend": plan.Backend, "service_mode": plan.ServiceMode, "runner_type": plan.RunnerType},
	})
	defer func() {
		if result != nil {
			m.emitLifecycleLocked(lifecyclelog.Event{
				Level: lifecyclelog.LevelError, Event: "operation.failed",
				Message: "runtime start failed", Component: "runtime", Phase: "failed",
				Error: result.Error(),
			})
			return
		}
		m.emitLifecycleLocked(lifecyclelog.Event{
			Level: lifecyclelog.LevelInfo, Event: "operation.succeeded",
			Message: "runtime start completed", Component: "runtime", Phase: "running",
		})
	}()
	if plan.Backend == DefaultHostBackend {
		if runnerAddressReachable(m.values, 500*time.Millisecond) {
			emitProgress(progress, "Existing host runner is already reachable.")
			m.status.RunnerRunning = true
			m.status.LastStartedAt = time.Now()
		} else if m.cmd == nil {
			emitProgress(progress, "Starting detached host runner process.")
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
		emitProgress(progress, "Writing docker-compose.yaml.")
		if err := WriteComposeFile(m.configDir, m.values); err != nil {
			m.status.LastError = err.Error()
			return err
		}
		emitProgress(progress, "Stopping stale Docker services.")
		if err := m.stopStaleComposeServicesLocked(ctx, plan); err != nil {
			m.status.LastError = err.Error()
			return err
		}
		pullServices := composePullServices(plan.ComposeServices, m.values["RUNNER_IMAGE_PULL_POLICY"])
		if !containsService(pullServices, "runner") && containsService(plan.ComposeServices, "runner") {
			emitProgress(progress, "Using the configured local runner image without pulling it.")
		}
		if len(pullServices) > 0 {
			emitProgress(progress, "Pulling Docker images. Large runner images can take several minutes.")
			pullArgs := composeArgs(plan, "pull")
			pullArgs = append(pullArgs, pullServices...)
			if _, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: pullArgs, Env: composeProgressEnv(), Stream: progress}); err != nil {
				m.status.LastError = err.Error()
				return err
			}
		}
		emitProgress(progress, "Starting Docker services.")
		composeStartedAt := time.Now()
		args := composeArgs(plan, "up", "-d", "--pull", "never")
		args = append(args, plan.ComposeServices...)
		if _, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: args, Env: composeProgressEnv(), Stream: progress}); err != nil {
			m.status.LastError = err.Error()
			return err
		}
		if plan.ServiceMode == "auto" {
			tunnelStartedAt, err := m.composeServiceStartedAtLocked(ctx, plan, "tunnel")
			if err != nil {
				m.status.LastError = err.Error()
				return err
			}
			composeStartedAt = tunnelStartedAt
		}
		m.startComposeLogFollowerLocked(plan)
		m.status.ComposeRunning = true
		m.status.LastStartedAt = composeStartedAt
	}

	m.status.PublicURL = resolvedRunnerPublicURL(m.values, "")
	return nil
}

func composePullServices(services []string, runnerPullPolicy string) []string {
	pullServices := append([]string(nil), services...)
	if defaultIfEmpty(runnerPullPolicy, DefaultRunnerImagePullPolicy) != "never" {
		return pullServices
	}
	filtered := pullServices[:0]
	for _, service := range pullServices {
		if service != "runner" {
			filtered = append(filtered, service)
		}
	}
	return filtered
}

func (m *LifecycleManager) composeServiceStartedAtLocked(ctx context.Context, plan RuntimePlan, service string) (time.Time, error) {
	args := composeArgs(plan, "ps", "-q", service)
	output, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: args})
	if err != nil {
		return time.Time{}, fmt.Errorf("resolve %s container: %w", service, err)
	}
	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		return time.Time{}, fmt.Errorf("resolve %s container: no running container found", service)
	}
	output, err = m.runner.Run(ctx, CommandSpec{
		Name: "docker",
		Args: []string{"inspect", "--format", "{{.State.StartedAt}}", containerID},
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("inspect %s container start time: %w", service, err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(output)))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s container start time: %w", service, err)
	}
	return startedAt, nil
}

func emitProgress(progress func(string), message string) {
	if progress != nil && strings.TrimSpace(message) != "" {
		progress(message)
	}
}

func composeProgressEnv() []string {
	return append(os.Environ(), "COMPOSE_PROGRESS=plain", "DOCKER_CLI_HINTS=false")
}

func (m *LifecycleManager) stopStaleComposeServicesLocked(ctx context.Context, plan RuntimePlan) error {
	staleServices := staleComposeServices(plan.ComposeServices)
	if len(staleServices) == 0 {
		return nil
	}
	args := composeArgs(plan, "rm", "-f", "-s")
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

func (m *LifecycleManager) Stop(ctx context.Context) (result error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	plan := BuildRuntimePlan(m.configDir, m.values)
	m.emitLifecycleLocked(lifecyclelog.Event{
		Level: lifecyclelog.LevelInfo, Event: "operation.started",
		Message: "runtime stop requested", Component: "runtime", Phase: "stopping",
		Fields: map[string]any{"backend": plan.Backend, "service_mode": plan.ServiceMode},
	})
	defer func() {
		if result != nil {
			m.emitLifecycleLocked(lifecyclelog.Event{
				Level: lifecyclelog.LevelError, Event: "operation.failed",
				Message: "runtime stop failed", Component: "runtime", Phase: "failed",
				Error: result.Error(),
			})
			return
		}
		m.emitLifecycleLocked(lifecyclelog.Event{
			Level: lifecyclelog.LevelInfo, Event: "operation.succeeded",
			Message: "runtime stop completed", Component: "runtime", Phase: "stopped",
		})
	}()
	if plan.Backend == DefaultHostBackend {
		if err := m.stopHostRunnerLocked(ctx); err != nil {
			m.status.LastError = err.Error()
			return err
		}
	}

	if len(plan.ComposeServices) > 0 {
		m.stopComposeLogFollowerLocked()
		args := composeArgs(plan, "stop")
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
		Args: composeArgs(plan, "down", "--remove-orphans"),
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
	args := composeArgs(plan, "logs", "-f", "--tail", "80")
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
	return m.UpgradeRunnerImage(ctx, nil)
}

// UpgradeRunnerImage replaces the configured runner image and brings the
// runtime back up. Progress receives both lifecycle milestones and Docker's
// plain-text pull output.
func (m *LifecycleManager) UpgradeRunnerImage(ctx context.Context, progress func(string)) error {
	m.mu.Lock()
	image := strings.TrimSpace(m.values["RUNNER_IMAGE"])
	pullPolicy := defaultIfEmpty(m.values["RUNNER_IMAGE_PULL_POLICY"], DefaultRunnerImagePullPolicy)
	plan := BuildRuntimePlan(m.configDir, m.values)
	m.mu.Unlock()
	if image == "" {
		return fmt.Errorf("RUNNER_IMAGE is required")
	}
	if !containsService(plan.ComposeServices, "runner") {
		return fmt.Errorf("runner image upgrade requires the container backend")
	}
	if pullPolicy == "never" {
		return fmt.Errorf("runner image upgrade is disabled when RUNNER_IMAGE_PULL_POLICY=never")
	}
	oldImageID, _ := m.runnerImageID(ctx, image)
	emitProgress(progress, "Checking for a newer runner image: "+image)
	if _, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: []string{"pull", image}, Env: composeProgressEnv(), Stream: progress}); err != nil {
		m.setLastError(err)
		return err
	}
	newImageID, err := m.runnerImageID(ctx, image)
	if err != nil {
		m.setLastError(err)
		return err
	}
	stopServices := []string{"runner"}
	stopMessage := "Stopping the runner container while keeping network services online."
	if plan.ServiceMode == "auto" {
		stopServices = append(stopServices, "tunnel")
		stopMessage = "Stopping the runner and quick tunnel while keeping the reverse proxy online."
	}
	emitProgress(progress, stopMessage)
	stopArgs := composeArgs(plan, "stop")
	stopArgs = append(stopArgs, stopServices...)
	if _, err := m.runner.Run(ctx, CommandSpec{
		Name:   "docker",
		Args:   stopArgs,
		Env:    composeProgressEnv(),
		Stream: progress,
	}); err != nil {
		m.setLastError(err)
		return err
	}
	emitProgress(progress, "Removing the stopped runner container.")
	if _, err := m.runner.Run(ctx, CommandSpec{
		Name:   "docker",
		Args:   composeArgs(plan, "rm", "-f", "-s", "runner"),
		Env:    composeProgressEnv(),
		Stream: progress,
	}); err != nil {
		m.setLastError(err)
		return err
	}
	if oldImageID != "" && oldImageID != newImageID {
		emitProgress(progress, "Deleting the superseded runner image: "+oldImageID)
		if _, err := m.runner.Run(ctx, CommandSpec{Name: "docker", Args: []string{"image", "rm", "-f", oldImageID}, Env: composeProgressEnv(), Stream: progress}); err != nil {
			m.setLastError(err)
			return err
		}
	} else {
		emitProgress(progress, "The configured runner image is already current; keeping its cached layers.")
	}
	emitProgress(progress, "Restarting the runner and Docker services.")
	if err := m.StartWithProgress(ctx, progress); err != nil {
		return err
	}
	emitProgress(progress, "Runner image upgrade complete.")
	return nil
}

func (m *LifecycleManager) runnerImageID(ctx context.Context, image string) (string, error) {
	output, err := m.runner.Run(ctx, CommandSpec{
		Name: "docker",
		Args: []string{"image", "inspect", "--format", "{{.Id}}", image},
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func containsService(services []string, target string) bool {
	for _, service := range services {
		if service == target {
			return true
		}
	}
	return false
}

func (m *LifecycleManager) setLastError(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	m.status.LastError = err.Error()
	m.mu.Unlock()
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

func (m *LifecycleManager) emitLifecycleLocked(event lifecyclelog.Event) {
	if m.lifecycle == nil {
		return
	}
	if err := m.lifecycle.Emit(event); err != nil {
		m.status.LastError = "lifecycle log: " + err.Error()
	}
}

func (m *LifecycleManager) Status(ctx context.Context) RuntimeStatus {
	m.mu.Lock()
	status := m.status
	values := cloneValues(m.values)
	runner := m.runner
	configDir := m.configDir
	m.mu.Unlock()

	// Test/embedded runners are command fakes; their in-memory status remains
	// authoritative. The production ExecRunner always observes the host before
	// rendering state so a restarted dashboard can adopt an existing runtime.
	if _, ok := runner.(ExecRunner); !ok {
		return status
	}
	observed := observeRuntime(ctx, runner, configDir, values)
	status.Observed = true
	status.ObservedAt = time.Now().UTC()
	status.RunnerRunning = observed.runnerRunning
	status.ComposeRunning = observed.composeRunning
	status.DeviceReady = observed.deviceReady
	if observed.err != nil {
		status.LastError = observed.err.Error()
	}
	m.mu.Lock()
	m.status = status
	m.mu.Unlock()
	return status
}

type observedRuntime struct {
	runnerRunning  bool
	composeRunning bool
	deviceReady    bool
	err            error
}

func observeRuntime(ctx context.Context, runner Runner, configDir string, values Values) observedRuntime {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	plan := BuildRuntimePlan(configDir, values)
	result := observedRuntime{deviceReady: configuredDeviceReady(ctx, values)}
	if plan.Backend == DefaultHostBackend {
		result.runnerRunning = runnerAddressReachable(values, 500*time.Millisecond)
	}
	if len(plan.ComposeServices) == 0 {
		return result
	}
	output, err := runner.Run(ctx, CommandSpec{Name: "docker", Args: composeArgs(plan, "ps", "--format", "json")})
	if err != nil {
		result.err = fmt.Errorf("observe compose runtime: %w", err)
		return result
	}
	for _, line := range bytes.Split(output, []byte("\n")) {
		var row struct {
			Service string `json:"Service"`
			State   string `json:"State"`
		}
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		if strings.EqualFold(row.State, "running") {
			result.composeRunning = true
			if row.Service == "runner" || row.Service == "runner_host" {
				result.runnerRunning = true
			}
		}
	}
	return result
}

func configuredDeviceReady(ctx context.Context, values Values) bool {
	serial := strings.TrimSpace(values["CREDIMI_RUNNER_SERIAL"])
	if serial == "" {
		return true
	}
	output, err := exec.CommandContext(ctx, "adb", "-s", serial, "get-state").Output()
	return err == nil && strings.TrimSpace(string(output)) == "device"
}

func (m *LifecycleManager) Logs(ctx context.Context, tail int) ([]LogLine, error) {
	return m.logs(ctx, tail, nil)
}

// TunnelLogs returns only quick-tunnel service output. URL discovery must not
// scan noisy runner/emulator logs because doing so can exhaust its deadline.
func (m *LifecycleManager) TunnelLogs(ctx context.Context, tail int) ([]LogLine, error) {
	return m.logs(ctx, tail, []string{"tunnel"})
}

func (m *LifecycleManager) logs(ctx context.Context, tail int, services []string) ([]LogLine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan := BuildRuntimePlan(m.configDir, m.values)
	args := composeLogArgs(plan, tail, m.status.LastStartedAt)
	args = append(args, services...)
	output, err := m.runner.Run(ctx, CommandSpec{
		Name: "docker",
		Args: args,
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
	args := composeArgs(plan, "logs")
	includeHistory := tail < 0
	if includeHistory {
		tail = -tail
	}
	if !since.IsZero() && !includeHistory {
		args = append(args, "--since", since.UTC().Format(time.RFC3339))
	}
	args = append(args, "--tail", fmt.Sprintf("%d", tail))
	return args
}
