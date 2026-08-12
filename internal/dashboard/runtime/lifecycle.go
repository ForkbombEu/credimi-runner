package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/controller/driver"
	"github.com/forkbombeu/credimi-runner/internal/lifecyclelog"
)

type Manager interface {
	Start(context.Context) error
	Stop(context.Context) error
	Restart(context.Context) error
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
	Output   io.Writer
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
			output := io.Writer(logFile)
			if spec.Output != nil {
				output = io.MultiWriter(logFile, spec.Output)
			}
			cmd.Stdout = output
			cmd.Stderr = output
		}
	} else if spec.Output != nil {
		cmd.Stdout = spec.Output
		cmd.Stderr = spec.Output
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
	configDir string
	goos      string
	values    Values
	runner    Runner
	logCmd    *exec.Cmd
	logDone   chan struct{}
	status    RuntimeStatus
	lifecycle *lifecyclelog.Logger
	verbose   *verboseLog
}

var quickTunnelHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
} = http.DefaultClient

var publicRuntimeHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
} = http.DefaultClient

const publicURLProbeTimeout = 5 * time.Second

var (
	ErrPublicOriginUnavailable = errors.New("public runner origin unavailable")
	ErrPublicEndpointIdentity  = errors.New("public endpoint belongs to another runner")
)

func NewLifecycleManager(binary, configDir string, values Values, runner Runner) *LifecycleManager {
	return NewLifecycleManagerForOS(binary, configDir, values, runner, runtime.GOOS)
}

