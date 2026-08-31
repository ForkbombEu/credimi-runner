//go:build darwin

package servicemanager

import (
	"strings"
	"testing"
)

func TestLaunchAgentPlistUsesInternalService(t *testing.T) {
	m := &LaunchAgentManager{ConfigDir: "/tmp/runner", BinaryPath: "/usr/local/bin/credimi-runner"}
	plist := m.plist()
	for _, want := range []string{"eu.forkbomb.credimi-runner", "/usr/local/bin/credimi-runner", "internal-service", "RunAtLoad", "KeepAlive", "CREDIMI_RUNNER_CONFIG_DIR"} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q", want)
		}
	}
}
