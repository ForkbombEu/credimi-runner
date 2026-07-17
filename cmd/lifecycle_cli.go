package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	"github.com/forkbombeu/credimi-runner/internal/lifecyclelog"
	"github.com/spf13/cobra"
)

var lifecycleStatusCmd = &cobra.Command{Use: "status", Short: "Show dashboard and runner lifecycle status", RunE: runLifecycleStatus}
var lifecycleRuntimeCmd = &cobra.Command{Use: "runtime", Short: "Control the configured runner runtime"}
var lifecycleRuntimeActionCmd = func(name string) *cobra.Command {
	return &cobra.Command{Use: name, Short: strings.Title(name) + " the runner runtime", RunE: func(cmd *cobra.Command, args []string) error {
		return runLifecycleRuntimeAction(cmd, name)
	}}
}
var lifecycleDashboardCmd = &cobra.Command{Use: "dashboard", Short: "Control the dashboard process"}
var lifecycleDashboardStopCmd = &cobra.Command{Use: "stop", Short: "Stop the dashboard process", RunE: runLifecycleDashboardStop}
var lifecycleLogLines int
var lifecycleLogOutput string
var lifecycleLogCmd = &cobra.Command{Use: "lifecycle-log", Short: "Inspect the bounded lifecycle diagnostic log"}
var lifecycleLogPathCmd = &cobra.Command{Use: "path", Short: "Print the lifecycle log path", RunE: func(cmd *cobra.Command, args []string) error { cmd.Println(lifecycleLogPath()); return nil }}
var lifecycleLogTailCmd = &cobra.Command{Use: "tail", Short: "Print recent lifecycle events", RunE: runLifecycleLogTail}
var lifecycleLogExportCmd = &cobra.Command{Use: "export", Short: "Export a sanitized Markdown diagnostic report", RunE: runLifecycleLogExport}

func init() {
	lifecycleRuntimeCmd.AddCommand(lifecycleRuntimeActionCmd("start"), lifecycleRuntimeActionCmd("stop"), lifecycleRuntimeActionCmd("restart"), lifecycleRuntimeActionCmd("down"))
	lifecycleDashboardCmd.AddCommand(lifecycleDashboardStopCmd)
	lifecycleLogTailCmd.Flags().IntVar(&lifecycleLogLines, "lines", 100, "Number of lifecycle events")
	lifecycleLogExportCmd.Flags().IntVar(&lifecycleLogLines, "lines", 500, "Number of lifecycle events")
	lifecycleLogExportCmd.Flags().StringVar(&lifecycleLogOutput, "output", "", "Write report to this path instead of stdout")
	lifecycleLogCmd.AddCommand(lifecycleLogPathCmd, lifecycleLogTailCmd, lifecycleLogExportCmd)
	rootCmd.AddCommand(lifecycleStatusCmd, lifecycleRuntimeCmd, lifecycleDashboardCmd, lifecycleLogCmd)
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
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cmd.Println("Dashboard: stopped")
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	if err := controller.Probe(ctx, metadata); err != nil {
		cmd.Printf("Dashboard: stale (pid %d, %s)\n", metadata.PID, err)
		return nil
	}
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

func runLifecycleRuntimeAction(cmd *cobra.Command, action string) error {
	metadata, err := controller.ReadMetadata(lifecycleConfigDir())
	if err != nil {
		return fmt.Errorf("dashboard is not running: %w", err)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
	defer cancel()
	if err := controller.Probe(ctx, metadata); err != nil {
		return fmt.Errorf("dashboard is not reachable: %w", err)
	}
	var snapshot controller.Snapshot
	if err := postLifecycleJSON(ctx, controllerBaseURL(metadata)+"/api/controller/runtime/"+action, &snapshot); err != nil {
		return err
	}
	cmd.Printf("Operation %s queued: %s\n", snapshot.ID, snapshot.Message)
	return nil
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
	if err := syscall.Kill(metadata.PID, syscall.SIGTERM); err != nil {
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
