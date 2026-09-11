package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func DefaultDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_CONFIG_DIR")); dir != "" {
		return dir, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("resolve user config directory: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "credimi", "runner"), nil
}

func DefaultPath() (string, error) {
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

func ResolvePath(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return filepath.Abs(explicit)
	}
	return DefaultPath()
}

func DefaultStateDir() (string, error) {
	dir := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "credimi-runner"), nil
}
