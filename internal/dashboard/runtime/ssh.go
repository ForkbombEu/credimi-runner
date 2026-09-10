package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// DefaultSSHKnownHostsPath returns the host user's OpenSSH known-hosts path.
// CREDIMI_HOST_HOME is supplied by the outer launcher on Linux, where the
// runner process itself may otherwise see the container's /root home.
func DefaultSSHKnownHostsPath() string {
	home := strings.TrimSpace(os.Getenv(HostHomeEnv))
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".ssh", "known_hosts")
}

// EffectiveSSHKnownHostsPath returns the canonical host path for one Redroid
// SSH configuration. An omitted path inherits the host user's default only
// when SSH delegation is enabled; local Redroid has no known-hosts file.
func EffectiveSSHKnownHostsPath(target, path string) string {
	path = strings.TrimSpace(path)
	if path == "" && strings.TrimSpace(target) != "" {
		return DefaultSSHKnownHostsPath()
	}
	return path
}

// AVDCTLSSHArgs derives the avdctl OpenSSH option from the canonical typed
// known-hosts path. The returned string is consumed by avdctl's existing
// space-separated AVDCTL_SSH_ARGS contract.
func AVDCTLSSHArgs(knownHostsPath string) string {
	knownHostsPath = strings.TrimSpace(knownHostsPath)
	if knownHostsPath == "" {
		return ""
	}
	return "-o UserKnownHostsFile=" + knownHostsPath
}

func validateKnownHostsPathValue(deviceID, path string) error {
	if strings.IndexFunc(path, unicode.IsSpace) >= 0 {
		return fmt.Errorf("device %q AVDCTL_SSH_KNOWN_HOSTS_PATH contains whitespace; paths with whitespace are not supported by avdctl SSH arguments", deviceID)
	}
	return nil
}
