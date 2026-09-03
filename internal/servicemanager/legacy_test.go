//go:build !darwin

package servicemanager

import (
	"context"
	"encoding/json"
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
		raw, err := json.Marshal(r.containers)
		return raw, err
	}
	if containsArgs(args, "ps") && containsArgs(args, "runner") {
		return []byte("container123\n"), nil
	}
	if containsArgs(args, ".Image") {
		return []byte("sha256:local\n"), nil
	}
	if containsArgs(args, ".RepoDigests") {
		return []byte(`[
  "ghcr.io/forkbombeu/credimi-runner@sha256:applied"
]`), nil
	}
	return nil, nil
}

func historicalContainer(id, name, image string, mounts []string, labels map[string]string) inspectedContainer {
	container := inspectedContainer{ID: id, Name: name}
	container.Config.Image = image
	container.Config.Cmd = []string{"--inventory"}
	container.Config.Labels = labels
	for _, source := range mounts {
		container.Mounts = append(container.Mounts, struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		}{Source: source, Destination: "/app"})
	}
	return container
}

func TestLegacyPreflightIgnoresSafeContainers(t *testing.T) {
	dir := t.TempDir()
	host, err := ResolveHostContext(dir)
	if err != nil {
		t.Fatal(err)
	}
	project := ProjectName(dir, host.UID)
	tests := []struct {
		name      string
		container inspectedContainer
	}{
		{"canonical service", historicalContainer("current", "/current", "ghcr.io/forkbombeu/credimi-runner:latest", nil, map[string]string{serviceManagedLabel: "true", serviceProjectLabel: project})},
		{"unrelated credimi helper", historicalContainer("helper", "/credimi-helper", "ghcr.io/example/credimi-helper:latest", nil, nil)},
		{"temporal project", historicalContainer("temporal", "/credimi-temporal-postgres-1", "postgres:16", nil, nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &legacyContainerRunner{containers: []inspectedContainer{tc.container}}
			m := NewDockerManager(dir, "")
			m.Runner = r
			if err := m.preflightLegacy(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(r.calls) != 0 {
				t.Fatalf("unexpected cleanup calls=%v", r.calls)
			}
		})
	}
}

func TestLegacyPreflightRemovesOwnedContainerByExactID(t *testing.T) {
	dir := t.TempDir()
	r := &legacyContainerRunner{containers: []inspectedContainer{historicalContainer(
		"legacy-id", "/old-runner", historicalPhoneImage+":latest", []string{dir}, nil,
	)}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	if err := m.preflightLegacy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || !containsArgs(r.calls[0], "rm") || !containsArgs(r.calls[0], "-f") || !containsArgs(r.calls[0], "legacy-id") {
		t.Fatalf("cleanup calls=%v", r.calls)
	}
	if containsArgs(r.calls[0], "down") || containsArgs(r.calls[0], "prune") {
		t.Fatalf("broad cleanup call=%v", r.calls[0])
	}
}

func TestLegacyPreflightRefusesAmbiguousOwnership(t *testing.T) {
	dir := t.TempDir()
	r := &legacyContainerRunner{containers: []inspectedContainer{historicalContainer(
		"other-id", "/other-runner", historicalContainerImage(), []string{filepath.Join(dir, "other")}, nil,
	)}}
	m := NewDockerManager(dir, "")
	m.Runner = r
	err := m.preflightLegacy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "docker inspect other-id") || !strings.Contains(err.Error(), "could not be proven") {
		t.Fatalf("error=%v", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("ambiguous container was cleaned: %v", r.calls)
	}
}

func TestLegacyAmbiguityStopsStartBeforeComposeUp(t *testing.T) {
	dir := t.TempDir()
	r := &legacyContainerRunner{containers: []inspectedContainer{historicalContainer("other-id", "/other-runner", historicalContainerImage(), nil, nil)}}
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

func historicalContainerImage() string {
	return historicalEmulatorImage + ":latest"
}

func TestLegacyPreflightUsesHistoricalRunnerSignatures(t *testing.T) {
	for _, image := range []string{historicalPhoneImage + ":latest", historicalEmulatorImage + ":latest"} {
		t.Run(image, func(t *testing.T) {
			dir := t.TempDir()
			container := historicalContainer("legacy", "/runner", image, []string{dir}, nil)
			r := &legacyContainerRunner{containers: []inspectedContainer{container}}
			m := NewDockerManager(dir, "")
			m.Runner = r
			if err := m.preflightLegacy(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
