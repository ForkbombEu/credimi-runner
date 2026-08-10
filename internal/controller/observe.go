package controller

import (
	"context"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/controller/driver"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

// Observer combines read-only driver results into one timestamped snapshot.
type Observer struct {
	Drivers []driver.Driver
	Now     func() time.Time
}

func (o Observer) Observe(ctx context.Context, configDir string, values dashboardruntime.Values) ObservedRuntime {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	observed := ObservedRuntime{State: StateStopped, ObservedAt: now().UTC()}
	plan := dashboardruntime.BuildRuntimePlan(configDir, values)
	request := driver.Request{
		ComposeProject: plan.ComposeProject,
		EnvPath:        plan.EnvPath,
		ComposePath:    plan.ComposePath,
	}
	for _, expected := range plan.ExpectedServices {
		request.ComposeServices = append(request.ComposeServices, driver.ExpectedService{ID: expected.ID, Name: expected.Name, Role: expected.Role, Kind: expected.Kind, Critical: expected.Critical})
	}
	for _, d := range o.Drivers {
		if d == nil {
			continue
		}
		result := d.Observe(ctx, request)
		if result.Error != nil && observed.Error == "" {
			observed.Error = result.Error.Error()
		}
		for _, service := range result.Services {
			state := StateStopped
			if service.Running {
				state = StateRunning
			}
			if !service.Owned && service.Detail == "foreign listener" {
				state = StateForeign
			}
			observed.Services = append(observed.Services, ObservedService{ID: service.ID, Name: service.Name, Role: service.Role, Image: service.Image, Detail: service.Detail, State: state, Owned: service.Owned, Critical: service.Critical})
		}
	}
	critical, running := 0, 0
	for _, service := range observed.Services {
		if service.State == StateForeign {
			observed.State = StateForeign
			return observed
		}
		if service.Critical {
			critical++
			if service.State == StateRunning {
				running++
			}
		}
	}
	switch {
	case critical > 0 && running == critical:
		observed.State = StateRunning
	case running > 0:
		observed.State = StateDegraded
	case observed.Error != "":
		observed.State = StateUnknown
	default:
		observed.State = StateStopped
	}
	return observed
}
