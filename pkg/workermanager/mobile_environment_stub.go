//go:build !credimi_extra

package workermanager

import (
	"context"
)

func withMobileEnvironment(ctx context.Context, _ func(string, ...any) string) context.Context {
	return ctx
}
