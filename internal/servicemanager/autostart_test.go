//go:build !darwin

package servicemanager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func TestBuildServiceSpecUsesAutostartRestartPolicy(t *testing.T) {
	dir := t.TempDir()
	host, err := ResolveHostContext(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Bootstrap()
	for _, tc := range []struct {
		name      string
		autostart bool
		want      string
	}{
		{"disabled", false, "on-failure"},
		{"enabled", true, "unless-stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := BuildServiceSpecWithAutostart(cfg, host, tc.autostart)
			if err != nil || spec.RestartPolicy != tc.want {
				t.Fatalf("spec=%+v err=%v", spec, err)
			}
		})
	}
}

func TestServiceLifecyclePreservesAutostart(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			dir := t.TempDir()
			if err := saveAutostart(dir, enabled); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Bootstrap()
			cfg.Android.RunnerImage = "credimi-runner:local"
			cfg.Android.PullPolicy = "never"
			r := &scriptedRunner{t: t, steps: []commandStep{
				{kind: "run", contains: []string{"down", "--timeout", "30"}},
				{kind: "run", contains: []string{"up", "-d", "runner"}},
				{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
				{kind: "output", contains: []string{"inspect", ".Image"}, output: []byte("sha256:local\n")},
				{kind: "output", contains: []string{"image", "inspect", ".RepoDigests"}, output: []byte(`[]`)},
			}}
			m := NewDockerManager(dir, "")
			m.Runner = r
			m.LoadConfig = func() (config.Config, error) { return cfg, nil }
			if err := m.Restart(context.Background()); err != nil {
				t.Fatal(err)
			}
			r.done()
			got, err := loadAutostart(dir)
			if err != nil || got != enabled {
				t.Fatalf("autostart=%v err=%v want %v", got, err, enabled)
			}
		})
	}
}

func TestStartDoesNotEnableAndStopDoesNotDisable(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Bootstrap()
	cfg.Android.RunnerImage = "credimi-runner:local"
	cfg.Android.PullPolicy = "never"
	r := &scriptedRunner{t: t, steps: []commandStep{
		{kind: "run", contains: []string{"up", "-d", "runner"}},
		{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
		{kind: "output", contains: []string{"inspect", ".Image"}, output: []byte("sha256:local\n")},
		{kind: "output", contains: []string{"image", "inspect", ".RepoDigests"}, output: []byte(`[]`)},
		{kind: "run", contains: []string{"down", "--timeout", "30"}},
	}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	m.LoadConfig = func() (config.Config, error) { return cfg, nil }
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := loadAutostart(dir); err != nil || got {
		t.Fatalf("after start autostart=%v err=%v", got, err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := loadAutostart(dir); err != nil || got {
		t.Fatalf("after stop autostart=%v err=%v", got, err)
	}
	r.done()
}

func TestEnableDisableStoppedOnlyPersist(t *testing.T) {
	dir := t.TempDir()
	r := &noDockerCallsRunner{}
	m := NewDockerManager(dir, "")
	m.Runner = r
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := loadAutostart(dir); err != nil || !got {
		t.Fatalf("after enable=%v err=%v", got, err)
	}
	if err := m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := loadAutostart(dir); err != nil || got {
		t.Fatalf("after disable=%v err=%v", got, err)
	}
	if r.calls != 0 {
		t.Fatalf("Docker calls=%d", r.calls)
	}
}

func TestEnableDisableRunningUpdatesPolicyWithoutRecreation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		enable bool
		policy string
	}{
		{"enable", true, "unless-stopped"},
		{"disable", false, "on-failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			r := &scriptedRunner{t: t, steps: []commandStep{
				{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
				{kind: "run", contains: []string{"update", "--restart", tc.policy, "container123"}},
			}}
			m := NewDockerManager(dir, "")
			m.Runner = r
			if err := func() error {
				if tc.enable {
					return m.Enable(context.Background())
				}
				return m.Disable(context.Background())
			}(); err != nil {
				t.Fatal(err)
			}
			r.done()
		})
	}
}

func TestStatusReportsAutostart(t *testing.T) {
	dir := t.TempDir()
	if err := saveAutostart(dir, true); err != nil {
		t.Fatal(err)
	}
	m := NewDockerManager(dir, "")
	m.Runner = &noDockerCallsRunner{}
	status, err := m.Status(context.Background())
	if err != nil || !status.Autostart || status.Running {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestRestartPolicyDoesNotMakeFingerprintStale(t *testing.T) {
	base := ServiceSpec{Image: "runner", PullPolicy: "never", NetworkMode: "bridge", RestartPolicy: "on-failure"}
	other := base
	other.RestartPolicy = "unless-stopped"
	if base.Fingerprint() != other.Fingerprint() {
		t.Fatal("restart policy unexpectedly changes topology fingerprint")
	}
}
