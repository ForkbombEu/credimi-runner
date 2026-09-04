//go:build !darwin

package servicemanager

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

type legacyContainerRunner struct {
	containers []inspectedContainer
	calls      [][]string
}

func (r *legacyContainerRunner) Run(_ context.Context, name string, args []string, _ []string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil
}

func (r *legacyContainerRunner) Output(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
	if containsArgs(args, "-aq") {
		ids := make([]string, 0, len(r.containers))
		for _, container := range r.containers {
			ids = append(ids, container.ID)
		}
		return []byte(strings.Join(ids, "\n")), nil
	}
	if len(args) > 0 && args[0] == "inspect" {
		return json.Marshal(r.containers)
	}
	if containsArgs(args, "ps") && containsArgs(args, "runner") {
		return []byte("container123\n"), nil
	}
	if containsArgs(args, ".Image") {
		return []byte("sha256:local\n"), nil
	}
	if containsArgs(args, ".RepoDigests") {
		return []byte(`[]`), nil
	}
	return nil, nil
}

func historicalLabels(configDir, service string) map[string]string {
	return map[string]string{
		"com.docker.compose.service":              service,
		"com.docker.compose.project":              "credimi-runner",
		"com.docker.compose.project.working_dir":  configDir,
		"com.docker.compose.project.config_files": filepath.Join(configDir, "docker-compose.yaml"),
	}
}

func historicalContainer(id, name, image string, command []string, labels map[string]string) inspectedContainer {
	container := inspectedContainer{ID: id, Name: name}
	container.State.Running = true
	container.Config.Image = image
	container.Config.Cmd = command
	container.Config.Labels = labels
	return container
}

func hostNetwork(container *inspectedContainer) {
	container.HostConfig.NetworkMode = "host"
}

func publishedPort(container *inspectedContainer, port string) {
	container.HostConfig.PortBindings = map[string][]containerPortBinding{
		port + "/tcp": {{HostPort: port}},
	}
}

func desiredService(network string, ports ...string) ServiceSpec {
	spec := ServiceSpec{NetworkMode: network}
	for _, port := range ports {
		spec.Ports = append(spec.Ports, PortMapping{HostPort: port, ContainerPort: port})
	}
	return spec
}

func TestLegacyPreflightClassificationUsesHistoricalRunnerForms(t *testing.T) {
	forms := []struct {
		name    string
		image   string
		command []string
	}{
		{"installer USB", historicalPhoneImage + ":latest", []string{"--usb"}},
		{"installer emulator", historicalEmulatorImage + ":latest", []string{"--emulator"}},
		{"installer no device", historicalPhoneImage + ":latest", []string{"--no-device"}},
		{"pre-unified USB", historicalPhoneImage + ":latest", []string{"--host-adb", "--usb"}},
		{"pre-unified Wi-Fi", historicalPhoneImage + ":latest", []string{"192.0.2.10:5555"}},
		{"shared inventory", historicalPhoneImage + ":latest", []string{"--inventory"}},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			container := historicalContainer("legacy", "/runner", form.image, form.command, nil)
			if !isHistoricalRunner(container) {
				t.Fatalf("form was not recognized: %+v", form)
			}
		})
	}
}

func TestLegacyPreflightIgnoresSafeContainers(t *testing.T) {
	dir := t.TempDir()
	host, err := ResolveHostContext(dir)
	if err != nil {
		t.Fatal(err)
	}
	project := ProjectName(dir, host.UID)
	canonical := historicalContainer("current", "/current", "ghcr.io/forkbombeu/credimi-runner:latest", []string{"internal-service"}, map[string]string{
		serviceManagedLabel: "true", serviceProjectLabel: project,
	})
	foreignStopped := historicalContainer("stopped", "/old-runner", historicalPhoneImage+":latest", []string{"--usb"}, historicalLabels(filepath.Join(dir, "other"), "runner"))
	foreignStopped.State.Running = false
	foreignRunningUnused := historicalContainer("unused", "/old-runner-2", historicalEmulatorImage+":latest", []string{"--emulator"}, historicalLabels(filepath.Join(dir, "other-2"), "runner"))
	publishedPort(&foreignRunningUnused, "9000")
	foreignHelper := historicalContainer("helper", "/credimi-helper", "ghcr.io/example/credimi-helper:latest", nil, nil)
	temporal := historicalContainer("temporal", "/credimi-temporal-postgres-1", "postgres:16", nil, nil)
	for _, tc := range []struct {
		name      string
		container inspectedContainer
	}{
		{"canonical service", canonical},
		{"stopped foreign historical runner", foreignStopped},
		{"running foreign non-conflicting runner", foreignRunningUnused},
		{"unrelated credimi helper", foreignHelper},
		{"temporal project", temporal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &legacyContainerRunner{containers: []inspectedContainer{tc.container}}
			m := NewDockerManager(dir, "")
			m.host = host
			m.Runner = r
			if err := m.preflightLegacy(context.Background(), config.Bootstrap(), desiredService("bridge", "8050")); err != nil {
				t.Fatal(err)
			}
			if len(r.calls) != 0 {
				t.Fatalf("unexpected cleanup calls=%v", r.calls)
			}
		})
	}
}

