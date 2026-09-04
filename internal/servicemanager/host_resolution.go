package servicemanager

import (
	"net"
	"sort"
	"strings"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

// lookupServiceHostIPs is the only DNS lookup used by service topology. It
// runs in the host process and is replaceable in tests.
var lookupServiceHostIPs = net.LookupIP

// ResolveServiceHostContext enriches host metadata with the host process's
// authoritative answers for names that affect persistent service networking.
func ResolveServiceHostContext(cfg config.Config, host HostContext) HostContext {
	resolved := map[string]string{}
	for _, name := range serviceDependencyHostnames(cfg) {
		name = normalizeHostname(name)
		if name == "" || name == "localhost" {
			continue
		}
		if ip := net.ParseIP(name); ip != nil {
			resolved[name] = serviceHostIP([]net.IP{ip}, host.HostAddresses)
			continue
		}
		ips, err := lookupServiceHostIPs(name)
		if err != nil {
			// The host evaluated the name, but it is not host-local. Docker-only
			// names remain valid because the container's configured network owns
			// their resolution.
			resolved[name] = ""
			continue
		}
		resolved[name] = serviceHostIP(ips, host.HostAddresses)
	}
	host.ResolvedHostLocality = resolved
	return host
}

func serviceHostIP(ips []net.IP, addresses []string) string {
	var matches []string
	for _, ip := range ips {
		if ip.IsLoopback() {
			matches = append(matches, ip.String())
			continue
		}
		for _, address := range addresses {
			if own := net.ParseIP(strings.Trim(strings.TrimSpace(address), "[]")); own != nil && own.Equal(ip) {
				matches = append(matches, own.String())
			}
		}
	}
	if len(matches) > 0 {
		sort.Strings(matches)
		return matches[0]
	}
	return ""
}

func cloneResolvedHostLocality(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[normalizeHostname(key)] = value
	}
	return copy
}
