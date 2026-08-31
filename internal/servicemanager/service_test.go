//go:build !darwin

package servicemanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

type fakeCommandRunner struct{ calls [][]string }

func (f *fakeCommandRunner) Run(_ context.Context, name string, args []string, _ []string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	return nil
}
func (f *fakeCommandRunner) Output(context.Context, string, []string, []string) ([]byte, error) {
	return []byte("container-id\n"), nil
}

func TestWriteServiceComposeHasOnePersistentRunner(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Bootstrap()
	cfg.Android.RunnerImage = "runner:test"
	cfg.Android.PullPolicy = "never"
	if err := WriteServiceCompose(dir, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "service-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"runner:", "restart: unless-stopped", "internal-service", "CREDIMI_RUNNER_CONFIG_DIR"} {
		if !strings.Contains(text, want) {
			t.Fatalf("compose missing %q: %s", want, text)
		}
	}
	for _, bad := range []string{"tunnel", "control" + ".sock", "docker" + ".sock"} {
		if strings.Contains(strings.ToLower(text), bad) {
			t.Fatalf("compose contains forbidden %q", bad)
		}
	}
}
func TestDockerManagerCommands(t *testing.T) {
	dir := t.TempDir()
	f := &fakeCommandRunner{}
	m := NewDockerManager(dir, "")
	m.Runner = f
	m.LoadConfig = func() (config.Config, error) { return config.Bootstrap(), nil }
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 4 {
		t.Fatalf("calls=%v", f.calls)
	}
	if _, err := m.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type failingCommandRunner struct {
	err error
}

func (r failingCommandRunner) Run(context.Context, string, []string, []string) error { return r.err }
func (r failingCommandRunner) Output(context.Context, string, []string, []string) ([]byte, error) {
	return nil, r.err
}

func TestDockerManagerWrapsComposeErrors(t *testing.T) {
	want := errors.New("docker unavailable")
	m := NewDockerManager(t.TempDir(), "")
	m.Runner = failingCommandRunner{err: want}
	m.LoadConfig = func() (config.Config, error) { return config.Bootstrap(), nil }
	if err := m.Start(context.Background()); !errors.Is(err, want) {
		t.Fatalf("start error=%v", err)
	}
	if err := m.Stop(context.Background()); !errors.Is(err, want) {
		t.Fatalf("stop error=%v", err)
	}
	if _, err := m.Status(context.Background()); !errors.Is(err, want) {
		t.Fatalf("status error=%v", err)
	}
}

func TestDockerManagerUsesDefaultConfigLoader(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Bootstrap()
	cfg.Runner = config.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
	cfg.Credimi.URL = "https://credimi.example"
	cfg.Credimi.UserAPIKey = "key"
	cfg.Temporal.Address = "temporal:7233"
	if err := config.WriteFile(filepath.Join(dir, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	m := NewDockerManager(dir, "")
	m.Runner = &fakeCommandRunner{}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "service-compose.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestDockerManagerStartsBeforeConfigurationExists(t *testing.T) {
	dir := t.TempDir()
	m := NewDockerManager(dir, "")
	m.Runner = &fakeCommandRunner{}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "service-compose.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchAgentIsUnsupportedOutsideDarwin(t *testing.T) {
	m := LaunchAgentManager{}
	if err := m.Start(context.Background()); err != ErrUnsupported {
		t.Fatalf("start=%v", err)
	}
	if err := m.Stop(context.Background()); err != ErrUnsupported {
		t.Fatalf("stop=%v", err)
	}
	if err := m.Restart(context.Background()); err != ErrUnsupported {
		t.Fatalf("restart=%v", err)
	}
	if _, err := m.Status(context.Background()); err != ErrUnsupported {
		t.Fatalf("status=%v", err)
	}
	if err := m.Logs(context.Background(), LogOptions{}); err != ErrUnsupported {
		t.Fatalf("logs=%v", err)
	}
}

func TestServiceSpecFingerprintIsOrderIndependent(t *testing.T) {
	a := ServiceSpec{Image: "runner", PullPolicy: "never", Volumes: []string{"b", "a"}, Devices: []string{"2", "1"}}
	b := ServiceSpec{Image: "runner", PullPolicy: "never", Volumes: []string{"a", "b"}, Devices: []string{"1", "2"}}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("fingerprint depends on set ordering")
	}
}
func TestExecRunnerCommands(t *testing.T) {
	r := execRunner{}
	if err := r.Run(context.Background(), "true", nil, os.Environ()); err != nil {
		t.Fatal(err)
	}
	out, err := r.Output(context.Background(), "printf", []string{"ok"}, os.Environ())
	if err != nil || string(out) != "ok" {
		t.Fatalf("%q %v", out, err)
	}
}
func TestDockerManagerLogsAndMissingConfig(t *testing.T) {
	m := NewDockerManager(t.TempDir(), "")
	m.Runner = &fakeCommandRunner{}
	if err := m.Logs(context.Background(), LogOptions{}); err != nil {
		t.Fatal(err)
	}
	m.LoadConfig = func() (config.Config, error) { return config.Config{}, os.ErrPermission }
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected config error")
	}
}

type statusRunner struct{ outputs [][]byte }

func (r *statusRunner) Run(context.Context, string, []string, []string) error { return nil }
func (r *statusRunner) Output(context.Context, string, []string, []string) ([]byte, error) {
	if len(r.outputs) == 0 {
		return nil, nil
	}
	out := r.outputs[0]
	r.outputs = r.outputs[1:]
	return out, nil
}

func TestDockerStatusReadsRuntimeAndFingerprint(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Bootstrap()
	if err := WriteServiceCompose(dir, cfg); err != nil {
		t.Fatal(err)
	}
	state := `{"desired":"running","actual":"failed","last_error":"offline"}`
	if err := os.WriteFile(filepath.Join(dir, "runtime-state.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &statusRunner{outputs: [][]byte{[]byte("id\n"), []byte("wrong\n")}}
	m := NewDockerManager(dir, "")
	m.Runner = runner
	m.LoadConfig = func() (config.Config, error) { return cfg, nil }
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.RuntimeDesired != "running" || status.RuntimeActual != "failed" || status.RuntimeError != "offline" || !status.ServiceRestartRequired {
		t.Fatalf("status=%+v", status)
	}
}

func TestDockerStatusReportsComposeError(t *testing.T) {
	want := errors.New("compose ps failed")
	m := NewDockerManager(t.TempDir(), "")
	m.Runner = failingCommandRunner{err: want}
	if _, err := m.Status(context.Background()); !errors.Is(err, want) {
		t.Fatalf("status error=%v", err)
	}
}
