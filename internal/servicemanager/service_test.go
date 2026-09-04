//go:build !darwin

package servicemanager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
	"gopkg.in/yaml.v3"
)

type fakeCommandRunner struct{ calls [][]string }

func writeServiceCompose(t *testing.T, dir string, cfg config.Config) {
	t.Helper()
	host, err := ResolveHostContext(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteServiceComposeWithHost(dir, cfg, host); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeCommandRunner) Run(_ context.Context, name string, args []string, _ []string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	return nil
}
func (f *fakeCommandRunner) Output(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
	if containsArgs(args, "-aq") && !containsArgs(args, "runner") {
		return nil, nil
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "image inspect") {
		return []byte(`["credimi-runner@sha256:test","ghcr.io/forkbombeu/credimi-runner@sha256:test"]`), nil
	}
	if strings.Contains(joined, "inspect --format") {
		return []byte("sha256:local-config\n"), nil
	}
	return []byte("container-id\n"), nil
}

func TestWriteServiceComposeHasOnePersistentRunner(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Bootstrap()
	cfg.Android.RunnerImage = "runner:test"
	cfg.Android.PullPolicy = "never"
	cfg.Android.Network = "credimi-runner"
	writeServiceCompose(t, dir, cfg)
	raw, err := os.ReadFile(filepath.Join(dir, "service-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"runner:", "restart: on-failure", "internal-service", "CREDIMI_RUNNER_CONFIG_DIR", "pull_policy: never"} {
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

func TestRecordAppliedImagePersistsMatchingRepoDigest(t *testing.T) {
	dir := t.TempDir()
	m := NewDockerManager(dir, "")
	stateRunner := &sequenceRunner{outputs: [][]byte{[]byte("container123\n"), []byte("sha256:local-config\n"), []byte(`["example.com/other/image@sha256:wrong","ghcr.io/forkbombeu/credimi-runner@sha256:right"]`)}}
	m.Runner = stateRunner
	host, err := ResolveHostContext(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.host = host
	if err := m.recordAppliedImage(context.Background(), "ghcr.io/forkbombeu/credimi-runner:latest"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "service-image-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	state := readImageState(t, dir)
	if state.Digest != "sha256:right" || state.ImageID != "sha256:local-config" || !state.RegistryTrackable {
		t.Fatalf("state=%s", raw)
	}
}

func TestDockerStartAllowsLocalImageWithoutRepoDigest(t *testing.T) {
	dir := t.TempDir()
	cfg := dockerTestConfig()
	cfg.Android.RunnerImage = "credimi-runner:local"
	cfg.Android.PullPolicy = "never"
	r := &scriptedRunner{t: t, steps: []commandStep{
		{kind: "run", contains: []string{"up", "-d", "runner"}},
		{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
		{kind: "output", contains: []string{"inspect", ".Image"}, output: []byte("sha256:local-image\n")},
		{kind: "output", contains: []string{"image", "inspect", ".RepoDigests"}, output: []byte("[]")},
	}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	m.LoadConfig = func() (config.Config, error) { return cfg, nil }
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.done()
	state := readImageState(t, dir)
	if state.Image != cfg.Android.RunnerImage || state.ImageID != "sha256:local-image" || state.Digest != "" || state.RegistryTrackable {
		t.Fatalf("state=%+v", state)
	}
	if info, err := os.Stat(filepath.Join(dir, "service-image-state.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions: %v, %v", info, err)
	}
}

type sequenceRunner struct{ outputs [][]byte }

func (r *sequenceRunner) Run(context.Context, string, []string, []string) error { return nil }
func (r *sequenceRunner) Output(context.Context, string, []string, []string) ([]byte, error) {
	out := r.outputs[0]
	r.outputs = r.outputs[1:]
	return out, nil
}

type commandStep struct {
	kind     string
	contains []string
	output   []byte
	err      error
}

type scriptedRunner struct {
	t     *testing.T
	steps []commandStep
}

func (r *scriptedRunner) take(kind, name string, args []string) commandStep {
	r.t.Helper()
	if len(r.steps) == 0 {
		r.t.Fatalf("unexpected %s %s %v", kind, name, args)
	}
	step := r.steps[0]
	r.steps = r.steps[1:]
	if step.kind != kind || name != "docker" {
		r.t.Fatalf("got %s %s %v, want %s docker", kind, name, args, step.kind)
	}
	joined := strings.Join(args, " ")
	for _, want := range step.contains {
		if !strings.Contains(joined, want) {
			r.t.Fatalf("args %v missing %q", args, want)
		}
	}
	return step
}

func (r *scriptedRunner) Run(_ context.Context, name string, args []string, _ []string) error {
	return r.take("run", name, args).err
}

func (r *scriptedRunner) Output(_ context.Context, name string, args []string, _ []string) ([]byte, error) {
	if containsArgs(args, "-aq") && !containsArgs(args, "runner") {
		return nil, nil
	}
	step := r.take("output", name, args)
	return step.output, step.err
}

func (r *scriptedRunner) done() {
	r.t.Helper()
	if len(r.steps) != 0 {
		r.t.Fatalf("%d scripted command steps unused", len(r.steps))
	}
}

func writeAppliedState(t *testing.T, dir, image, digest string) []byte {
	t.Helper()
	raw := []byte(`{"image":"` + image + `","digest":"` + digest + `"}`)
	if err := os.WriteFile(filepath.Join(dir, "service-image-state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func readAppliedState(t *testing.T, dir string) (image, digest string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "service-image-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Image  string `json:"image"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state.Image, state.Digest
}

type appliedImageState struct {
	Image             string `json:"image"`
	ImageID           string `json:"image_id"`
	Digest            string `json:"digest"`
	RegistryTrackable bool   `json:"registry_trackable"`
}

func readImageState(t *testing.T, dir string) appliedImageState {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "service-image-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state appliedImageState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func dockerTestConfig() config.Config {
	cfg := config.Bootstrap()
	cfg.Android.RunnerImage = "ghcr.io/forkbombeu/credimi-runner:latest"
	return cfg
}

func TestRecordAppliedImageRejectsMalformedRepoDigestsAndPreservesState(t *testing.T) {
	dir := t.TempDir()
	old := writeAppliedState(t, dir, "ghcr.io/forkbombeu/credimi-runner:latest", "sha256:old")
	r := &scriptedRunner{t: t, steps: []commandStep{
		{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
		{kind: "output", contains: []string{"inspect", ".Image"}, output: []byte("sha256:local\n")},
		{kind: "output", contains: []string{"image", "inspect", ".RepoDigests"}, output: []byte("not-json")},
	}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	err := m.recordAppliedImage(context.Background(), dockerTestConfig().Android.RunnerImage)
	if err == nil || !strings.Contains(err.Error(), "RepoDigests") {
		t.Fatalf("error=%v", err)
	}
	r.done()
	got, _ := os.ReadFile(filepath.Join(dir, "service-image-state.json"))
	if string(got) != string(old) {
		t.Fatalf("state changed from %q to %q", old, got)
	}
}

func TestRecordAppliedImageRejectsEmptyAndUnmatchedRepoDigests(t *testing.T) {
	for _, metadata := range []string{"[]", "null", `["ghcr.io/forkbombeu/credimi-runner@latest"]`, `["example.com/other/image@sha256:wrong"]`} {
		t.Run(metadata, func(t *testing.T) {
			dir := t.TempDir()
			old := writeAppliedState(t, dir, "ghcr.io/forkbombeu/credimi-runner:latest", "sha256:old")
			r := &scriptedRunner{t: t, steps: []commandStep{
				{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
				{kind: "output", contains: []string{"inspect", ".Image"}, output: []byte("sha256:local\n")},
				{kind: "output", contains: []string{"image", "inspect", ".RepoDigests"}, output: []byte(metadata)},
			}}
			m := NewDockerManager(dir, "")
			m.Runner = r
			if err := m.recordAppliedImage(context.Background(), dockerTestConfig().Android.RunnerImage); err == nil {
				t.Fatal("expected RepoDigest error")
			}
			r.done()
			got, _ := os.ReadFile(filepath.Join(dir, "service-image-state.json"))
			if string(got) != string(old) {
				t.Fatalf("state changed from %q to %q", old, got)
			}
		})
	}
}

func TestDockerStartRecordsRepoDigestAndPropagatesFailure(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		r := &scriptedRunner{t: t, steps: []commandStep{
			{kind: "run", contains: []string{"up", "-d", "runner"}},
			{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
			{kind: "output", contains: []string{"inspect", ".Image"}, output: []byte("sha256:local\n")},
			{kind: "output", contains: []string{"image", "inspect", ".RepoDigests"}, output: []byte(`["ghcr.io/forkbombeu/credimi-runner@sha256:applied"]`)},
		}}
		m := NewDockerManager(dir, "")
		m.Runner = r
		m.LoadConfig = func() (config.Config, error) { return dockerTestConfig(), nil }
		if err := m.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		r.done()
		image, digest := readAppliedState(t, dir)
		if image != dockerTestConfig().Android.RunnerImage || digest != "sha256:applied" {
			t.Fatalf("state=%q %q", image, digest)
		}
	})
	t.Run("recording failure", func(t *testing.T) {
		dir := t.TempDir()
		r := &scriptedRunner{t: t, steps: []commandStep{
			{kind: "run", contains: []string{"up", "-d", "runner"}},
			{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
			{kind: "output", contains: []string{"inspect", ".Image"}, output: []byte("sha256:local\n")},
			{kind: "output", contains: []string{"image", "inspect", ".RepoDigests"}, output: []byte("not-json")},
		}}
		m := NewDockerManager(dir, "")
		m.Runner = r
		m.LoadConfig = func() (config.Config, error) { return dockerTestConfig(), nil }
		if err := m.Start(context.Background()); err == nil {
			t.Fatal("expected image-state error")
		}
		r.done()
	})
}

func TestDockerUpgradeImagePreservesAndUpdatesAppliedState(t *testing.T) {
	t.Run("pull failure", func(t *testing.T) {
		dir := t.TempDir()
		_ = writeAppliedState(t, dir, dockerTestConfig().Android.RunnerImage, "sha256:old")
		if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		pullErr := errors.New("pull failed")
		r := &scriptedRunner{t: t, steps: []commandStep{
			{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
			{kind: "output", contains: []string{"inspect", "service-fingerprint"}, output: []byte("fingerprint\n")},
			{kind: "run", contains: []string{"pull", "runner"}, err: pullErr},
		}}
		m := NewDockerManager(dir, "")
		m.Runner = r
		m.LoadConfig = func() (config.Config, error) { return dockerTestConfig(), nil }
		if err := m.UpgradeImage(context.Background(), nil); !errors.Is(err, pullErr) {
			t.Fatalf("err=%v", err)
		}
		r.done()
		_, digest := readAppliedState(t, dir)
		if digest != "sha256:old" {
			t.Fatalf("digest=%s", digest)
		}
	})
	t.Run("stopped pull only", func(t *testing.T) {
		dir := t.TempDir()
		_ = writeAppliedState(t, dir, dockerTestConfig().Android.RunnerImage, "sha256:old")
		if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := &scriptedRunner{t: t, steps: []commandStep{{kind: "output", contains: []string{"ps", "runner"}, output: []byte("\n")}, {kind: "output", contains: []string{"inspect", "service-fingerprint"}, output: []byte("\n")}, {kind: "run", contains: []string{"pull", "runner"}}}}
		m := NewDockerManager(dir, "")
		m.Runner = r
		m.LoadConfig = func() (config.Config, error) { return dockerTestConfig(), nil }
		if err := m.UpgradeImage(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		r.done()
		_, digest := readAppliedState(t, dir)
		if digest != "sha256:old" {
			t.Fatalf("digest=%s", digest)
		}
	})
	t.Run("recreate failure", func(t *testing.T) {
		dir := t.TempDir()
		_ = writeAppliedState(t, dir, dockerTestConfig().Android.RunnerImage, "sha256:old")
		if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		recreateErr := errors.New("recreate failed")
		r := &scriptedRunner{t: t, steps: []commandStep{{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")}, {kind: "output", contains: []string{"inspect", "service-fingerprint"}, output: []byte("fingerprint\n")}, {kind: "run", contains: []string{"pull", "runner"}}, {kind: "run", contains: []string{"up", "--force-recreate", "runner"}, err: recreateErr}}}
		m := NewDockerManager(dir, "")
		m.Runner = r
		m.LoadConfig = func() (config.Config, error) { return dockerTestConfig(), nil }
		if err := m.UpgradeImage(context.Background(), nil); !errors.Is(err, recreateErr) {
			t.Fatalf("err=%v", err)
		}
		r.done()
		_, digest := readAppliedState(t, dir)
		if digest != "sha256:old" {
			t.Fatalf("digest=%s", digest)
		}
	})
	t.Run("recreate success", func(t *testing.T) {
		dir := t.TempDir()
		_ = writeAppliedState(t, dir, dockerTestConfig().Android.RunnerImage, "sha256:old")
		if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := &scriptedRunner{t: t, steps: []commandStep{{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")}, {kind: "output", contains: []string{"inspect", "service-fingerprint"}, output: []byte("fingerprint\n")}, {kind: "run", contains: []string{"pull", "runner"}}, {kind: "run", contains: []string{"up", "--force-recreate", "runner"}}, {kind: "output", contains: []string{"ps", "runner"}, output: []byte("new-container\n")}, {kind: "output", contains: []string{"inspect", ".Image"}, output: []byte("sha256:new-local\n")}, {kind: "output", contains: []string{"image", "inspect", ".RepoDigests"}, output: []byte(`["ghcr.io/forkbombeu/credimi-runner@sha256:new"]`)}}}
		m := NewDockerManager(dir, "")
		m.Runner = r
		m.LoadConfig = func() (config.Config, error) { return dockerTestConfig(), nil }
		if err := m.UpgradeImage(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		r.done()
		_, digest := readAppliedState(t, dir)
		if digest != "sha256:new" {
			t.Fatalf("digest=%s", digest)
		}
	})
	t.Run("recreate success but recording fails", func(t *testing.T) {
		dir := t.TempDir()
		old := writeAppliedState(t, dir, dockerTestConfig().Android.RunnerImage, "sha256:old")
		if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := &scriptedRunner{t: t, steps: []commandStep{
			{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
			{kind: "output", contains: []string{"inspect", "service-fingerprint"}, output: []byte("fingerprint\n")},
			{kind: "run", contains: []string{"pull", "runner"}},
			{kind: "run", contains: []string{"up", "--force-recreate", "runner"}},
			{kind: "output", contains: []string{"ps", "runner"}, output: []byte("new-container\n")},
			{kind: "output", contains: []string{"inspect", ".Image"}, output: []byte("sha256:new-local\n")},
			{kind: "output", contains: []string{"image", "inspect", ".RepoDigests"}, output: []byte("not-json")},
		}}
		m := NewDockerManager(dir, "")
		m.Runner = r
		m.LoadConfig = func() (config.Config, error) { return dockerTestConfig(), nil }
		if err := m.UpgradeImage(context.Background(), nil); err == nil {
			t.Fatal("expected recording error")
		}
		r.done()
		got, _ := os.ReadFile(filepath.Join(dir, "service-image-state.json"))
		if string(got) != string(old) {
			t.Fatalf("state changed from %q to %q", old, got)
		}
	})
}

func TestDockerUpgradeImageNeverSkipsPullForLocalImage(t *testing.T) {
	dir := t.TempDir()
	cfg := dockerTestConfig()
	cfg.Android.RunnerImage = "credimi-runner:local"
	cfg.Android.PullPolicy = "never"
	if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &scriptedRunner{t: t, steps: []commandStep{
		{kind: "output", contains: []string{"ps", "runner"}, output: []byte("container123\n")},
		{kind: "output", contains: []string{"inspect", "service-fingerprint"}, output: []byte("fingerprint\n")},
		{kind: "run", contains: []string{"up", "--force-recreate", "runner"}},
		{kind: "output", contains: []string{"ps", "runner"}, output: []byte("new-container\n")},
		{kind: "output", contains: []string{"inspect", ".Image"}, output: []byte("sha256:local-image\n")},
		{kind: "output", contains: []string{"image", "inspect", ".RepoDigests"}, output: []byte("[]")},
	}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	m.LoadConfig = func() (config.Config, error) { return cfg, nil }
	if err := m.UpgradeImage(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	r.done()
}

func TestDockerStopRetainsAppliedState(t *testing.T) {
	dir := t.TempDir()
	old := writeAppliedState(t, dir, dockerTestConfig().Android.RunnerImage, "sha256:old")
	if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &scriptedRunner{t: t, steps: []commandStep{{kind: "run", contains: []string{"stop", "--timeout", "30", "runner"}}}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.done()
	got, _ := os.ReadFile(filepath.Join(dir, "service-image-state.json"))
	if string(got) != string(old) {
		t.Fatalf("state changed")
	}
}

func TestWriteServiceComposeUsesComposePullPolicyNames(t *testing.T) {
	cfg := config.Bootstrap()
	dir := t.TempDir()
	writeServiceCompose(t, dir, cfg)
	raw, err := os.ReadFile(filepath.Join(dir, "service-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pull_policy: if-not-present") || !strings.Contains(string(raw), "pull_policy: missing") {
		t.Fatalf("invalid Compose pull policy: %s", raw)
	}
}

func TestDockerManagerAppliesBootstrapOptionsBeforeConfigExists(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeCommandRunner{}
	m := NewDockerManagerWithBootstrap(dir, "", BootstrapOptions{Image: "credimi-runner:local", PullPolicy: "never"})
	m.Runner = runner
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "service-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"image: credimi-runner:local",
		"pull_policy: never",
		"CREDIMI_BOOTSTRAP_IMAGE:",
		"CREDIMI_BOOTSTRAP_PULL_POLICY:",
		"ANDROID_RUNNER_IMAGE:",
		"ANDROID_PULL_POLICY:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("bootstrap compose missing %q: %s", want, text)
		}
	}
}

func TestDockerManagerRejectsInvalidBootstrapPullPolicy(t *testing.T) {
	m := NewDockerManagerWithBootstrap(t.TempDir(), "", BootstrapOptions{PullPolicy: "later"})
	m.Runner = &fakeCommandRunner{}
	if err := m.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid bootstrap pull policy") {
		t.Fatalf("invalid bootstrap policy error = %v", err)
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
	wantProject := ProjectName(dir, m.host.UID)
	for _, call := range f.calls {
		if !containsArgs(call, "--project-name") || !containsArgs(call, wantProject) {
			t.Fatalf("compose call does not use canonical project %q: %v", wantProject, call)
		}
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
	a := ServiceSpec{Image: "runner", PullPolicy: "never", Volumes: []NamedVolume{{Name: "b", Target: "/b"}, {Name: "a", Target: "/a"}}, Devices: []DeviceMapping{{Source: "2", Target: "2"}, {Source: "1", Target: "1"}}}
	b := ServiceSpec{Image: "runner", PullPolicy: "never", Volumes: []NamedVolume{{Name: "a", Target: "/a"}, {Name: "b", Target: "/b"}}, Devices: []DeviceMapping{{Source: "1", Target: "1"}, {Source: "2", Target: "2"}}}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("fingerprint depends on set ordering")
	}
}

func TestBuildServiceSpecRestoresEmulatorHostCapabilities(t *testing.T) {
	host := testHost("/home/alice")
	cfg := configForDevices(config.DeviceConfig{
		ID: "org/runner/emulator", Name: "Emulator", Type: config.DeviceAndroidEmulator, Enabled: true,
		AndroidEmulator: &config.AndroidEmulatorConfig{BaseName: "credimi", GoldenSource: "/avd-golden/credimi-golden"},
	})
	spec, err := BuildServiceSpec(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	assertBind(t, spec, "/home/alice/.android", ContainerAndroidDir, false)
	assertBind(t, spec, "/home/alice/avd-golden", ContainerGoldenRoot, false)
	assertDevice(t, spec, "/dev/kvm", "/dev/kvm")
	for key, want := range map[string]string{
		ContainerAndroidEnv: ContainerAndroidDir,
		ContainerAVDHomeEnv: ContainerAVDHome,
		ContainerGoldenEnv:  ContainerGoldenRoot,
		ConfigOwnerUIDEnv:   "1000",
		ConfigOwnerGIDEnv:   "1000",
	} {
		if spec.Environment[key] != want {
			t.Fatalf("environment %s=%q, want %q", key, spec.Environment[key], want)
		}
	}
	assertNoDuplicateBindTargets(t, spec)
}

func TestBuildServiceSpecPreservesEmulatorAssetPathContract(t *testing.T) {
	home := t.TempDir()
	host := testHost(home)
	host.AndroidDir = filepath.Join(home, ".android")
	host.AVDHome = filepath.Join(host.AndroidDir, "avd")
	host.GoldenRoot = filepath.Join(home, "avd-golden")
	for _, path := range []string{
		filepath.Join(host.AVDHome, "credimi.avd"),
		filepath.Join(host.AVDHome, "credimi.ini"),
		filepath.Join(host.GoldenRoot, "credimi-golden"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := configForDevices(config.DeviceConfig{
		ID: "org/runner/emulator", Name: "Emulator", Type: config.DeviceAndroidEmulator, Enabled: true,
		AndroidEmulator: &config.AndroidEmulatorConfig{BaseName: "credimi", GoldenSource: "/avd-golden/credimi-golden"},
	})
	spec, err := BuildServiceSpec(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	assertBind(t, spec, filepath.Join(home, ".android"), ContainerAndroidDir, false)
	assertBind(t, spec, filepath.Join(home, "avd-golden"), ContainerGoldenRoot, false)
	if spec.Environment[AndroidAVDHomeEnv] != ContainerAVDHome {
		t.Fatalf("ANDROID_AVD_HOME=%q, want %q", spec.Environment[AndroidAVDHomeEnv], ContainerAVDHome)
	}
}

func TestBuildServiceSpecRestoresPhysicalUSBHostADBContract(t *testing.T) {
	// Before the persistent-service refactor, USB runners reused the host ADB
	// daemon through host networking and ADB_SERVER_SOCKET. Keep that behavior
	// so adb devices inside the runner sees the host's authorized devices.
	host := testHost("/home/alice")
	cfg := configForDevices(config.DeviceConfig{
		ID: "org/runner/phone", Name: "Phone", Type: config.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "usb", Serial: "SERIAL"},
	})
	spec, err := BuildServiceSpec(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	if spec.NetworkMode != "host" || spec.Environment[ADBServerSocketEnv] != ADBServerSocket {
		t.Fatalf("USB ADB topology = mode %q env %#v", spec.NetworkMode, spec.Environment)
	}
	assertDevice(t, spec, "/dev/bus/usb", "/dev/bus/usb")
	assertBind(t, spec, "/home/alice/.android", ContainerAndroidDir, false)
	if spec.Environment[ConfigOwnerUIDEnv] != "1000" || spec.Environment[ConfigOwnerGIDEnv] != "1000" {
		t.Fatalf("host ownership environment = %#v", spec.Environment)
	}
}

func TestBuildServiceSpecBootstrapCanDiscoverHostADBDevices(t *testing.T) {
	// The Dashboard discovers phones before setup persists a device entry, so
	// bootstrap must retain the old host-ADB topology with an empty config.
	host := testHost("/home/alice")
	host.BeforeSetup = true
	cfg := config.Bootstrap()
	spec, err := BuildServiceSpec(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	if spec.NetworkMode != "host" {
		t.Fatalf("bootstrap network mode = %q, want host", spec.NetworkMode)
	}
	if got := spec.Environment[ADBServerSocketEnv]; got != ADBServerSocket {
		t.Fatalf("bootstrap ADB socket = %q, want %q", got, ADBServerSocket)
	}
	assertBind(t, spec, "/home/alice/.android", ContainerAndroidDir, false)
	assertBind(t, spec, "/home/alice/avd-golden", ContainerGoldenRoot, false)
	for _, device := range spec.Devices {
		if device.Source == "/dev/bus/usb" {
			t.Fatal("bootstrap unexpectedly requires USB device mapping")
		}
	}
}

func TestBuildServiceSpecExportsCanonicalComposeProject(t *testing.T) {
	host := testHost("/home/alice/.config/credimi-runner")
	cfg := configForDevices()
	spec, err := BuildServiceSpec(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	want := ProjectName(host.ConfigDir, host.UID)
	if spec.Environment[ComposeProjectEnv] != want {
		t.Fatalf("compose project = %q, want %q", spec.Environment[ComposeProjectEnv], want)
	}
	wantFingerprint := ServiceConfigFingerprint(cfg, true)
	if spec.Environment[AppliedServiceConfigFingerprintEnv] != wantFingerprint {
		t.Fatalf("applied service fingerprint = %q, want %q", spec.Environment[AppliedServiceConfigFingerprintEnv], wantFingerprint)
	}
}

func TestBuildServiceSpecPreservesExposurePortBindings(t *testing.T) {
	for _, tc := range []struct {
		mode, wantAPI string
	}{
		{mode: "manual", wantAPI: "0.0.0.0"},
		{mode: "quick_tunnel", wantAPI: "127.0.0.1"},
		{mode: "named_tunnel", wantAPI: "127.0.0.1"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cfg := configForDevices()
			cfg.Exposure.Mode = tc.mode
			cfg.Server.DashboardListen = "127.0.0.1:18051"
			cfg.Server.APIListen = "0.0.0.0:18050"
			spec, err := BuildServiceSpec(cfg, testHost("/home/alice"))
			if err != nil {
				t.Fatal(err)
			}
			var dashboard, api *PortMapping
			for index := range spec.Ports {
				port := &spec.Ports[index]
				switch port.ContainerPort {
				case "18051":
					dashboard = port
				case "18050":
					api = port
				}
			}
			if dashboard == nil || dashboard.HostIP != "127.0.0.1" {
				t.Fatalf("dashboard binding = %#v", dashboard)
			}
			if api == nil || api.HostIP != tc.wantAPI {
				t.Fatalf("API binding = %#v, want host IP %q", api, tc.wantAPI)
			}
		})
	}
}

func TestBuildServiceSpecUnionsAndDeduplicatesMultiDeviceCapabilities(t *testing.T) {
	home := t.TempDir()
	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	if err := os.MkdirAll(filepath.Dir(knownHosts), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHosts, []byte("known"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := testHost(home)
	cfg := configForDevices(
		config.DeviceConfig{ID: "org/runner/phone", Name: "Phone", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "usb", Serial: "phone"}},
		config.DeviceConfig{ID: "org/runner/emulator", Name: "Emulator", Type: config.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &config.AndroidEmulatorConfig{BaseName: "credimi", GoldenSource: "/avd-golden/credimi-golden"}},
		config.DeviceConfig{ID: "org/runner/redroid", Name: "Redroid", Type: config.DeviceRedroid, Enabled: true, Redroid: &config.RedroidConfig{Host: "redroid", Image: "redroid:latest", DataDir: "/data", DataArchive: "/data.tar", ADBPort: 5555, AVDCTLSSHTarget: "root@redroid", AVDCTLSSHKnownHostsPath: knownHosts}},
	)
	spec, err := BuildServiceSpec(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	if spec.NetworkMode != "host" || spec.Environment[ADBServerSocketEnv] != ADBServerSocket {
		t.Fatalf("multi-device network/ADB topology = %q/%q", spec.NetworkMode, spec.Environment[ADBServerSocketEnv])
	}
	assertBind(t, spec, filepath.Join(home, ".android"), ContainerAndroidDir, false)
	assertBind(t, spec, filepath.Join(home, "avd-golden"), ContainerGoldenRoot, false)
	assertBind(t, spec, knownHosts, knownHosts, true)
	assertDevice(t, spec, "/dev/kvm", "/dev/kvm")
	assertDevice(t, spec, "/dev/bus/usb", "/dev/bus/usb")
	assertNoDuplicateBindTargets(t, spec)
	if len(spec.Devices) != 2 || len(spec.Volumes) != 3 {
		t.Fatalf("multi-device deduplication = %d devices, %d volumes", len(spec.Devices), len(spec.Volumes))
	}
}

func TestServiceFingerprintAndSpecShareCapabilityProjection(t *testing.T) {
	host := testHost("/home/alice")
	emulator := config.DeviceConfig{ID: "org/runner/emulator-a", Type: config.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &config.AndroidEmulatorConfig{BaseName: "a", GoldenSource: "/avd-golden/a-golden"}}
	second := emulator
	second.ID = "org/runner/emulator-b"
	second.AndroidEmulator = &config.AndroidEmulatorConfig{BaseName: "b", GoldenSource: "/avd-golden/b-golden"}
	firstCfg := configForDevices(emulator)
	secondCfg := configForDevices(emulator, second)
	if ServiceConfigFingerprint(firstCfg, true) != ServiceConfigFingerprint(secondCfg, true) {
		t.Fatal("logical second emulator changed the capability fingerprint")
	}
	firstSpec, err := BuildServiceSpec(firstCfg, host)
	if err != nil {
		t.Fatal(err)
	}
	secondSpec, err := BuildServiceSpec(secondCfg, host)
	if err != nil {
		t.Fatal(err)
	}
	delete(firstSpec.Environment, AppliedServiceConfigFingerprintEnv)
	delete(secondSpec.Environment, AppliedServiceConfigFingerprintEnv)
	if !reflect.DeepEqual(firstSpec, secondSpec) {
		t.Fatalf("same service capability projection produced different specs:\nfirst=%+v\nsecond=%+v", firstSpec, secondSpec)
	}
}

func TestBuildServiceSpecPreservesRedroidKnownHostsResolution(t *testing.T) {
	home := t.TempDir()
	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	if err := os.MkdirAll(filepath.Dir(knownHosts), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHosts, []byte("known"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := testHost(home)
	for _, tc := range []struct {
		name, configured, want string
	}{
		{name: "explicit", configured: knownHosts, want: knownHosts},
		{name: "host default", want: knownHosts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configForDevices(config.DeviceConfig{ID: "org/runner/redroid", Name: "Redroid", Type: config.DeviceRedroid, Enabled: true, Redroid: &config.RedroidConfig{Host: "redroid", Image: "redroid:latest", DataDir: "/data", DataArchive: "/data.tar", ADBPort: 5555, AVDCTLSSHTarget: "root@redroid", AVDCTLSSHKnownHostsPath: tc.configured}})
			spec, err := BuildServiceSpec(cfg, host)
			if err != nil {
				t.Fatal(err)
			}
			assertBind(t, spec, tc.want, tc.want, true)
		})
	}
}

func TestBuildServiceSpecExportsRedroidKnownHostsMetadata(t *testing.T) {
	home := t.TempDir()
	a := filepath.Join(home, "known-a")
	b := filepath.Join(home, "known-b")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("known"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := configForDevices(
		config.DeviceConfig{ID: "org/runner/a", Type: config.DeviceRedroid, Enabled: true, Redroid: &config.RedroidConfig{AVDCTLSSHTarget: "root@a", AVDCTLSSHKnownHostsPath: a}},
		config.DeviceConfig{ID: "org/runner/b", Type: config.DeviceRedroid, Enabled: true, Redroid: &config.RedroidConfig{AVDCTLSSHTarget: "root@b", AVDCTLSSHKnownHostsPath: b}},
	)
	spec, err := BuildServiceSpec(cfg, testHost(home))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal([]byte(spec.Environment[AppliedServiceRedroidKnownHostsEnv]), &got); err != nil {
		t.Fatal(err)
	}
	want := ServiceRedroidKnownHostsForConfig(cfg)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("known-host metadata = %#v, want %#v", got, want)
	}
}

func TestServiceCompatibilityMatchesAppliedSupersets(t *testing.T) {
	redroid := func(path string) config.DeviceConfig {
		return config.DeviceConfig{Type: config.DeviceRedroid, Enabled: true, Redroid: &config.RedroidConfig{AVDCTLSSHTarget: "root@redroid", ADBPort: 5555, AVDCTLSSHKnownHostsPath: path}}
	}
	applied := config.Bootstrap()
	if err := config.ApplyDefaults(&applied); err != nil {
		t.Fatal(err)
	}
	applied.Devices = []config.DeviceConfig{redroid("/known/a"), redroid("/known/b")}
	desired := applied
	desired.Devices = []config.DeviceConfig{redroid("/known/a")}
	if !ServiceConfigsCompatible(applied, desired, true) {
		t.Fatal("applied Redroid mount superset was not accepted")
	}
	missing := applied
	missing.Devices = []config.DeviceConfig{redroid("/known/c")}
	if ServiceConfigsCompatible(applied, missing, true) {
		t.Fatal("missing Redroid mount was accepted")
	}

	applied.Devices = []config.DeviceConfig{{Type: config.DeviceAndroidEmulator, Enabled: true}}
	desired.Devices = nil
	if !ServiceConfigsCompatible(applied, desired, true) {
		t.Fatal("applied emulator capability was not retained")
	}
	capabilities := ServiceCapabilitiesForConfig(applied)
	fingerprint := ServiceConfigFingerprint(applied, true)
	if !ServiceConfigCompatibleWithFingerprint(desired, true, fingerprint, capabilities) {
		t.Fatal("fingerprint compatibility rejected an applied capability superset")
	}
	capabilities.RedroidKnownHosts = []string{"/known/a"}
	missing = applied
	missing.Devices = []config.DeviceConfig{redroid("/known/b")}
	if ServiceConfigCompatibleWithFingerprint(missing, true, fingerprint, capabilities) {
		t.Fatal("fingerprint compatibility accepted a missing Redroid mount")
	}
}

func TestServiceSpecFingerprintIncludesCapabilityFields(t *testing.T) {
	home := t.TempDir()
	knownHostsPath := filepath.Join(home, ".ssh", "known_hosts")
	otherKnownHostsPath := filepath.Join(home, ".ssh", "other_known_hosts")
	if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{knownHostsPath, otherKnownHostsPath} {
		if err := os.WriteFile(path, []byte("known"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	host := testHost(home)
	base := configForDevices(config.DeviceConfig{ID: "org/runner/emulator", Name: "Emulator", Type: config.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &config.AndroidEmulatorConfig{BaseName: "credimi", GoldenSource: "/avd-golden/credimi-golden"}})
	spec, err := BuildServiceSpec(base, host)
	if err != nil {
		t.Fatal(err)
	}
	changedHost := host
	changedHost.GoldenRoot = "/home/bob/avd-golden"
	changed, _ := BuildServiceSpec(base, changedHost)
	if spec.Fingerprint() == changed.Fingerprint() {
		t.Fatal("golden root did not affect fingerprint")
	}
	changedHost = host
	changedHost.HasKVM = false
	changed, _ = BuildServiceSpec(base, changedHost)
	if spec.Fingerprint() == changed.Fingerprint() {
		t.Fatal("KVM capability did not affect fingerprint")
	}
	phone := configForDevices(config.DeviceConfig{ID: "org/runner/phone", Name: "Phone", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "usb", Serial: "phone"}})
	phoneSpec, _ := BuildServiceSpec(phone, host)
	if spec.Fingerprint() == phoneSpec.Fingerprint() {
		t.Fatal("USB capability did not affect fingerprint")
	}
	knownHosts := configForDevices(config.DeviceConfig{ID: "org/runner/redroid", Name: "Redroid", Type: config.DeviceRedroid, Enabled: true, Redroid: &config.RedroidConfig{Host: "redroid", Image: "redroid:latest", DataDir: "/data", DataArchive: "/data.tar", ADBPort: 5555, AVDCTLSSHTarget: "root@redroid", AVDCTLSSHKnownHostsPath: knownHostsPath}})
	knownSpec, _ := BuildServiceSpec(knownHosts, host)
	knownHosts.Devices[0].Redroid.AVDCTLSSHKnownHostsPath = otherKnownHostsPath
	otherKnownSpec, _ := BuildServiceSpec(knownHosts, host)
	if knownSpec.Fingerprint() == otherKnownSpec.Fingerprint() {
		t.Fatal("known-hosts path did not affect fingerprint")
	}
	originalPhoneSpec, _ := BuildServiceSpec(phone, host)
	phoneSpec.Environment[ADBServerSocketEnv] = "tcp:127.0.0.1:5038"
	if phoneSpec.Fingerprint() == originalPhoneSpec.Fingerprint() {
		t.Fatal("ADB socket did not affect fingerprint")
	}
}

func TestRenderServiceComposeDeclaresNamedVolumes(t *testing.T) {
	spec := ServiceSpec{Image: "runner:test", PullPolicy: "never", NetworkMode: "bridge", RestartPolicy: "always", Command: []string{"internal-service"}, Environment: map[string]string{}, Volumes: []NamedVolume{{Name: "state", Target: "/state"}, {Name: "tools", Target: "/tools"}}}
	content := RenderServiceCompose(spec)
	var document struct {
		Services map[string]struct {
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if err := yaml.Unmarshal([]byte(content), &document); err != nil {
		t.Fatalf("rendered Compose is not YAML: %v\n%s", err, content)
	}
	for _, mount := range document.Services["runner"].Volumes {
		name := strings.SplitN(mount, ":", 2)[0]
		if _, ok := document.Volumes[name]; ok {
			continue
		}
		if name == "state" || name == "tools" {
			t.Fatalf("named volume %q is not declared: %s", name, content)
		}
	}
}

func TestRepresentativeServiceComposePassesDockerConfig(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is unavailable")
	}
	home := t.TempDir()
	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	if err := os.MkdirAll(filepath.Dir(knownHosts), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHosts, []byte("known"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := testHost(home)
	cases := []struct {
		name string
		cfg  config.Config
	}{
		{name: "emulator", cfg: configForDevices(config.DeviceConfig{ID: "org/runner/emulator", Name: "Emulator", Type: config.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &config.AndroidEmulatorConfig{BaseName: "credimi", GoldenSource: "/avd-golden/credimi-golden"}})},
		{name: "physical phone", cfg: configForDevices(config.DeviceConfig{ID: "org/runner/phone", Name: "Phone", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "usb", Serial: "phone"}})},
		{name: "redroid", cfg: configForDevices(config.DeviceConfig{ID: "org/runner/redroid", Name: "Redroid", Type: config.DeviceRedroid, Enabled: true, Redroid: &config.RedroidConfig{Host: "redroid", Image: "redroid:latest", DataDir: "/data", DataArchive: "/data.tar", ADBPort: 5555, AVDCTLSSHTarget: "root@redroid", AVDCTLSSHKnownHostsPath: knownHosts}})},
		{name: "multi-device", cfg: configForDevices(
			config.DeviceConfig{ID: "org/runner/phone", Name: "Phone", Type: config.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &config.AndroidPhysicalConfig{Transport: "usb", Serial: "phone"}},
			config.DeviceConfig{ID: "org/runner/emulator", Name: "Emulator", Type: config.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &config.AndroidEmulatorConfig{BaseName: "credimi", GoldenSource: "/avd-golden/credimi-golden"}},
			config.DeviceConfig{ID: "org/runner/redroid", Name: "Redroid", Type: config.DeviceRedroid, Enabled: true, Redroid: &config.RedroidConfig{Host: "redroid", Image: "redroid:latest", DataDir: "/data", DataArchive: "/data.tar", ADBPort: 5555, AVDCTLSSHTarget: "root@redroid", AVDCTLSSHKnownHostsPath: knownHosts}},
		)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteServiceComposeWithHost(dir, tc.cfg, host); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("docker", "compose", "-f", filepath.Join(dir, "service-compose.yaml"), "config", "--quiet")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("docker compose config: %v\n%s", err, output)
			}
		})
	}
}

func configForDevices(devices ...config.DeviceConfig) config.Config {
	cfg := config.Bootstrap()
	cfg.Android.RunnerImage = "runner:test"
	cfg.Android.PullPolicy = "never"
	cfg.Android.Network = "credimi-runner"
	cfg.Android.StateVolume = "state"
	cfg.Android.ToolCacheVolume = "tools"
	cfg.Android.SDKVolume = "sdk"
	cfg.Devices = devices
	return cfg
}

func testHost(configDir string) HostContext {
	return HostContext{ConfigDir: configDir, HomeDir: configDir, UID: 1000, GID: 1000, AndroidDir: filepath.Join(configDir, ".android"), AVDHome: filepath.Join(configDir, ".android", "avd"), GoldenRoot: filepath.Join(configDir, "avd-golden"), HasKVM: true, OS: "linux"}
}

func assertBind(t *testing.T, spec ServiceSpec, source, target string, readOnly bool) {
	t.Helper()
	for _, mount := range spec.BindMounts {
		if mount.Source == source && mount.Target == target && mount.ReadOnly == readOnly {
			return
		}
	}
	t.Fatalf("bind mount %s -> %s (ro=%t) missing: %#v", source, target, readOnly, spec.BindMounts)
}

func assertDevice(t *testing.T, spec ServiceSpec, source, target string) {
	t.Helper()
	for _, device := range spec.Devices {
		if device.Source == source && device.Target == target {
			return
		}
	}
	t.Fatalf("device %s -> %s missing: %#v", source, target, spec.Devices)
}

func assertNoDuplicateBindTargets(t *testing.T, spec ServiceSpec) {
	t.Helper()
	seen := map[string]bool{}
	for _, mount := range spec.BindMounts {
		if seen[mount.Target] {
			t.Fatalf("duplicate bind target %q: %#v", mount.Target, spec.BindMounts)
		}
		seen[mount.Target] = true
	}
}

func containsArgs(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
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
	writeServiceCompose(t, dir, cfg)
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
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewDockerManager(dir, "")
	m.Runner = failingCommandRunner{err: want}
	if _, err := m.Status(context.Background()); !errors.Is(err, want) {
		t.Fatalf("status error=%v", err)
	}
}
