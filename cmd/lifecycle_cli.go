package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/internal/lifecyclelog"
	"github.com/spf13/cobra"
)

var lifecycleStatusCmd = &cobra.Command{Use: "status", Short: "Show dashboard and runner lifecycle status", Hidden: true, RunE: runLifecycleStatus}
var lifecycleRunnerCmd = &cobra.Command{Use: "runner", Short: "Control the configured runner", Hidden: true}
var lifecycleRunnerActionCmd = func(name string) *cobra.Command {
	title := strings.ToUpper(name[:1]) + name[1:]
	return &cobra.Command{Use: name, Short: title + " the runner", RunE: func(cmd *cobra.Command, args []string) error {
		return runLifecycleRuntimeAction(cmd, name)
	}}
}
var lifecycleDashboardCmd = &cobra.Command{Use: "dashboard", Short: "Control the dashboard process", Hidden: true}
var lifecycleDashboardStopCmd = &cobra.Command{Use: "stop", Short: "Stop the dashboard process", RunE: runLifecycleDashboardStop}
var lifecycleDashboardStatusCmd = &cobra.Command{Use: "status", Short: "Show dashboard status", RunE: runLifecycleStatus}
var lifecycleDashboardOpenCmd = &cobra.Command{Use: "open", Short: "Open the running dashboard when a local display is available", RunE: runLifecycleDashboardOpen}
var lifecycleRunnerStatusCmd = &cobra.Command{Use: "status", Short: "Show runner status", RunE: runLifecycleStatus}
var lifecycleLogLines int
var lifecycleLogOutput string
var lifecycleOperationPollInterval = 250 * time.Millisecond
var lifecycleRuntimeExecutable = os.Executable
var lifecycleKill = syscall.Kill
var lifecycleRuntimeWaitReady func(context.Context, dashboardruntime.Values) error
var lifecycleRuntimeManagerFactory = func(binaryPath, configDir string, values dashboardruntime.Values) dashboardruntime.Manager {
	return dashboardruntime.NewLifecycleManager(binaryPath, configDir, values, nil)
}
var lifecycleLogCmd = &cobra.Command{Use: "lifecycle-log", Short: "Inspect the bounded lifecycle diagnostic log", Hidden: true}
var lifecycleLogPathCmd = &cobra.Command{Use: "path", Short: "Print the lifecycle log path", RunE: func(cmd *cobra.Command, args []string) error { cmd.Println(lifecycleLogPath()); return nil }}
var lifecycleLogTailCmd = &cobra.Command{Use: "tail", Short: "Print recent lifecycle events", RunE: runLifecycleLogTail}
var lifecycleLogExportCmd = &cobra.Command{Use: "export", Short: "Export a sanitized Markdown diagnostic report", RunE: runLifecycleLogExport}

func init() {
	lifecycleRunnerCmd.AddCommand(lifecycleRunnerActionCmd("start"), lifecycleRunnerActionCmd("stop"), lifecycleRunnerActionCmd("restart"), lifecycleRunnerStatusCmd)
	lifecycleDashboardCmd.AddCommand(lifecycleDashboardStopCmd, lifecycleDashboardStatusCmd, lifecycleDashboardOpenCmd)
	lifecycleLogTailCmd.Flags().IntVar(&lifecycleLogLines, "lines", 100, "Number of lifecycle events")
	lifecycleLogExportCmd.Flags().IntVar(&lifecycleLogLines, "lines", 500, "Number of lifecycle events")
	lifecycleLogExportCmd.Flags().StringVar(&lifecycleLogOutput, "output", "", "Write report to this path instead of stdout")
	lifecycleLogCmd.AddCommand(lifecycleLogPathCmd, lifecycleLogTailCmd, lifecycleLogExportCmd)
	// Legacy lifecycle commands remain available to internal tests and old
	// scripts, but are no longer part of the public command tree.
	rootCmd.AddCommand(lifecycleStatusCmd, lifecycleRunnerCmd, lifecycleLogCmd)
}

func lifecycleLogPath() string { return filepath.Join(lifecycleConfigDir(), "lifecycle.jsonl") }

func lifecycleConfigDir() string {
	if strings.TrimSpace(dashboardConfigDir) != "" {
		return dashboardConfigDir
	}
	return dashboard.ConfigDir()
}

