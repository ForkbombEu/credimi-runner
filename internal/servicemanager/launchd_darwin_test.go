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
			dir := filepath.Join(home, "config")
			if err := saveAutostart(dir, true); err != nil {
				t.Fatal(err)
			}
			var calls []string
			m := &LaunchAgentManager{ConfigDir: dir, BinaryPath: "/bin/runner", HomeDir: home}
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

func TestLaunchAgentDisabledStartUsesTransientPlist(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "config")
	var calls []string
	m := &LaunchAgentManager{ConfigDir: dir, BinaryPath: "/bin/runner", HomeDir: home}
	m.Run = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if len(calls) == 1 {
			return errors.New("not loaded")
		}
		return nil
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")); !os.IsNotExist(err) {
		t.Fatalf("persistent plist error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "service-launchd.plist")); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchAgentEnableDisableDoNotChangeRunningService(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "config")
	if err := saveAutostart(dir, false); err != nil {
		t.Fatal(err)
	}
	var calls []string
	m := &LaunchAgentManager{ConfigDir: dir, BinaryPath: "/bin/runner", HomeDir: home}
	m.Run = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("enable launchctl calls=%v", calls)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")); err != nil {
		t.Fatal(err)
	}
	if err := m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "launchctl print") {
		t.Fatalf("disable launchctl calls=%v", calls)
	}
	if got, err := loadAutostart(dir); err != nil || got {
		t.Fatalf("autostart=%v err=%v", got, err)
	}
}

func TestLaunchAgentStopPreservesAutostart(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "config")
	if err := saveAutostart(dir, true); err != nil {
		t.Fatal(err)
	}
	m := &LaunchAgentManager{ConfigDir: dir, BinaryPath: "/bin/runner", HomeDir: home, Run: func(context.Context, string, ...string) error { return nil }}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := loadAutostart(dir); err != nil || !got {
		t.Fatalf("autostart=%v err=%v", got, err)
	}
}

func TestLaunchAgentStatusReportsAutostart(t *testing.T) {
	dir := t.TempDir()
	if err := saveAutostart(dir, true); err != nil {
		t.Fatal(err)
	}
	m := &LaunchAgentManager{ConfigDir: dir, Run: func(context.Context, string, ...string) error { return errors.New("not loaded") }}
	status, err := m.Status(context.Background())
	if err != nil || !status.Autostart || status.Running {
		t.Fatalf("status=%+v err=%v", status, err)
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
