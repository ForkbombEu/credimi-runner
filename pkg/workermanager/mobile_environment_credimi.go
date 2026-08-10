//go:build credimi_extra

package workermanager

import (
	"context"

	"github.com/forkbombeu/credimi/pkg/workflowengine/activities"
)

func withMobileEnvironment(ctx context.Context, getter func(string, ...any) string) context.Context {
	return activities.WithMobileEnvironment(ctx, activities.MobileEnvironmentGetter(getter))
}
