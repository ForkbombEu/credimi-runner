package workermanager

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/forkbombeu/credimi/pkg/workflowengine"
)

type fakeMobileActivity struct {
	name string
	read func(context.Context)
}

func (a fakeMobileActivity) Name() string { return a.name }

func (a fakeMobileActivity) Execute(ctx context.Context, _ workflowengine.ActivityInput) (workflowengine.ActivityResult, error) {
	a.read(ctx)
	return workflowengine.ActivityResult{Output: "ok"}, nil
}

func (a fakeMobileActivity) NewActivityError(failure workflowengine.ActivityError) error {
	return fmt.Errorf("%s: %s", failure.Code, failure.Message)
}

func (a fakeMobileActivity) NewNonRetryableActivityError(failure workflowengine.ActivityError) error {
	return fmt.Errorf("%s: %s", failure.Code, failure.Message)
}

func (a fakeMobileActivity) NewMissingOrInvalidPayloadError(err error) error { return err }

type scopedGetterKey struct{}

func TestMobileActivityExecutorScopesConcurrentDeviceConfiguration(t *testing.T) {
	config := RunnerRuntimeConfig{
		RunnerID: "acme/runner",
		Host:     map[string]string{"SHARED": "host"},
		Devices: []DeviceRuntimeConfig{
			{ID: "acme/runner/a", Enabled: true, Values: map[string]string{"BASE_NAME": "a"}},
			{ID: "acme/runner/b", Enabled: true, Values: map[string]string{"BASE_NAME": "b"}},
		},
	}
	provider := func() (RunnerRuntimeConfig, error) { return config, nil }
	seen := make(chan string, 2)
	activity := fakeMobileActivity{name: "Run a mobile test flow", read: func(ctx context.Context) {
		getter := ctx.Value(scopedGetterKey{}).(func(string, ...any) string)
		seen <- getter("BASE_NAME") + ":" + getter("SHARED")
	}}
	scope := func(ctx context.Context, getter func(string, ...any) string) context.Context {
		return context.WithValue(ctx, scopedGetterKey{}, getter)
	}
	executor := mobileActivityExecutorWithScope(activity, provider, scope)

	var wg sync.WaitGroup
	for _, deviceID := range []string{"acme/runner/a", "acme/runner/b"} {
		wg.Add(1)
		go func(deviceID string) {
			defer wg.Done()
			if _, err := executor(context.Background(), workflowengine.ActivityInput{Payload: map[string]any{"device_id": deviceID}}); err != nil {
				t.Errorf("execute %s: %v", deviceID, err)
			}
		}(deviceID)
	}
	wg.Wait()
	close(seen)
	values := map[string]bool{}
	for value := range seen {
		values[value] = true
	}
	if !values["a:host"] || !values["b:host"] {
		t.Fatalf("scoped environment leaked across devices: %#v", values)
	}
}

func TestMobileActivityExecutorUsesLatestInventoryAndRejectsRemovedDevice(t *testing.T) {
	configs := []RunnerRuntimeConfig{
		{RunnerID: "acme/runner", Devices: []DeviceRuntimeConfig{{ID: "acme/runner/a", Enabled: true, Values: map[string]string{"BASE_NAME": "old"}}}},
		{RunnerID: "acme/runner", Devices: []DeviceRuntimeConfig{{ID: "acme/runner/a", Enabled: true, Values: map[string]string{"BASE_NAME": "new"}}}},
	}
	current := 0
	provider := func() (RunnerRuntimeConfig, error) {
		config := configs[current]
		current++
		return config, nil
	}
	var got string
	activity := fakeMobileActivity{name: "Run a mobile test flow", read: func(ctx context.Context) {
		getter := ctx.Value(scopedGetterKey{}).(func(string, ...any) string)
		got = getter("BASE_NAME")
	}}
	scope := func(ctx context.Context, getter func(string, ...any) string) context.Context {
		return context.WithValue(ctx, scopedGetterKey{}, getter)
	}
	executor := mobileActivityExecutorWithScope(activity, provider, scope)
	if _, err := executor(context.Background(), workflowengine.ActivityInput{Payload: map[string]any{"device_id": "acme/runner/a"}}); err != nil {
		t.Fatal(err)
	}
	if got != "old" {
		t.Fatalf("initial device value = %q", got)
	}
	if _, err := executor(context.Background(), workflowengine.ActivityInput{Payload: map[string]any{"device_id": "acme/runner/a"}}); err != nil {
		t.Fatal(err)
	}
	if got != "new" {
		t.Fatalf("updated device value = %q", got)
	}
	configs = append(configs, RunnerRuntimeConfig{RunnerID: "acme/runner", Devices: nil})
	if _, err := executor(context.Background(), workflowengine.ActivityInput{Payload: map[string]any{"device_id": "acme/runner/a"}}); err == nil {
		t.Fatal("removed device was accepted")
	}
}