func TestLegacyPreflightRemovesOwnedConflictingContainerByExactID(t *testing.T) {
	dir := t.TempDir()
	container := historicalContainer("legacy-id", "/old-runner", historicalPhoneImage+":latest", []string{"--usb"}, historicalLabels(dir, "runner"))
	hostNetwork(&container)
	r := &legacyContainerRunner{containers: []inspectedContainer{container}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	if err := m.preflightLegacy(context.Background(), config.Bootstrap(), desiredService("host")); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || !containsArgs(r.calls[0], "rm") || !containsArgs(r.calls[0], "-f") || !containsArgs(r.calls[0], "legacy-id") {
		t.Fatalf("cleanup calls=%v", r.calls)
	}
	if containsArgs(r.calls[0], "down") || containsArgs(r.calls[0], "prune") {
		t.Fatalf("broad cleanup call=%v", r.calls)
	}
}

func TestLegacyPreflightRefusesAmbiguousConflictingContainer(t *testing.T) {
	dir := t.TempDir()
	container := historicalContainer("other-id", "/other-runner", historicalEmulatorImage+":latest", []string{"--emulator"}, historicalLabels(filepath.Join(dir, "other"), "runner"))
	hostNetwork(&container)
	r := &legacyContainerRunner{containers: []inspectedContainer{container}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	err := m.preflightLegacy(context.Background(), config.Bootstrap(), desiredService("host"))
	if err == nil || !strings.Contains(err.Error(), "docker inspect other-id") || !strings.Contains(err.Error(), "could not be proven") {
		t.Fatalf("error=%v", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("ambiguous container was cleaned: %v", r.calls)
	}
}

func TestLegacyAmbiguityStopsStartBeforeComposeUp(t *testing.T) {
	dir := t.TempDir()
	container := historicalContainer("other-id", "/other-runner", historicalPhoneImage+":latest", []string{"--no-device"}, historicalLabels(filepath.Join(dir, "other"), "runner"))
	hostNetwork(&container)
	r := &legacyContainerRunner{containers: []inspectedContainer{container}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	m.LoadConfig = func() (config.Config, error) { return config.Bootstrap(), nil }
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("ambiguous legacy container did not block start")
	}
	for _, call := range r.calls {
		if containsArgs(call, "compose") && containsArgs(call, "up") {
			t.Fatalf("Compose up ran after ambiguity: %v", r.calls)
		}
	}
}

func TestLegacyPreflightRequiresActualConflictForCleanup(t *testing.T) {
	dir := t.TempDir()
	container := historicalContainer("same-id", "/old-runner", historicalPhoneImage+":latest", []string{"--usb"}, historicalLabels(dir, "runner"))
	container.HostConfig.NetworkMode = "bridge"
	publishedPort(&container, "9000")
	r := &legacyContainerRunner{containers: []inspectedContainer{container}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	if err := m.preflightLegacy(context.Background(), config.Bootstrap(), desiredService("bridge", "8050")); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("non-conflicting owned historical runner was removed: %v", r.calls)
	}
}

func TestLegacyPreflightDetectsPublishedPortFromNetworkSettings(t *testing.T) {
	dir := t.TempDir()
	container := historicalContainer("other-id", "/other-runner", historicalEmulatorImage+":latest", []string{"--emulator"}, historicalLabels(filepath.Join(dir, "other"), "runner"))
	container.NetworkSettings.Ports = map[string][]containerPortBinding{
		"8050/tcp": {{HostPort: "8050"}},
	}
	r := &legacyContainerRunner{containers: []inspectedContainer{container}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	err := m.preflightLegacy(context.Background(), config.Bootstrap(), desiredService("bridge", "8050"))
	if err == nil || !strings.Contains(err.Error(), "other-id") {
		t.Fatalf("error=%v", err)
	}
}

func TestLegacyPreflightUsesCustomPublishedPort(t *testing.T) {
	for _, tc := range []struct {
		name       string
		legacyPort string
		desired    string
		conflict   bool
	}{
		{"custom port conflict", "9123", "9123", true},
		{"different port is safe", "8050", "9123", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			container := historicalContainer("legacy", "/old-runner", historicalEmulatorImage+":latest", []string{"--emulator"}, historicalLabels(filepath.Join(dir, "other"), "runner"))
			publishedPort(&container, tc.legacyPort)
			r := &legacyContainerRunner{containers: []inspectedContainer{container}}
			m := NewDockerManager(dir, "")
			m.Runner = r
			err := m.preflightLegacy(context.Background(), config.Bootstrap(), desiredService("bridge", tc.desired))
			if tc.conflict && (err == nil || !strings.Contains(err.Error(), "legacy")) {
				t.Fatalf("error=%v, want conflict", err)
			}
			if !tc.conflict && err != nil {
				t.Fatalf("error=%v, want no conflict", err)
			}
		})
	}
}

func TestLegacyPreflightUsesExplicitHostNetworkPort(t *testing.T) {
	tests := []struct {
		name     string
		env      []string
		desired  string
		conflict bool
	}{
		{name: "PORT matches desired", env: []string{"PORT=9123"}, desired: "9123", conflict: true},
		{name: "PORT does not activate fallback", env: []string{"PORT=9123"}, desired: "8050"},
		{name: "no explicit port uses fallback", desired: "8050", conflict: true},
		{name: "RUNNER_PORT matches desired", env: []string{"RUNNER_PORT=9123"}, desired: "9123", conflict: true},
		{name: "RUNNER_PORT does not activate fallback", env: []string{"RUNNER_PORT=9123"}, desired: "8050"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			container := historicalContainer("legacy", "/old-runner", historicalPhoneImage+":latest", []string{"--host-adb", "--usb"}, historicalLabels(filepath.Join(dir, "other"), "runner"))
			hostNetwork(&container)
			container.Config.Env = tc.env
			r := &legacyContainerRunner{containers: []inspectedContainer{container}}
			m := NewDockerManager(dir, "")
			m.Runner = r
			cfg := config.Bootstrap()
			cfg.Server.APIListen = "127.0.0.1:" + tc.desired
			err := m.preflightLegacy(context.Background(), cfg, desiredService("host"))
			if tc.conflict && (err == nil || !strings.Contains(err.Error(), "legacy")) {
				t.Fatalf("error=%v, want custom host-port conflict", err)
			}
			if !tc.conflict && err != nil {
				t.Fatalf("error=%v, want no conflict", err)
			}
		})
	}
}

func TestLegacyPreflightCleansOwnedHistoricalModes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		image   string
		command []string
	}{
		{"emulator", historicalEmulatorImage + ":latest", []string{"--emulator"}},
		{"no device", historicalPhoneImage + ":latest", []string{"--no-device"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			container := historicalContainer("legacy-"+tc.name, "/old-runner", tc.image, tc.command, historicalLabels(dir, "runner"))
			publishedPort(&container, "8050")
			r := &legacyContainerRunner{containers: []inspectedContainer{container}}
			m := NewDockerManager(dir, "")
			m.Runner = r
			if err := m.preflightLegacy(context.Background(), config.Bootstrap(), desiredService("bridge", "8050")); err != nil {
				t.Fatal(err)
			}
			if len(r.calls) != 1 || !containsArgs(r.calls[0], "rm") || !containsArgs(r.calls[0], "-f") || !containsArgs(r.calls[0], container.ID) {
				t.Fatalf("cleanup calls=%v", r.calls)
			}
		})
	}
}

