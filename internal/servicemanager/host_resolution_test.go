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
	if !host.ResolvedHostLocality["runner-host.example"] || host.ResolvedHostLocality["remote.example"] {
		t.Fatalf("resolved locality = %#v", host.ResolvedHostLocality)
	}
}

func TestServiceHostLocalityUnknownIsConservative(t *testing.T) {
	cfg := config.Bootstrap()
	cfg.Credimi.URL = "http://new-host.example:8090"
	host := HostContext{OS: "linux", HostAddresses: []string{"192.0.2.10"}, ResolvedHostLocality: map[string]bool{}}
	if !ServiceHostLocalityUnknown(cfg, host) {
		t.Fatal("unresolved service hostname was not reported as unknown")
	}
	if ServiceHostLocalityUnknown(cfg, HostContext{OS: "linux", ResolvedHostLocality: map[string]bool{"new-host.example": false}}) {
		t.Fatal("authoritatively remote hostname was reported as unknown")
	}
}
