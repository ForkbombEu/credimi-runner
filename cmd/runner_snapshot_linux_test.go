//go:build !darwin

package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/servicecoordination"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
)

type snapshotDockerRunner struct {
	configDir    string
	metadataPath string
	probeURL     string
	composeOnUp  string
}

func hasSnapshotArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func (r *snapshotDockerRunner) Run(_ context.Context, _ string, args []string, _ []string) error {
	if hasSnapshotArg(args, "up") && hasSnapshotArg(args, "runner") {
		raw, err := os.ReadFile(filepath.Join(r.configDir, "service-compose.yaml"))
		if err != nil {
			return err
		}
		r.composeOnUp = string(raw)
		metadata := controller.Metadata{
			Schema: 1, ControllerID: "replacement", ConfigDir: r.configDir,
			ListenPort: 8051, ProbeURL: r.probeURL, PublicURL: r.probeURL,
			ConfigFingerprint: "runtime-plan", IdentityToken: "new-identity",
		}
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		return os.WriteFile(r.metadataPath, encoded, 0o600)
	}
	return nil
}

func (r *snapshotDockerRunner) Output(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
	if hasSnapshotArg(args, "-aq") && !hasSnapshotArg(args, "runner") {
		return nil, nil
	}
	if hasSnapshotArg(args, "ps") && hasSnapshotArg(args, "-q") {
		return []byte("container-a\n"), nil
	}
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "service-fingerprint"):
		for _, line := range strings.Split(r.composeOnUp, "\n") {
			if marker := "io.credimi.runner.service-fingerprint:"; strings.Contains(line, marker) {
				return []byte(strings.Trim(strings.TrimSpace(strings.SplitN(line, marker, 2)[1]), "\"") + "\n"), nil
			}
		}
		return nil, nil
	case strings.Contains(joined, "{{.Image}}"):
		return []byte("sha256:image-a\n"), nil
	case strings.Contains(joined, "RepoDigests"):
		return []byte("[]"), nil
	default:
		return nil, nil
	}
}

func TestApplyServiceRestartRequestBindsDockerRestartToVerifiedSnapshot(t *testing.T) {
	dir := t.TempDir()
	active := stage3Config(t, dir)
	active.Android.RunnerImage = "image:a"
	active.Android.PullPolicy = "never"
	active.Credimi.URL = "http://203.0.113.10:8090"
	active.Temporal.Address = "203.0.113.11:7233"
	if err := runnerconfig.WriteFile(filepath.Join(dir, "config.toml"), active); err != nil {
		t.Fatal(err)
	}
	snapshotA, _, err := runnerconfig.LoadFileSnapshot(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	desired := active
	desired.Android.RunnerImage = "image:b"
	digest := stage3ConfigDigest(t, dir)
	request, err := servicecoordination.NewRestartRequest(digest, true, nowForTest())
	if err != nil {
		t.Fatal(err)
	}

	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Credimi-Controller-Token") != "new-identity" {
			http.Error(w, "identity mismatch", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"controller_id":"replacement","config_fingerprint":"runtime-plan"}`))
	}))
	defer probe.Close()
	metadataPath := filepath.Join(dir, "controller.json")
	oldMetadata := controller.Metadata{Schema: 1, ControllerID: "old", ConfigDir: dir, ProbeURL: probe.URL, PublicURL: probe.URL, IdentityToken: "old-identity"}
	raw, err := json.Marshal(oldMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	runner := &snapshotDockerRunner{configDir: dir, metadataPath: metadataPath, probeURL: probe.URL}
	manager := servicemanager.NewDockerManager(dir, "")
	manager.Runner = runner
	oldSnapshotLoader := loadServiceConfigSnapshot
	loadServiceConfigSnapshot = func(path string) (runnerconfig.Config, string, error) {
		cfg, snapshotDigest, snapshotErr := runnerconfig.LoadFileSnapshot(path)
		if snapshotErr == nil {
			if writeErr := runnerconfig.WriteFile(path, desired); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		return cfg, snapshotDigest, snapshotErr
	}
	t.Cleanup(func() { loadServiceConfigSnapshot = oldSnapshotLoader })

	if err := applyServiceRestartRequest(context.Background(), manager, dir, request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.composeOnUp, "image: image:a") || strings.Contains(runner.composeOnUp, "image: image:b") {
		t.Fatalf("service was not generated from snapshot A: %s", runner.composeOnUp)
	}
	if matches, err := manager.ServiceMatchesConfig(context.Background(), snapshotA); err != nil || !matches {
		t.Fatalf("snapshot A service match=%v err=%v compose=%s", matches, err, runner.composeOnUp)
	}
	if matches, err := manager.ServiceMatchesConfig(context.Background(), desired); err != nil || matches {
		t.Fatalf("superseding config incorrectly matches applied service: %v err=%v", matches, err)
	}
}
