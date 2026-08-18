package runtime

import (
	"os"
	"path/filepath"
	"strings"
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
