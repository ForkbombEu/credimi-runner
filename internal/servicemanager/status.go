package servicemanager

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	wantIP, actualIP := net.ParseIP(wantHost), net.ParseIP(actualHost)
	return wantIP != nil && actualIP != nil && wantIP.IsLoopback() && actualIP.IsLoopback()
}

type liveControllerMetadata struct {
	ControllerID      string `json:"controller_id"`
	ConfigFingerprint string `json:"config_fingerprint"`
	ProbeURL          string `json:"probe_url"`
	PublicURL         string `json:"public_url"`
	IdentityToken     string `json:"identity_token"`
	ListenHost        string `json:"listen_host"`
	ListenPort        int    `json:"listen_port"`
}

func readLiveController(ctx context.Context, dir string) (liveControllerMetadata, error) {
	var metadata liveControllerMetadata
	raw, err := os.ReadFile(filepath.Join(dir, "controller.json"))
	if err != nil {
		return metadata, err
	}
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata.PublicURL == "" || metadata.ProbeURL == "" || metadata.IdentityToken == "" {
		if err != nil {
			return metadata, err
		}
		return metadata, os.ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata.ProbeURL, nil)
	if err != nil {
		return metadata, err
	}
	req.Header.Set("X-Credimi-Controller-Token", metadata.IdentityToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return metadata, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return metadata, fmt.Errorf("controller probe returned %s", resp.Status)
	}
	var identity struct {
		ControllerID      string `json:"controller_id"`
		ConfigFingerprint string `json:"config_fingerprint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&identity); err != nil {
		return metadata, err
	}
	if identity.ControllerID != metadata.ControllerID || identity.ConfigFingerprint != metadata.ConfigFingerprint {
		return metadata, os.ErrInvalid
	}
	return metadata, nil
}

// liveDashboardURL returns a verified controller endpoint when one exists.
func liveDashboardURL(ctx context.Context, dir string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	metadata, err := readLiveController(probeCtx, dir)
	if err != nil {
		return ""
	}
	return strings.TrimRight(metadata.PublicURL, "/")
}
