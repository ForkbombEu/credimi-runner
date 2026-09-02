package servicemanager

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
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

// liveDashboardURL returns a verified controller endpoint when one exists.
func liveDashboardURL(ctx context.Context, dir string) string {
	var metadata struct {
		ControllerID      string `json:"controller_id"`
		ConfigFingerprint string `json:"config_fingerprint"`
		ProbeURL          string `json:"probe_url"`
		PublicURL         string `json:"public_url"`
		IdentityToken     string `json:"identity_token"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "controller.json"))
	if err != nil || json.Unmarshal(raw, &metadata) != nil || metadata.PublicURL == "" || metadata.ProbeURL == "" || metadata.IdentityToken == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata.ProbeURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("X-Credimi-Controller-Token", metadata.IdentityToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var identity struct {
		ControllerID      string `json:"controller_id"`
		ConfigFingerprint string `json:"config_fingerprint"`
	}
	if json.NewDecoder(resp.Body).Decode(&identity) != nil || identity.ControllerID != metadata.ControllerID || identity.ConfigFingerprint != metadata.ConfigFingerprint {
		return ""
	}
	return strings.TrimRight(metadata.PublicURL, "/")
}