func NewLifecycleManagerForOS(binary, configDir string, values Values, runner Runner, goos string) *LifecycleManager {
	_ = binary // native application ownership no longer belongs to this manager
	if runner == nil {
		runner = ExecRunner{}
	}
	var lifecycle *lifecyclelog.Logger
	if strings.TrimSpace(configDir) != "" {
		lifecycle, _ = lifecyclelog.New(filepath.Join(configDir, "lifecycle.jsonl"), lifecyclelog.Options{})
	}
	return &LifecycleManager{
		configDir: configDir,
		goos:      goos,
		values:    cloneValues(values),
		runner:    runner,
		lifecycle: lifecycle,
		verbose:   openVerboseLog(),
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
	var err error
	if m.lifecycle != nil {
		err = m.lifecycle.Close()
		m.lifecycle = nil
	}
	return errors.Join(err, m.verbose.Close())
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

	plan := BuildRuntimePlanForOS(m.configDir, m.values, m.goos)
	m.emitLifecycleLocked(lifecyclelog.Event{
		Level: lifecyclelog.LevelInfo, Event: "operation.started",
		Message: "runtime start requested", Component: "runtime", Phase: "starting",
		Fields: map[string]any{"backend": plan.Backend, "service_mode": plan.ServiceMode},
	})
	m.verbose.Printf("runtime start requested")
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
	if len(plan.ComposeServices) > 0 {
		emitProgress(progress, "Writing docker-compose.yaml.")
		if err := WriteComposeFileForOS(m.configDir, m.values, m.goos); err != nil {
			m.status.LastError = err.Error()
			return err
		}
		spec, specErr := sharedRunnerSpec(m.values, m.goos)
		if specErr != nil && containsService(plan.ComposeServices, "runner") {
			m.status.LastError = specErr.Error()
			return specErr
		}
		pullServices := composePullServices(plan.ComposeServices, spec.PullPolicy)
		if !containsService(pullServices, "runner") && containsService(plan.ComposeServices, "runner") {
			emitProgress(progress, "Using the configured local runner image without pulling it.")
		}
		if len(pullServices) > 0 {
			emitProgress(progress, "Pulling Docker images. Large runner images can take several minutes.")
			pullSpec := m.composeSpec(plan, "pull")
			pullArgs := pullSpec.Args
			pullArgs = append(pullArgs, pullServices...)
			pullSpec.Args = pullArgs
			pullSpec.Stream = m.verboseProgress(progress)
			if _, err := m.run(ctx, pullSpec); err != nil {
				m.status.LastError = err.Error()
				return err
			}
		}
		emitProgress(progress, "Starting Docker services.")
		composeStartedAt := time.Now()
		// `up --remove-orphans` removes services from a previous topology while
		// only naming services declared by the freshly generated Compose model.
		// Calling `compose rm caddy` after rendering a runner-only bootstrap file
		// is invalid because Compose rejects unknown service names.
		upSpec := m.composeSpec(plan, "up", "-d", "--remove-orphans", "--pull", "never")
		args := upSpec.Args
		args = append(args, plan.ComposeServices...)
		upSpec.Args = args
		upSpec.Stream = m.verboseProgress(progress)
		if _, err := m.run(ctx, upSpec); err != nil {
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
	if defaultIfEmpty(runnerPullPolicy, DefaultAndroidPullPolicy) != "never" {
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
	output, err := m.run(ctx, m.composeSpec(plan, "ps", "-q", service))
	if err != nil {
		return time.Time{}, fmt.Errorf("resolve %s container: %w", service, err)
	}
	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		return time.Time{}, fmt.Errorf("resolve %s container: no running container found", service)
	}
	output, err = m.run(ctx, CommandSpec{
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

func commandError(spec CommandSpec, output []byte, err error) error {
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	command := strings.TrimSpace(strings.Join(append([]string{spec.Name}, spec.Args...), " "))
	if detail == "" {
		return fmt.Errorf("%s: %w", command, err)
	}
	return fmt.Errorf("%s: %s: %w", command, detail, err)
}

func (m *LifecycleManager) run(ctx context.Context, spec CommandSpec) ([]byte, error) {
	output, err := m.runner.Run(ctx, spec)
	return output, commandError(spec, output, err)
}

func composeProgressEnv() []string {
	return append(os.Environ(), "COMPOSE_PROGRESS=plain", "DOCKER_CLI_HINTS=false")
}

func (m *LifecycleManager) composeSpec(plan RuntimePlan, command ...string) CommandSpec {
	return CommandSpec{
		Name: "docker",
		Args: composeArgs(plan, command...),
		Env:  composeEnv(m.values, plan, m.goos),
	}
}

func (m *LifecycleManager) Stop(ctx context.Context) (result error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	plan := BuildRuntimePlanForOS(m.configDir, m.values, m.goos)
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
	if len(plan.ComposeServices) > 0 {
		m.stopComposeLogFollowerLocked()
		services := composeExposureServices(plan)
		if len(services) > 0 {
			args := append([]string{"stop"}, services...)
			if _, err := m.run(ctx, m.composeSpec(plan, args...)); err != nil {
				m.status.LastError = err.Error()
				return err
			}
			if err := m.verifyComposeStoppedLocked(ctx, plan, services...); err != nil {
				m.status.LastError = err.Error()
				return err
			}
		}
	}

	// Stop is an operational stop, not destruction of the unified runner
	// container. Dashboard/GoA stays available so a later Start can restore the
	// execution and exposure services without recreating the control plane.
	m.status.RunnerRunning = containsService(plan.ComposeServices, "runner")
	m.status.ComposeRunning = false
	m.status.PublicURL = ""
	return nil
}

// RecreateRunner replaces only the unified runner service. Exposure services
// and their public URL are deliberately left untouched so a configuration
// change to inner-runner settings does not rotate a healthy tunnel.
func (m *LifecycleManager) RecreateRunner(ctx context.Context, pull bool) (result error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan := BuildRuntimePlanForOS(m.configDir, m.values, m.goos)
	if !containsService(plan.ComposeServices, "runner") {
		return errors.New("runner service recreation requires the container backend")
	}
	if err := WriteComposeFileForOS(m.configDir, m.values, m.goos); err != nil {
		return fmt.Errorf("write runner Compose file: %w", err)
	}
	spec, err := sharedRunnerSpec(m.values, m.goos)
	if err != nil {
		return fmt.Errorf("resolve runner service configuration: %w", err)
	}
	if pull && defaultIfEmpty(spec.PullPolicy, DefaultAndroidPullPolicy) != "never" {
		pullSpec := m.composeSpec(plan, "pull", "runner")
		if _, err := m.run(ctx, pullSpec); err != nil {
			m.status.LastError = err.Error()
			return fmt.Errorf("pull runner image: %w", err)
		}
	}
	m.stopComposeLogFollowerLocked()
	upSpec := m.composeSpec(plan, "up", "-d", "--no-deps", "--force-recreate", "--pull", "never", "runner")
	if _, err := m.run(ctx, upSpec); err != nil {
		m.status.LastError = err.Error()
		return fmt.Errorf("recreate runner service: %w", err)
	}
	startedAt, err := m.composeServiceStartedAtLocked(ctx, plan, "runner")
	if err != nil {
		m.status.LastError = err.Error()
		return fmt.Errorf("verify recreated runner service: %w", err)
	}
	m.startComposeLogFollowerLocked(plan)
	m.status.RunnerRunning = true
	m.status.ComposeRunning = true
	m.status.LastStartedAt = startedAt
	m.status.LastError = ""
	return nil
}

func (m *LifecycleManager) verifyComposeStoppedLocked(ctx context.Context, plan RuntimePlan, services ...string) error {
	args := []string{"ps", "-q"}
	args = append(args, services...)
	output, err := m.run(ctx, m.composeSpec(plan, args...))
	if err != nil {
		return fmt.Errorf("verify managed Compose services stopped: %w", err)
	}
	for _, line := range strings.Fields(string(output)) {
		if looksLikeContainerID(line) {
			return fmt.Errorf("managed Compose service %s is still running after stop", line)
		}
	}
	return nil
}

func composeExposureServices(plan RuntimePlan) []string {
	services := make([]string, 0, len(plan.ComposeServices))
	for _, service := range plan.ComposeServices {
		if service != "runner" {
			services = append(services, service)
		}
	}
	return services
}

func looksLikeContainerID(value string) bool {
	if len(value) < 12 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

// Native application lifecycle is owned by runApplicationRuntime. This
// manager only reconciles Docker and edge services.

// validateReachableHostRunner proves that the process already listening on the
// configured host port is the configured credimi-runner. A TCP connection is
// intentionally insufficient: the port can belong to a stale or unrelated
// process, and adopting it would make later dashboard operations unsafe.
func (m *LifecycleManager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}

// StartLogFollower attaches the owning dashboard process to managed runtime
// logs without starting or recreating any service.
func (m *LifecycleManager) StartLogFollower() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startComposeLogFollowerLocked(BuildRuntimePlanForOS(m.configDir, m.values, m.goos))
}

func (m *LifecycleManager) startComposeLogFollowerLocked(plan RuntimePlan) {
	if m.logCmd != nil || len(plan.ComposeServices) == 0 {
		return
	}
	spec := m.composeSpec(plan, "logs", "-f", "--tail", "80")
	if m.verbose != nil {
		spec.Args = append(spec.Args, "--timestamps")
	}
	spec.Args = append(spec.Args, plan.ComposeServices...)
	m.verbose.Printf("following container logs: docker %s", strings.Join(spec.Args, " "))
	if m.verbose != nil {
		spec.Output = io.MultiWriter(os.Stdout, m.verbose)
	}
	cmd, err := m.runner.Start(context.Background(), spec)
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
	plan := BuildRuntimePlanForOS(m.configDir, m.values, m.goos)
	images := configuredRuntimeImages(m.values, m.goos)
	m.mu.Unlock()
	if !containsService(plan.ComposeServices, "runner") {
		return fmt.Errorf("runner image upgrade requires the container backend")
	}
	if len(images) == 0 {
		return fmt.Errorf("the configured shared runner image is local-only or no container runtime is configured")
	}
	updated := make(map[string][2]string, len(images))
	for _, image := range images {
		oldID, _ := m.runnerImageID(ctx, image)
		emitProgress(progress, "Checking for a newer device runtime image: "+image)
		if _, err := m.run(ctx, CommandSpec{Name: "docker", Args: []string{"pull", image}, Env: composeProgressEnv(), Stream: progress}); err != nil {
			m.setLastError(err)
			return err
		}
		newID, err := m.runnerImageID(ctx, image)
		if err != nil {
			m.setLastError(err)
			return err
		}
		updated[image] = [2]string{oldID, newID}
	}
	stopServices := []string{"runner"}
	stopMessage := "Stopping the runner container while keeping network services online."
	if plan.ServiceMode == "auto" {
		stopServices = append(stopServices, "tunnel")
		stopMessage = "Stopping the runner and quick tunnel while keeping the reverse proxy online."
	}
	emitProgress(progress, stopMessage)
	stopSpec := m.composeSpec(plan, "stop")
	stopArgs := stopSpec.Args
	stopArgs = append(stopArgs, stopServices...)
	stopSpec.Args = stopArgs
	stopSpec.Stream = progress
	if _, err := m.run(ctx, stopSpec); err != nil {
		m.setLastError(err)
		return err
	}
	emitProgress(progress, "Removing the stopped runner container.")
	removeSpec := m.composeSpec(plan, "rm", "-f", "-s", "runner")
	removeSpec.Stream = progress
	if _, err := m.run(ctx, removeSpec); err != nil {
		m.setLastError(err)
		return err
	}
	for image, ids := range updated {
		if ids[0] == "" || ids[0] == ids[1] {
			emitProgress(progress, "Image already current: "+image)
			continue
		}
		emitProgress(progress, "Deleting superseded image for "+image+": "+ids[0])
		if _, err := m.run(ctx, CommandSpec{Name: "docker", Args: []string{"image", "rm", "-f", ids[0]}, Env: composeProgressEnv(), Stream: progress}); err != nil {
			m.setLastError(err)
			return err
		}
	}
	emitProgress(progress, "Restarting the runner and Docker services.")
	if err := m.StartWithProgress(ctx, progress); err != nil {
		return err
	}
	emitProgress(progress, "Device runtime image upgrade complete.")
	return nil
}

// configuredRuntimeImages returns the one image used by the shared container.
// A `never` policy means the operator owns a local image and it must not be
// touched by the common dashboard maintenance action.
func configuredRuntimeImages(values Values, goos string) []string {
	spec, err := sharedRunnerSpec(values, goos)
	if err != nil || spec.Image == "" || spec.PullPolicy == "never" {
		return nil
	}
	return []string{spec.Image}
}

func (m *LifecycleManager) runnerImageID(ctx context.Context, image string) (string, error) {
	output, err := m.run(ctx, CommandSpec{
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
	m.verbose.Printf("lifecycle event=%s level=%s message=%s error=%s", event.Event, event.Level, event.Message, event.Error)
	if m.lifecycle == nil {
		return
	}
	if err := m.lifecycle.Emit(event); err != nil {
		m.status.LastError = "lifecycle log: " + err.Error()
	}
}

func (m *LifecycleManager) verboseProgress(progress func(string)) func(string) {
	return func(message string) {
		m.verbose.Printf("docker: %s", message)
		emitProgress(progress, message)
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
	m.status = mergeObservedRuntimeStatus(m.status, observed, status.ObservedAt)
	status = m.status
	m.mu.Unlock()
	return status
}

// mergeObservedRuntimeStatus applies only values owned by Docker observation.
// Registration can set PublicURL while observation runs, so that field must
// remain owned by the lifecycle operation rather than a stale status snapshot.
func mergeObservedRuntimeStatus(current RuntimeStatus, observed observedRuntime, at time.Time) RuntimeStatus {
	current.Observed = true
	current.ObservedAt = at
	current.RunnerRunning = observed.runnerRunning
	current.ComposeRunning = observed.composeRunning
	current.DeviceReady = observed.deviceReady
	if observed.err != nil {
		current.LastError = observed.err.Error()
	}
	return current
}

type observedRuntime struct {
	runnerRunning  bool
	composeRunning bool
	deviceReady    bool
	err            error
}

func observeRuntime(ctx context.Context, runner Runner, configDir string, values Values) observedRuntime {
	return observeRuntimeForOS(ctx, runner, configDir, values, runtime.GOOS)
}

func observeRuntimeForOS(ctx context.Context, runner Runner, configDir string, values Values, goos string) observedRuntime {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	plan := BuildRuntimePlanForOS(configDir, values, goos)
	result := observedRuntime{deviceReady: configuredDeviceReady(ctx, values)}
	if len(plan.ComposeServices) == 0 {
		return result
	}
	output, err := runner.Run(ctx, CommandSpec{
		Name: "docker",
		Args: composeArgs(plan, "ps", "--format", "json"),
		Env:  composeEnv(values, plan, goos),
	})
	if err != nil {
		result.err = fmt.Errorf("observe compose runtime: %w", err)
		return result
	}
	rows, err := driver.ParseComposePS(output)
	if err != nil {
		result.err = fmt.Errorf("parse compose runtime: %w", err)
		return result
	}
	for _, row := range rows {
		if strings.EqualFold(row.State, "running") {
			result.composeRunning = true
			if row.Service == "runner" {
				result.runnerRunning = true
			}
		}
	}
	return result
}

func configuredDeviceReady(ctx context.Context, values Values) bool {
	inventory, err := ParseRuntimeConfig(values)
	if err != nil {
		return true
	}
	for _, device := range inventory.Devices {
		if !device.Enabled || device.Mode == "" || device.Mode == "no_device" || device.Type == "redroid" || device.Type == "android_emulator" || device.Type == "ios_simulator" {
			continue
		}
		if device.Serial == "" {
			return false
		}
		output, err := exec.CommandContext(ctx, "adb", "-s", device.Serial, "get-state").Output()
		if err != nil || strings.TrimSpace(string(output)) != "device" {
			return false
		}
	}
	return true
}

func (m *LifecycleManager) Logs(ctx context.Context, tail int) ([]LogLine, error) {
	return m.logs(ctx, tail, nil)
}

// QuickTunnelURL reads cloudflared's local structured diagnostics endpoint.
// The endpoint is published only on loopback by the generated Compose file.
func (m *LifecycleManager) QuickTunnelURL(ctx context.Context) (string, error) {
	m.mu.Lock()
	plan := BuildRuntimePlanForOS(m.configDir, m.values, m.goos)
	port := QuickTunnelMetricsPort(plan)
	m.mu.Unlock()
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/quicktunnel", port), nil)
	if err != nil {
		return "", err
	}
	response, err := quickTunnelHTTPClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("query quick tunnel diagnostics: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("quick tunnel diagnostics returned %s", response.Status)
	}
	var payload struct {
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode quick tunnel diagnostics: %w", err)
	}
	return ParseQuickTunnelHostname(payload.Hostname)
}

// VerifyPublicURL proves that the published endpoint reaches this runner,
// rather than merely accepting a hostname from cloudflared diagnostics.
func (m *LifecycleManager) VerifyPublicURL(ctx context.Context, publicURL string) error {
	m.mu.Lock()
	expectedRunnerID := strings.TrimSpace(m.values["CREDIMI_RUNNER_ID"])
	m.mu.Unlock()
	probeCtx, cancel := context.WithTimeout(ctx, publicURLProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, strings.TrimRight(strings.TrimSpace(publicURL), "/")+"/readyz", nil)
	if err != nil {
		return fmt.Errorf("build public runner readiness request: %w", err)
	}
	response, err := publicRuntimeHTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("probe public runner endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusBadGateway || response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusGatewayTimeout {
			return fmt.Errorf("%w: public runner endpoint returned %s", ErrPublicOriginUnavailable, response.Status)
		}
		return fmt.Errorf("public runner endpoint returned %s", response.Status)
	}
	var readiness struct {
		Service  string `json:"service"`
		RunnerID string `json:"runner_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&readiness); err != nil {
		return fmt.Errorf("decode public runner readiness: %w", err)
	}
	if readiness.Service != "credimi-runner" || (expectedRunnerID != "" && readiness.RunnerID != expectedRunnerID) {
		return fmt.Errorf("%w: public endpoint identified runner %q, expected %q", ErrPublicEndpointIdentity, readiness.RunnerID, expectedRunnerID)
	}
	return nil
}

// ParseQuickTunnelHostname validates and normalizes cloudflared's structured
// hostname without accepting an arbitrary public URL from the diagnostics
// channel.
func ParseQuickTunnelHostname(hostname string) (string, error) {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return "", errors.New("quick tunnel hostname is not available yet")
	}
	if strings.Contains(hostname, "/") || strings.Contains(hostname, "://") {
		return "", fmt.Errorf("invalid quick tunnel hostname %q", hostname)
	}
	parsed, err := url.Parse("https://" + hostname)
	if err != nil || parsed.Host != hostname {
		return "", fmt.Errorf("invalid quick tunnel hostname %q", hostname)
	}
	return parsed.String(), nil
}

func (m *LifecycleManager) logs(ctx context.Context, tail int, services []string) ([]LogLine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan := BuildRuntimePlanForOS(m.configDir, m.values, m.goos)
	args := composeLogArgs(plan, tail, m.status.LastStartedAt)
	args = append(args, services...)
	spec := m.composeSpec(plan)
	spec.Args = args
	output, err := m.run(ctx, spec)
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
