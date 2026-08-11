package launcher

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const quickTunnelURLFile = "quick-tunnel-url"

// WriteQuickTunnelURL records the currently running quick-tunnel URL outside
// TOML. The outer launcher owns this ephemeral state and clears it whenever
// the tunnel topology is replaced or stopped.
func WriteQuickTunnelURL(configDir, value string) error {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("invalid quick tunnel URL %q", value)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create quick tunnel state directory: %w", err)
	}
	temporary, err := os.CreateTemp(configDir, ".quick-tunnel-url-*")
	if err != nil {
		return fmt.Errorf("create quick tunnel state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure quick tunnel state: %w", err)
	}
	if _, err := temporary.WriteString(value + "\n"); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write quick tunnel state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close quick tunnel state: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(configDir, quickTunnelURLFile)); err != nil {
		return fmt.Errorf("publish quick tunnel state: %w", err)
	}
	return nil
}

// ReadQuickTunnelURL returns only the URL published by the current launcher.
func ReadQuickTunnelURL(configDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(configDir, quickTunnelURLFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("quick tunnel URL is not available yet")
		}
		return "", fmt.Errorf("read quick tunnel state: %w", err)
	}
	value := strings.TrimSpace(string(raw))
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("quick tunnel state is invalid")
	}
	return value, nil
}

// ClearQuickTunnelURL prevents a later runner container from registering a
// stale hostname after the launcher has stopped or replaced the tunnel.
func ClearQuickTunnelURL(configDir string) error {
	err := os.Remove(filepath.Join(configDir, quickTunnelURLFile))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("clear quick tunnel state: %w", err)
}
