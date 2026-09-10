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
