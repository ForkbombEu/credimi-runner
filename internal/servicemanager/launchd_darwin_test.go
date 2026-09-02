//go:build darwin

package servicemanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestLaunchAgentStartCommandSequences(t *testing.T) {
	for _, tc := range []struct {
		name       string
		printError bool
		want       []string
	}{
		{"unloaded", true, []string{"print", "bootstrap", "kickstart"}},
		{"loaded", false, []string{"print"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			var calls []string
			m := &LaunchAgentManager{ConfigDir: filepath.Join(home, "config"), BinaryPath: "/bin/runner", HomeDir: home}
			m.Run = func(_ context.Context, name string, args ...string) error {
				calls = append(calls, name+" "+strings.Join(args, " "))
				if len(calls) == 1 && tc.printError {
					return errors.New("not loaded")
				}
				return nil
			}
			if err := m.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			for i, want := range tc.want {
				if !strings.HasPrefix(calls[i], "launchctl "+want) {
					t.Fatalf("calls=%v", calls)
				}
			}
			if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLaunchAgentRestartBootsOutBeforeStart(t *testing.T) {
	home := t.TempDir()
	var calls []string
	m := &LaunchAgentManager{ConfigDir: filepath.Join(home, "config"), BinaryPath: "/bin/runner", HomeDir: home}
	m.Run = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if len(calls) == 1 {
			return nil
		}
		return nil
	}
	if err := m.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) < 2 || !strings.HasPrefix(calls[0], "launchctl bootout") || !strings.HasPrefix(calls[1], "launchctl print") {
		t.Fatalf("calls=%v", calls)
	}
}

func TestLaunchAgentStatusStoppedHasNoPendingRestart(t *testing.T) {
	m := &LaunchAgentManager{ConfigDir: t.TempDir(), Run: func(context.Context, string, ...string) error {
		return errors.New("not loaded")
	}}
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Running || status.ServiceRestartRequired {
		t.Fatalf("status=%+v", status)
	}
}
