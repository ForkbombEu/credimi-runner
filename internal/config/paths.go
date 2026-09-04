package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(dir, "credimi-runner", "config.toml"), nil
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
