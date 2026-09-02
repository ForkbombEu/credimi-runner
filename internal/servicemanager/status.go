package servicemanager

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	controller "github.com/forkbombeu/credimi-runner/internal/controller/identity"
)

func populateRuntimeState(dir string, status *Status) {
	raw, err := os.ReadFile(filepath.Join(dir, "runtime-state.json"))
	if err != nil {
		return
	}
	var state struct {
		Desired   string `json:"desired"`
		Actual    string `json:"actual"`
		LastError string `json:"last_error"`
	}
	if json.Unmarshal(raw, &state) == nil {
		status.RuntimeDesired, status.RuntimeActual, status.RuntimeError = state.Desired, state.Actual, state.LastError
	}
}

func desiredDashboardURL(cfg runnerconfig.Config) string {
	host, port, err := net.SplitHostPort(cfg.Server.DashboardListen)
	if err != nil || port == "" {
		host, port = "127.0.0.1", "8051"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func isEquivalentListener(wantHost, wantPort, actualHost, actualPort string) bool {
	if wantPort != actualPort {
		return false
	}
	wantHost = strings.TrimSpace(wantHost)
	actualHost = strings.TrimSpace(actualHost)
	if strings.EqualFold(wantHost, "localhost") {
		wantHost = "127.0.0.1"
	}
	if strings.EqualFold(actualHost, "localhost") {
		actualHost = "127.0.0.1"
	}
	if strings.EqualFold(wantHost, actualHost) {
		return true
	}
	return false
}

func readLiveController(ctx context.Context, dir string) (controller.Metadata, error) {
	metadata, err := controller.ReadMetadata(dir)
	if err != nil {
		return metadata, err
	}
	if strings.TrimSpace(metadata.PublicURL) == "" {
		return controller.Metadata{}, errors.New("controller metadata has no public URL")
	}
	if err := controller.Probe(ctx, metadata); err != nil {
		return controller.Metadata{}, err
	}
	return metadata, nil
}