func runLifecycleStatus(cmd *cobra.Command, args []string) error {
	metadata, err := controller.ReadMetadata(lifecycleConfigDir())
	dashboardState := "Dashboard: stopped"
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	if err == nil && controller.Probe(ctx, metadata) == nil {
		cmd.Printf("Dashboard: running at %s (pid %d)\n", metadata.PublicURL, metadata.PID)
		base := controllerBaseURL(metadata)
		var payload struct {
			Runtime   map[string]any `json:"runtime"`
			Operation any            `json:"operation"`
		}
		if err := getLifecycleJSON(ctx, base+"/api/controller/status", &payload); err == nil {
			encoded, _ := json.Marshal(payload)
			cmd.Printf("Lifecycle: %s\n", encoded)
		}
		return nil
	}
	if err == nil {
		dashboardState = fmt.Sprintf("Dashboard: unavailable (pid %d)", metadata.PID)
	}
	cmd.Println(dashboardState)
	manager, _, closeManager, err := lifecycleDirectManager()
	if err != nil {
		if errors.Is(err, errLifecycleConfigMissing) {
			cmd.Println("Runner: not configured")
			return nil
		}
		return err
	}
	defer closeManager()
	status := manager.Status(ctx)
	if status.RunnerRunning || status.ComposeRunning {
		cmd.Println("Runner: running")
		return nil
	}
	cmd.Println("Runner: stopped")
	return nil
}

func runLifecycleRuntimeAction(cmd *cobra.Command, action string) error {
	metadata, err := controller.ReadMetadata(lifecycleConfigDir())
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	defer cancel()
	if err == nil && controller.Probe(ctx, metadata) == nil {
		return runLifecycleDashboardAction(ctx, cmd, controllerBaseURL(metadata), action)
	}
	return runLifecycleDirectAction(ctx, cmd, action)
}

func runLifecycleDashboardAction(ctx context.Context, cmd *cobra.Command, baseURL, action string) error {
	return runLifecycleDashboardEndpointAction(ctx, cmd, baseURL, baseURL+"/api/controller/runtime/"+action, "Runner "+lifecycleActionPastTense(action), "runner "+action)
}

func runLifecycleDashboardEndpointAction(ctx context.Context, cmd *cobra.Command, baseURL, endpoint, successLabel, failureLabel string) error {
	var snapshot controller.Snapshot
	if err := postLifecycleJSON(ctx, endpoint, &snapshot); err != nil {
		return err
	}
	completed, err := waitForLifecycleOperation(ctx, baseURL, snapshot.ID)
	if err != nil {
		return fmt.Errorf("%s did not complete: %w", failureLabel, err)
	}
	if completed.Phase != controller.PhaseSucceeded {
		message := strings.TrimSpace(completed.Error)
		if message == "" {
			message = strings.TrimSpace(completed.Message)
		}
		if message == "" {
			message = "operation did not succeed"
		}
		return fmt.Errorf("%s failed: %s", failureLabel, message)
	}
	cmd.Printf("%s successfully.\n", successLabel)
	return nil
}

var errLifecycleConfigMissing = errors.New("runner configuration is missing")

func lifecycleDirectManager() (dashboardruntime.Manager, dashboardruntime.Values, func() error, error) {
	configDir := lifecycleConfigDir()
	store, err := dashboardruntime.LoadStore(configDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load runner configuration: %w", err)
	}
	if !store.Exists() {
		return nil, nil, nil, errLifecycleConfigMissing
	}
	values, err := dashboardruntime.NormalizeValues(store.Snapshot(), runtime.GOOS)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("normalize runner configuration: %w", err)
	}
	binaryPath, err := lifecycleRuntimeExecutable()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve runner executable: %w", err)
	}
	manager := lifecycleRuntimeManagerFactory(binaryPath, configDir, values)
	closeManager := func() error {
		if closer, ok := manager.(interface{ Close() error }); ok {
			return closer.Close()
		}
		return nil
	}
	return manager, values, closeManager, nil
}

