package workermanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/forkbombeu/credimi/pkg/workflowengine"
)

var activeMobileActivities atomic.Int64
var mobileActivityStateFile atomic.Value

func init() { mobileActivityStateFile.Store("") }

// ConfigureMobileActivityStateFile publishes the active mobile activity count
// for the outer Linux launcher. The file is deliberately a tiny local state
// boundary; device configuration remains context-scoped and in-memory.
func ConfigureMobileActivityStateFile(path string) {
	mobileActivityStateFile.Store(strings.TrimSpace(path))
	writeMobileActivityState(0)
}

// ActiveMobileActivities returns the number of currently executing device
// activities in this runner process.
func ActiveMobileActivities() int64 { return activeMobileActivities.Load() }

func beginMobileActivity() func() {
	writeMobileActivityState(activeMobileActivities.Add(1))
	return func() { writeMobileActivityState(activeMobileActivities.Add(-1)) }
}

func writeMobileActivityState(count int64) {
	path, _ := mobileActivityStateFile.Load().(string)
	if path == "" {
		return
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".active-mobile-activities-*")
	if err != nil {
		return
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.WriteString(strconv.FormatInt(count, 10) + "\n"); err != nil {
		_ = temporary.Close()
		return
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return
	}
	if err := temporary.Close(); err != nil {
		return
	}
	_ = os.Rename(name, path)
}

type mobileActivityPayload struct {
	DeviceID string `json:"device_id"`
}

func mobileActivityExecutor(activity workflowengine.ExecutableActivity, provider RuntimeConfigProvider) func(context.Context, workflowengine.ActivityInput) (workflowengine.ActivityResult, error) {
	return mobileActivityExecutorWithScope(activity, provider, withMobileEnvironment)
}

func mobileActivityExecutorWithScope(
	activity workflowengine.ExecutableActivity,
	provider RuntimeConfigProvider,
	scope func(context.Context, func(string, ...any) string) context.Context,
) func(context.Context, workflowengine.ActivityInput) (workflowengine.ActivityResult, error) {
	return func(ctx context.Context, input workflowengine.ActivityInput) (workflowengine.ActivityResult, error) {
		if provider == nil {
			return activity.Execute(ctx, input)
		}

		payload, err := workflowengine.DecodePayload[mobileActivityPayload](input.Payload)
		if err != nil {
			return workflowengine.ActivityResult{}, activity.NewMissingOrInvalidPayloadError(err)
		}
		deviceID := strings.TrimPrefix(strings.TrimSpace(payload.DeviceID), "/")
		if deviceID == "" {
			return workflowengine.ActivityResult{}, activity.NewMissingOrInvalidPayloadError(fmt.Errorf("device_id is required for %s", activity.Name()))
		}
		finish := beginMobileActivity()
		defer finish()

		config, err := provider()
		if err != nil {
			return workflowengine.ActivityResult{}, fmt.Errorf("load current runner inventory for device %q: %w", deviceID, err)
		}
		getter, err := config.Environment(deviceID)
		if err != nil {
			return workflowengine.ActivityResult{}, fmt.Errorf("resolve configured device %q for %s: %w", deviceID, activity.Name(), err)
		}
		return activity.Execute(scope(ctx, getter), input)
	}
}