func TestLegacyOwnershipRequiresInstallationEvidence(t *testing.T) {
	dir := t.TempDir()
	project := ProjectName(dir, 1000)
	tests := []struct {
		name   string
		labels map[string]string
		env    []string
		want   bool
	}{
		{"compose working directory", historicalLabels(dir, "runner"), nil, true},
		{"compose config file", map[string]string{
			"com.docker.compose.service":              "runner",
			"com.docker.compose.project.config_files": filepath.Join(dir, "docker-compose.yml"),
		}, nil, true},
		{"explicit config environment", nil, []string{"CREDIMI_RUNNER_CONFIG_DIR=" + dir}, true},
		{"container path is not host identity", nil, []string{"CREDIMI_RUNNER_CONFIG_DIR=/app"}, false},
		{"canonical labels are handled separately", map[string]string{serviceManagedLabel: "true", serviceProjectLabel: project}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			container := inspectedContainer{}
			container.Config.Labels = tc.labels
			container.Config.Env = tc.env
			if got := containerBelongsToInstallation(container, dir, project); got != tc.want {
				t.Fatalf("belongs=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestLegacyPreflightErrorsIfInspectionFails(t *testing.T) {
	dir := t.TempDir()
	m := NewDockerManager(dir, "")
	m.Runner = &errorLegacyRunner{}
	err := m.preflightLegacy(context.Background(), config.Bootstrap(), desiredService("bridge", "8050"))
	if err == nil || !strings.Contains(err.Error(), "inspect existing Docker containers") {
		t.Fatalf("error=%v", err)
	}
}

type errorLegacyRunner struct{}

func (*errorLegacyRunner) Run(context.Context, string, []string, []string) error { return nil }
func (*errorLegacyRunner) Output(context.Context, string, []string, []string) ([]byte, error) {
	return nil, errors.New("docker unavailable")
}
