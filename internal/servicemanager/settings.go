package servicemanager

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/forkbombeu/credimi-runner/internal/atomicfile"
)

const serviceSettingsName = "service-settings.json"

type serviceSettings struct {
	Autostart bool `json:"autostart"`
}

func serviceSettingsPath(configDir string) string {
	return filepath.Join(configDir, serviceSettingsName)
}

func loadAutostart(configDir string) (bool, error) {
	raw, err := os.ReadFile(serviceSettingsPath(configDir))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read service settings: %w", err)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, fmt.Errorf("decode service settings: expected an object")
	}
	var settings serviceSettings
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return false, fmt.Errorf("decode service settings: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return false, fmt.Errorf("decode service settings: multiple JSON values")
		}
		return false, fmt.Errorf("decode service settings: %w", err)
	}
	return settings.Autostart, nil
}

func saveAutostart(configDir string, enabled bool) error {
	raw, err := json.Marshal(serviceSettings{Autostart: enabled})
	if err != nil {
		return fmt.Errorf("encode service settings: %w", err)
	}
	raw = append(raw, '\n')
	if err := atomicfile.WriteAtomic(serviceSettingsPath(configDir), 0o600, atomicfile.FromEnvironment(), func(w io.Writer) error {
		_, err := w.Write(raw)
		return err
	}); err != nil {
		return fmt.Errorf("write service settings: %w", err)
	}
	return nil
}