func runLifecycleDirectAction(ctx context.Context, cmd *cobra.Command, action string) error {
	closeVerboseLog, err := enableVerboseLog(cmd, lifecycleConfigDir())
	if err != nil {
		return err
	}
	defer closeVerboseLog()
	lease, err := controller.Acquire(lifecycleConfigDir())
	if err != nil {
		if errors.Is(err, controller.ErrAlreadyRunning) {
			return errors.New("dashboard controller is active but could not be verified; refusing direct runtime control")
		}
		return err
	}
	defer lease.Close()

	manager, values, closeManager, err := lifecycleDirectManager()
	if err != nil {
		return err
	}
	defer closeManager()
	lifecycle := controller.RuntimeLifecycle{Manager: manager, Values: values, GOOS: runtime.GOOS, WaitReady: lifecycleRuntimeWaitReady}
	progress := func(message string) { cmd.Println(message) }

	switch action {
	case "start":
		if err := lifecycle.Start(ctx, progress); err != nil {
			return fmt.Errorf("runner start failed: %w", err)
		}
	case "stop":
		if err := lifecycle.Stop(ctx); err != nil {
			return fmt.Errorf("runner stop failed: %w", err)
		}
	case "restart":
		if err := lifecycle.Restart(ctx, progress); err != nil {
			return fmt.Errorf("runner restart failed: %w", err)
		}
	default:
		return fmt.Errorf("unsupported runtime action %q", action)
	}
	cmd.Printf("Runner %s successfully.\n", lifecycleActionPastTense(action))
	return nil
}

func waitForLifecycleOperation(ctx context.Context, baseURL, operationID string) (controller.Snapshot, error) {
	if strings.TrimSpace(operationID) == "" {
		return controller.Snapshot{}, errors.New("dashboard returned an operation without an ID")
	}
	poll := func() (controller.Snapshot, bool, error) {
		var snapshot controller.Snapshot
		if err := getLifecycleJSON(ctx, baseURL+"/api/controller/operations/"+operationID, &snapshot); err != nil {
			return controller.Snapshot{}, false, err
		}
		switch snapshot.Phase {
		case controller.PhaseSucceeded, controller.PhaseFailed, controller.PhaseCancelled:
			return snapshot, true, nil
		default:
			return snapshot, false, nil
		}
	}
	if snapshot, done, err := poll(); err != nil || done {
		return snapshot, err
	}
	ticker := time.NewTicker(lifecycleOperationPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return controller.Snapshot{}, ctx.Err()
		case <-ticker.C:
			snapshot, done, err := poll()
			if err != nil || done {
				return snapshot, err
			}
		}
	}
}

func lifecycleActionPastTense(action string) string {
	switch action {
	case "start":
		return "started"
	case "stop":
		return "stopped"
	case "restart":
		return "restarted"
	default:
		return action + "ed"
	}
}

func runLifecycleDashboardOpen(cmd *cobra.Command, args []string) error {
	metadata, err := controller.ReadMetadata(lifecycleConfigDir())
	if err != nil {
		return fmt.Errorf("dashboard is not running: %w", err)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	if err := controller.Probe(ctx, metadata); err != nil {
		return fmt.Errorf("dashboard is not reachable: %w", err)
	}
	if !dashboardCanOpenBrowser() {
		cmd.Printf("Dashboard is running at %s (no local graphical display available)\n", metadata.PublicURL)
		return nil
	}
	return openDashboardBrowser(metadata.PublicURL)
}

func controllerBaseURL(metadata controller.Metadata) string {
	return strings.TrimSuffix(metadata.ProbeURL, "/internal/controller/identity")
}

func runLifecycleLogTail(cmd *cobra.Command, args []string) error {
	events, err := lifecyclelog.Tail(lifecycleLogPath(), lifecycleLogLines)
	if err != nil {
		return err
	}
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		cmd.Println(string(encoded))
	}
	return nil
}

func runLifecycleLogExport(cmd *cobra.Command, args []string) error {
	report, err := lifecyclelog.ExportMarkdown(lifecycleLogPath(), lifecycleLogLines)
	if err != nil {
		return err
	}
	if strings.TrimSpace(lifecycleLogOutput) == "" {
		cmd.Print(report)
		return nil
	}
	if err := os.WriteFile(lifecycleLogOutput, []byte(report), 0o600); err != nil {
		return err
	}
	cmd.Printf("Lifecycle diagnostic report written to %s\n", lifecycleLogOutput)
	return nil
}

func runLifecycleDashboardStop(cmd *cobra.Command, args []string) error {
	metadata, err := controller.ReadMetadata(lifecycleConfigDir())
	if err != nil {
		return fmt.Errorf("dashboard is not running: %w", err)
	}
	if metadata.PID <= 1 {
		return errors.New("refusing to stop invalid dashboard PID")
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	if err := controller.Probe(ctx, metadata); err != nil {
		return fmt.Errorf("refusing to stop an unverified dashboard controller: %w", err)
	}
	if err := lifecycleKill(metadata.PID, syscall.SIGTERM); err != nil {
		return err
	}
	cmd.Printf("Dashboard stop requested (pid %d)\n", metadata.PID)
	return nil
}

func getLifecycleJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %s", endpoint, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func postLifecycleJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: %s", endpoint, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
