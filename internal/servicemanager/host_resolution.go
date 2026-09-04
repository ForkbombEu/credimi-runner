package servicemanager

import (
	"net"
	"strings"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

// lookupServiceHostIPs is the only DNS lookup used by service topology. It
// runs in the host process and is replaceable in tests.
var lookupServiceHostIPs = net.LookupIP

// ResolveServiceHostContext enriches host metadata with the host process's
// authoritative answers for names that affect persistent service networking.
func ResolveServiceHostContext(cfg config.Config, host HostContext) (HostContext, error) {
	resolved := map[string]bool{}
	for _, name := range serviceDependencyHostnames(cfg) {
		name = normalizeHostname(name)
		if name == "" || net.ParseIP(name) != nil || name == "localhost" {
			continue
		}
		ips, err := lookupServiceHostIPs(name)
		if err != nil {
			// An unavailable resolver is not evidence that a name is remote.
			// Leave it absent so callers can handle the unknown state
			// conservatively and retry on the next service operation.
			continue
		}
		resolved[name] = serviceIPsBelongToHost(ips, host.HostAddresses)
	}
	host.ResolvedHostLocality = resolved
	return host, nil
}

func serviceIPsBelongToHost(ips []net.IP, addresses []string) bool {
	for _, ip := range ips {
		for _, address := range addresses {
			if own := net.ParseIP(strings.Trim(strings.TrimSpace(address), "[]")); own != nil && own.Equal(ip) {
				return true
			}
		}
	}
	return false
}

func cloneBoolMap(values map[string]bool) map[string]bool {
	if len(values) == 0 {
		return map[string]bool{}
	}
	copy := make(map[string]bool, len(values))
	for key, value := range values {
		copy[normalizeHostname(key)] = value
	}
	return copy
}
