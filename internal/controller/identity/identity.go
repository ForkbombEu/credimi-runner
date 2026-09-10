// Package identity contains the dependency-free controller metadata protocol.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const ProbeTimeout = 3 * time.Second

type Metadata struct {
	Schema            int       `json:"schema"`
	ControllerID      string    `json:"controller_id"`
	PID               int       `json:"pid"`
	StartedAt         time.Time `json:"started_at"`
	ConfigDir         string    `json:"config_dir"`
	ListenHost        string    `json:"listen_host"`
	ListenPort        int       `json:"listen_port"`
	ProbeURL          string    `json:"probe_url"`
	PublicURL         string    `json:"public_url"`
	ConfigFingerprint string    `json:"config_fingerprint"`
	IdentityToken     string    `json:"identity_token"`
}

func ReadMetadata(configDir string) (Metadata, error) {
	var metadata Metadata
	raw, err := os.ReadFile(filepath.Join(configDir, "controller.json"))
	if err != nil {
		return metadata, err
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return metadata, fmt.Errorf("decode controller metadata: %w", err)
	}
	if metadata.Schema != 1 || metadata.ListenPort < 1 || metadata.ListenPort > 65535 || metadata.ProbeURL == "" || metadata.ControllerID == "" || metadata.IdentityToken == "" {
		return metadata, errors.New("controller metadata is invalid")
	}
	return metadata, nil
}

func Probe(ctx context.Context, metadata Metadata) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata.ProbeURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-Credimi-Controller-Token", metadata.IdentityToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("controller probe returned %s", response.Status)
	}
	var identity struct {
		ControllerID      string `json:"controller_id"`
		ConfigFingerprint string `json:"config_fingerprint"`
	}
	if err := json.NewDecoder(response.Body).Decode(&identity); err != nil {
		return fmt.Errorf("decode controller identity: %w", err)
	}
	if identity.ControllerID != metadata.ControllerID || identity.ConfigFingerprint != metadata.ConfigFingerprint {
		return errors.New("controller identity does not match metadata")
	}
	return nil
}
