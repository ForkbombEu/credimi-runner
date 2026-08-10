package workermanager

import (
	"context"
	"fmt"
	"strings"

	"github.com/forkbombeu/credimi/pkg/workflowengine"
)

type mobileActivityPayload struct {
	DeviceID string `json:"device_id"`
}

// isMobileActivity deliberately lists the existing Credimi mobile activities.
// HTTP and future non-device activities remain direct registrations rather
// than passing through a device-selection path.
func isMobileActivity(name string) bool {
	switch name {
	case "Run a mobile test flow",
		"Setup mobile device",
		"Install APK on device",
		"Run APK post-install checks",
		"Unlock emulator",
		"Setup iOS simulator",
		"Install iOS app on device",
		"Run iOS post-install checks",
		"Start recording device screen",
		"Start recording iOS device screen",
		"Stop recording device screen",
		"Stop recording iOS device screen",
		"List installed mobile apps",
		"Disable Android Play Store",
		"Cleanup mobile device":
		return true
	default:
		return false
	}
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
