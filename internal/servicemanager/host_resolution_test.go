package servicemanager

import (
	"net"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func TestResolveServiceHostContextUsesHostAuthoritativeDNS(t *testing.T) {
	oldLookup := lookupServiceHostIPs
	lookupServiceHostIPs = func(name string) ([]net.IP, error) {
		switch name {
		case "runner-host.example":
			return []net.IP{net.ParseIP("192.168.178.120")}, nil
		case "remote.example":
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		default:
			t.Fatalf("unexpected hostname lookup %q", name)
			return nil, nil
		}
	}
	t.Cleanup(func() { lookupServiceHostIPs = oldLookup })
	cfg := config.Bootstrap()
	cfg.Credimi.URL = "http://runner-host.example:8090"
	cfg.Temporal.Address = "remote.example:7233"
	host, err := ResolveServiceHostContext(cfg, HostContext{OS: "linux", HostAddresses: []string{"192.168.178.120"}})
	if err != nil {
		t.Fatal(err)
	}
	if host.ResolvedHostLocality["runner-host.example"] != "192.168.178.120" || host.ResolvedHostLocality["remote.example"] != "" {
		t.Fatalf("resolved locality = %#v", host.ResolvedHostLocality)
	}
}

func TestResolveServiceHostContextRecordsDockerOnlyNamesAsNonLocal(t *testing.T) {
	oldLookup := lookupServiceHostIPs
	lookupServiceHostIPs = func(string) ([]net.IP, error) { return nil, errDNSFailure{} }
	t.Cleanup(func() { lookupServiceHostIPs = oldLookup })
	cfg := config.Bootstrap()
	cfg.Android.Network = "credimi-stack"
	cfg.Credimi.URL = "http://credimi:8090"
	cfg.Temporal.Address = "temporal:7233"
	host, err := ResolveServiceHostContext(cfg, HostContext{OS: "linux", HostAddresses: []string{"192.168.178.120"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := host.ResolvedHostLocality["credimi"]; !ok {
		t.Fatalf("credimi locality was not evaluated: %#v", host.ResolvedHostLocality)
	}
	if _, ok := host.ResolvedHostLocality["temporal"]; !ok {
		t.Fatalf("temporal locality was not evaluated: %#v", host.ResolvedHostLocality)
	}
	if ServiceNetworkModeForConfig(cfg, host) != "bridge" {
		t.Fatal("Docker-only names selected host networking")
	}
	spec, err := BuildServiceSpec(cfg, testHost(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.ExtraHosts) != 0 {
		t.Fatalf("Docker-only names received extra hosts: %#v", spec.ExtraHosts)
	}
	capabilities := ServiceCapabilities{NetworkMode: spec.NetworkMode, ResolvedHostLocality: host.ResolvedHostLocality}
	if !ServiceConfigCompatibleWithFingerprint(cfg, true, ServiceConfigFingerprintForHost(cfg, true, host), capabilities) {
		t.Fatal("evaluated Docker-only names did not settle compatibility")
	}
}

type errDNSFailure struct{}

func (errDNSFailure) Error() string { return "name does not resolve on host" }

func TestBuildServiceSpecMapsHostLocalDependencyHostname(t *testing.T) {
	host := testHost(t.TempDir())
	host.HostAddresses = []string{"192.168.178.120"}
	host.ResolvedHostLocality = map[string]string{"runner-host.example": "192.168.178.120"}
	cfg := config.Bootstrap()
	cfg.Credimi.URL = "http://runner-host.example:8090"
	spec, err := BuildServiceSpec(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	if spec.NetworkMode != "host" {
		t.Fatalf("network mode = %q, want host", spec.NetworkMode)
	}
	found := false
	for _, extraHost := range spec.ExtraHosts {
		if extraHost == "runner-host.example:192.168.178.120" {
			found = true
		}
	}
	if !found {
		t.Fatalf("extra hosts = %#v", spec.ExtraHosts)
	}
}

func TestServiceHostLocalityUnknownIsConservative(t *testing.T) {
	cfg := config.Bootstrap()
	cfg.Credimi.URL = "http://new-host.example:8090"
	host := HostContext{OS: "linux", HostAddresses: []string{"192.0.2.10"}, ResolvedHostLocality: map[string]string{}}
	if !ServiceHostLocalityUnknown(cfg, host) {
		t.Fatal("unresolved service hostname was not reported as unknown")
	}
	if ServiceHostLocalityUnknown(cfg, HostContext{OS: "linux", ResolvedHostLocality: map[string]string{"new-host.example": ""}}) {
		t.Fatal("authoritatively remote hostname was reported as unknown")
	}
}
