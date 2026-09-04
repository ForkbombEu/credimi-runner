package servicemanager

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

func hostResolutionTestHost(configDir string) HostContext {
	return HostContext{
		ConfigDir: configDir, HomeDir: configDir, UID: 1000, GID: 1000,
		AndroidDir: filepath.Join(configDir, ".android"), AVDHome: filepath.Join(configDir, ".android", "avd"),
		GoldenRoot: filepath.Join(configDir, "avd-golden"), HasKVM: true, OS: "linux",
	}
}

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

func TestResolveServiceHostContextTreatsLoopbackAliasesAsLocal(t *testing.T) {
	oldLookup := lookupServiceHostIPs
	lookupServiceHostIPs = func(name string) ([]net.IP, error) {
		if name != "runner-host.example" {
			t.Fatalf("unexpected hostname lookup %q", name)
		}
		return []net.IP{net.ParseIP("127.0.1.1")}, nil
	}
	t.Cleanup(func() { lookupServiceHostIPs = oldLookup })
	cfg := config.Bootstrap()
	cfg.Credimi.URL = "http://runner-host.example:8090"
	cfg.Temporal.Address = "[::1]:7233"
	host, err := ResolveServiceHostContext(cfg, HostContext{OS: "linux", HostAddresses: []string{"127.0.0.1", "192.168.178.120"}})
	if err != nil {
		t.Fatal(err)
	}
	if host.ResolvedHostLocality["runner-host.example"] != "127.0.1.1" {
		t.Fatalf("resolved locality = %#v", host.ResolvedHostLocality)
	}
	if ServiceNetworkModeForConfig(cfg, host) != "host" {
		t.Fatalf("network mode = %q, want host", ServiceNetworkModeForConfig(cfg, host))
	}
	specHost := hostResolutionTestHost(t.TempDir())
	specHost.HostAddresses = host.HostAddresses
	specHost.ResolvedHostLocality = host.ResolvedHostLocality
	spec, err := BuildServiceSpec(cfg, specHost)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, extraHost := range spec.ExtraHosts {
		if extraHost == "runner-host.example:127.0.1.1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("extra hosts = %#v", spec.ExtraHosts)
	}
}

func TestResolveServiceHostContextEvaluatesLiteralIPLocality(t *testing.T) {
	for _, tc := range []struct {
		name, address, wantMode, wantValue string
	}{
		{"current host", "192.168.178.121", "host", "192.168.178.121"},
		{"remote", "203.0.113.50", "bridge", ""},
		{"loopback v4", "127.0.0.1", "host", "127.0.0.1"},
		{"loopback v6", "::1", "host", "::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Bootstrap()
			address := tc.address
			if strings.Contains(address, ":") {
				address = "[" + address + "]"
			}
			cfg.Credimi.URL = "http://" + address + ":8090"
			cfg.Temporal.Address = "203.0.113.11:7233"
			host, err := ResolveServiceHostContext(cfg, HostContext{OS: "linux", HostAddresses: []string{"192.168.178.121"}})
			if err != nil {
				t.Fatal(err)
			}
			if host.ResolvedHostLocality[tc.address] != tc.wantValue {
				t.Fatalf("resolved locality = %#v", host.ResolvedHostLocality)
			}
			if ServiceNetworkModeForConfig(cfg, host) != tc.wantMode {
				t.Fatalf("network mode = %q, want %q", ServiceNetworkModeForConfig(cfg, host), tc.wantMode)
			}
			specHost := hostResolutionTestHost(t.TempDir())
			specHost.HostAddresses = host.HostAddresses
			specHost.ResolvedHostLocality = host.ResolvedHostLocality
			spec, err := BuildServiceSpec(cfg, specHost)
			if err != nil {
				t.Fatal(err)
			}
			for _, extraHost := range spec.ExtraHosts {
				if strings.HasPrefix(extraHost, tc.address+":") {
					t.Fatalf("literal dependency received extra host mapping: %#v", spec.ExtraHosts)
				}
			}
		})
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
	spec, err := BuildServiceSpec(cfg, hostResolutionTestHost(t.TempDir()))
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
	host := hostResolutionTestHost(t.TempDir())
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
	literal := config.Bootstrap()
	literal.Credimi.URL = "http://192.168.178.121:8090"
	oldHost := HostContext{OS: "linux", HostAddresses: []string{"192.168.178.120"}, ResolvedHostLocality: map[string]string{"192.168.178.120": "192.168.178.120"}}
	if !ServiceHostLocalityUnknown(literal, oldHost) {
		t.Fatal("new literal host address was classified from stale metadata")
	}
	oldHost.ResolvedHostLocality["192.168.178.121"] = ""
	if ServiceHostLocalityUnknown(literal, oldHost) {
		t.Fatal("evaluated remote literal host address remained unknown")
	}
}
