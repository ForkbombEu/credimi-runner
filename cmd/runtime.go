package cmd

import (
	"context"
	"encoding/json"
	"errors"
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
	client, err := newControllerClient(ctx, effectiveConfigDir())
	if err != nil {
		return controller.Metadata{}, err
	}
	return client.metadata, nil
}
func runRuntimeAPIAction(cmd *cobra.Command, action string) error {
	client, err := newControllerClient(cmd.Context(), effectiveConfigDir())
	if err != nil {
		return err
	}
	var snap controller.Snapshot
	if err := client.postJSON(cmd.Context(), "/api/controller/runtime/"+action, &snap); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	defer cancel()
	done, err := waitForLifecycleOperation(ctx, client, snap.ID)
	if err != nil {
		return err
	}
	if done.Phase != controller.PhaseSucceeded {
		return errors.New(lifecycleFailureMessage(done))
	}
	cmd.Printf("Runtime %s successfully.\n", action)
	return nil
}

func lifecycleFailureMessage(done controller.Snapshot) string {
	message := strings.TrimSpace(done.Error)
	if message == "" {
		message = strings.TrimSpace(done.Message)
	}
	if message == "" {
		message = "runtime operation did not succeed"
	}
	return message
}
func runRuntimeStatus(cmd *cobra.Command, _ []string) error {
	client, err := newControllerClient(cmd.Context(), effectiveConfigDir())
	if err != nil {
		return err
	}
	var payload struct {
		Runtime json.RawMessage `json:"runtime"`
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	if err := client.getJSON(ctx, "/api/controller/status", &payload); err != nil {
		return err
	}
	cmd.Println(string(payload.Runtime))
	return nil
}

func waitForLifecycleOperation(ctx context.Context, client *controllerClient, operationID string) (controller.Snapshot, error) {
	if strings.TrimSpace(operationID) == "" {
		return controller.Snapshot{}, errors.New("dashboard returned an operation without an ID")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var snapshot controller.Snapshot
		if err := client.getJSON(ctx, "/api/controller/operations/"+operationID, &snapshot); err != nil {
			return controller.Snapshot{}, err
		}
		switch snapshot.Phase {
		case controller.PhaseSucceeded, controller.PhaseFailed, controller.PhaseCancelled:
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return controller.Snapshot{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
