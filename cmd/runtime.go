package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/spf13/cobra"
)

var runtimeCmd = &cobra.Command{Use: "runtime", Short: "Control runtime execution through the Dashboard"}

func init() {
	runtimeCmd.AddCommand(runtimeAction("start"), runtimeAction("stop"), runtimeAction("restart"), &cobra.Command{Use: "status", RunE: runRuntimeStatus})
	rootCmd.AddCommand(runtimeCmd)
}
func runtimeAction(action string) *cobra.Command {
	return &cobra.Command{Use: action, RunE: func(cmd *cobra.Command, _ []string) error { return runRuntimeAPIAction(cmd, action) }}
}
func requireRunningController(ctx context.Context) (controller.Metadata, error) {
	metadata, err := controller.ReadMetadata(effectiveConfigDir())
	if err != nil {
		return metadata, fmt.Errorf("Credimi Runner service is not running. Start it with: credimi-runner service start")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := controller.Probe(probeCtx, metadata); err != nil {
		return metadata, fmt.Errorf("Credimi Runner service is not running. Start it with: credimi-runner service start")
	}
	return metadata, nil
}
func runRuntimeAPIAction(cmd *cobra.Command, action string) error {
	metadata, err := requireRunningController(cmd.Context())
	if err != nil {
		return err
	}
	base := controllerBaseURL(metadata)
	var snap controller.Snapshot
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, base+"/api/controller/runtime/"+action, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("runtime %s: %s", action, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	defer cancel()
	done, err := waitForLifecycleOperation(ctx, base, snap.ID)
	if err != nil {
		return err
	}
	if done.Phase != controller.PhaseSucceeded {
		return errors.New(strings.TrimSpace(done.Error))
	}
	cmd.Printf("Runtime %s successfully.\n", action)
	return nil
}
func runRuntimeStatus(cmd *cobra.Command, _ []string) error {
	metadata, err := requireRunningController(cmd.Context())
	if err != nil {
		return err
	}
	var payload struct {
		Runtime json.RawMessage `json:"runtime"`
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	if err := getLifecycleJSON(ctx, controllerBaseURL(metadata)+"/api/controller/status", &payload); err != nil {
		return err
	}
	cmd.Println(string(payload.Runtime))
	return nil
}

func controllerBaseURL(metadata controller.Metadata) string {
	return strings.TrimSuffix(metadata.ProbeURL, "/internal/controller/identity")
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

func waitForLifecycleOperation(ctx context.Context, baseURL, operationID string) (controller.Snapshot, error) {
	if strings.TrimSpace(operationID) == "" {
		return controller.Snapshot{}, errors.New("dashboard returned an operation without an ID")
	}
	for {
		var snapshot controller.Snapshot
		if err := getLifecycleJSON(ctx, baseURL+"/api/controller/operations/"+operationID, &snapshot); err != nil {
			return controller.Snapshot{}, err
		}
		switch snapshot.Phase {
		case controller.PhaseSucceeded, controller.PhaseFailed, controller.PhaseCancelled:
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return controller.Snapshot{}, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
