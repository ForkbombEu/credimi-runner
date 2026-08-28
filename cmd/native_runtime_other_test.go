//go:build !darwin

package cmd

import (
	"strings"
	"testing"
)

func TestNativeRuntimeIsExplicitlyUnavailableOffMacOS(t *testing.T) {
	err := runNativeApplicationRuntime(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "only on macOS") {
		t.Fatalf("native runtime error = %v", err)
	}
}
