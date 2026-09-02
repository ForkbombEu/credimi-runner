//go:build !darwin

package servicemanager

import (
	"context"
	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type upgradeRunner struct {
	calls  [][]string
	output string
}

func (r *upgradeRunner) Run(_ context.Context, _ string, args []string, _ []string) error {
	r.calls = append(r.calls, args)
	return nil
}
func (r *upgradeRunner) Output(context.Context, string, []string, []string) ([]byte, error) {
	return []byte(r.output), nil
}
func TestDockerUpgradeImageUsesAppliedComposeAndRunningIntent(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		wantUp       bool
	}{{"running", "container-id\n", true}, {"stopped", "\n", false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			r := &upgradeRunner{output: tc.output}
			m := NewDockerManager(dir, "")
			m.Runner = r
			m.LoadConfig = func() (runnerconfig.Config, error) { return runnerconfig.Bootstrap(), nil }
			if err := m.UpgradeImage(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if len(r.calls) < 1 || !strings.Contains(strings.Join(r.calls[0], " "), "pull runner") {
				t.Fatalf("calls=%v", r.calls)
			}
			if tc.wantUp && !strings.Contains(strings.Join(r.calls[len(r.calls)-1], " "), "force-recreate") {
				t.Fatalf("recreate calls=%v", r.calls)
			}
			if !tc.wantUp && len(r.calls) != 1 {
				t.Fatalf("stopped calls=%v", r.calls)
			}
		})
	}
}

func TestDockerStatusHasDefaultDashboardURLWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	r := &upgradeRunner{}
	m := NewDockerManager(dir, "")
	m.Runner = r
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.DashboardURL != "http://127.0.0.1:8051" {
		t.Fatalf("dashboard URL=%q", status.DashboardURL)
	}
}
