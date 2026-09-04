package runtime

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
)

func TestRuntimePlanDescribesRuntimePlacement(t *testing.T) {
	for _, tc := range []struct {
		name       string
		goos       string
		mode       string
		wantDocker bool
	}{
		{name: "linux", goos: "linux", mode: "auto", wantDocker: true},
		{name: "manual", goos: "linux", mode: "manual", wantDocker: true},
		{name: "darwin", goos: "darwin", mode: "manual", wantDocker: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildRuntimePlanForOS(t.TempDir(), Values{"CREDIMI_SERVICE_MODE": tc.mode}, tc.goos)
			if plan.RequiresDocker != tc.wantDocker {
				t.Fatalf("plan = %#v", plan)
			}
		})
	}
}

func TestRuntimePlanClassifiesPersistentChanges(t *testing.T) {
	checks := []struct {
		name  string
		key   string
		value string
		class ApplyClass
	}{
		{"saved-only token", "DASHBOARD_TOKEN", "token", ApplySavedOnly},
		{"runtime", "TEMPORAL_ADDRESS", "temporal:7233", ApplyRuntimeReconcile},
		{"service", "ANDROID_RUNNER_IMAGE", "runner:test", ApplyServiceRestartRequired},
		{"Credimi", "RUNNER_PUBLIC_URL", "https://runner.example", ApplyCredimiUpdateRequired},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			oldValues := Values{}
			newValues := Values{check.key: check.value}
			diff := DiffValuesForOS(oldValues, newValues, "linux")
			if len(diff.Classes) != 1 || diff.Classes[0] != check.class {
				t.Fatalf("diff = %#v", diff)
			}
		})
	}
	deviceDiff := DiffValuesForOS(Values{}, Values{"CREDIMI_DEVICE_1_ID": "org/runner/device"}, "linux")
	for _, class := range []ApplyClass{ApplyRuntimeReconcile, ApplyCredimiUpdateRequired} {
		if !hasClass(deviceDiff, class) {
			t.Fatalf("device diff missing %q: %#v", class, deviceDiff)
		}
	}
	if hasClass(deviceDiff, ApplyServiceRestartRequired) {
		t.Fatal("device identity change unexpectedly requires service restart")
	}
}

func TestRuntimePlanRuntimeHelpers(t *testing.T) {
	values := Values{"ANDROID_RUNNER_IMAGE": "runner:test", "ANDROID_PULL_POLICY": "never", "RUNNER_PORT": "18050"}
	if !RunnerAPIReachableFromHost(values, "linux") || !RunnerReadinessRequiredBeforeRegistration(values, "linux") {
		t.Fatal("linux runner should be host reachable")
	}
	if !DeviceReadinessRequired(Values{"CREDIMI_RUNNER_ID": "org/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "org/runner/device", "CREDIMI_DEVICE_1_TYPE": "android_phone", "CREDIMI_DEVICE_1_MODE": "usb"}, "linux") {
		t.Fatal("physical Android device should require readiness")
	}
	if BuildRuntimePlan("/tmp/config", values).ConfigFingerprint == "" || len(DiffValues(Values{}, Values{"DASHBOARD_TOKEN": "token"}).Classes) != 1 {
		t.Fatal("wrapper helpers returned empty results")
	}
	if RunnerAPIReachableFromHost(Values{"CREDIMI_RUNNER_TYPE": "ios_simulator"}, "linux") {
		t.Fatal("unsupported device type reported as reachable")
	}
	if DeviceReadinessRequired(Values{"CREDIMI_DEVICE_COUNT": "bad"}, "linux") {
		t.Fatal("invalid inventory reported as ready")
	}
	if DeviceReadinessRequired(Values{"CREDIMI_RUNNER_ID": "org/runner", "CREDIMI_DEVICE_COUNT": "1", "CREDIMI_DEVICE_1_ID": "org/runner/device", "CREDIMI_DEVICE_1_TYPE": "android_phone", "CREDIMI_DEVICE_1_MODE": "no_device", "CREDIMI_DEVICE_1_ENABLED": "false"}, "linux") {
		t.Fatal("disabled device reported as requiring readiness")
	}
}

func TestRuntimePlanBootstrap(t *testing.T) {
	plan := BuildRuntimePlanForOS(t.TempDir(), Values{BootstrapPhaseEnv: "true"}, "linux")
	if plan.ServiceMode != "bootstrap" || plan.PublicMode != "bootstrap" {
		t.Fatalf("bootstrap plan = %+v", plan)
	}
	if !RunnerAPIReachableFromHost(Values{}, "darwin") || !RunnerReadinessRequiredBeforeRegistration(Values{}, "darwin") {
		t.Fatal("native runner should be reachable")
	}
}

func TestRuntimePlanDeviceDiffAndNoop(t *testing.T) {
	if diff := DiffValuesForOS(Values{"DASHBOARD_TOKEN": "same"}, Values{"DASHBOARD_TOKEN": "same"}, "linux"); len(diff.ChangedKeys) != 0 || len(diff.Classes) != 1 || diff.Classes[0] != ApplySavedOnly {
		t.Fatalf("no-op diff = %+v", diff)
	}
	updated := DiffValuesForOS(Values{"CREDIMI_DEVICE_1_ID": "old"}, Values{"CREDIMI_DEVICE_1_ID": "new"}, "linux")
	if len(updated.ChangedKeys) != 1 || !hasClass(updated, ApplyRuntimeReconcile) {
		t.Fatalf("updated device diff = %+v", updated)
	}
	added := DiffValuesForOS(Values{}, Values{"CREDIMI_DEVICE_2_NAME": "new"}, "linux")
	if len(added.ChangedKeys) != 1 || hasClass(added, ApplyServiceRestartRequired) {
		t.Fatalf("added device diff = %+v", added)
	}
}

func TestServiceCompatibilityRetainsRedroidKnownHostsSuperset(t *testing.T) {
	redroid := func(path string) runnerconfig.DeviceConfig {
		return runnerconfig.DeviceConfig{ID: "org/runner/" + path, Type: runnerconfig.DeviceRedroid, Enabled: true, Redroid: &runnerconfig.RedroidConfig{
			Host: "redroid", Image: "redroid:latest", DataDir: "/data", DataArchive: "/data.tar", ADBPort: 5555,
			AVDCTLSSHTarget: "root@redroid", AVDCTLSSHKnownHostsPath: path,
		}}
	}
	applied := runnerconfig.Bootstrap()
	if err := runnerconfig.ApplyDefaults(&applied); err != nil {
		t.Fatal(err)
	}
	applied.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
	applied.Devices = []runnerconfig.DeviceConfig{redroid("/known/a"), redroid("/known/b")}
	shrunk := applied
	shrunk.Devices = []runnerconfig.DeviceConfig{redroid("/known/a")}
	if !servicemanager.ServiceConfigsCompatible(applied, shrunk, true) {
		t.Fatal("removing an applied Redroid known-host mount requires an unnecessary restart")
	}
	expanded := applied
	expanded.Devices = append(expanded.Devices, redroid("/known/c"))
	if servicemanager.ServiceConfigsCompatible(applied, expanded, true) {
		t.Fatal("adding a Redroid known-host mount did not require a restart")
	}
	replaced := applied
	replaced.Devices = []runnerconfig.DeviceConfig{redroid("/known/c"), redroid("/known/b")}
	if servicemanager.ServiceConfigsCompatible(applied, replaced, true) {
		t.Fatal("replacing a Redroid known-host mount did not require a restart")
	}
}

func TestServiceRestartRequiredUsesAppliedRedroidKnownHosts(t *testing.T) {
	old := map[string]string{}
	for _, key := range []string{
		servicemanager.AppliedServiceConfigFingerprintEnv,
		servicemanager.AppliedServiceNeedsHostADBEnv,
		servicemanager.AppliedServiceNeedsUSBEnv,
		servicemanager.AppliedServiceNeedsEmulatorEnv,
		servicemanager.AppliedServiceRedroidKnownHostsEnv,
	} {
		old[key] = os.Getenv(key)
		_ = os.Unsetenv(key)
	}
	t.Cleanup(func() {
		for key, value := range old {
			if value == "" {
				_ = os.Unsetenv(key)
			} else {
				_ = os.Setenv(key, value)
			}
		}
	})
	redroid := func(path string) runnerconfig.DeviceConfig {
		return runnerconfig.DeviceConfig{ID: "org/runner/" + path, Type: runnerconfig.DeviceRedroid, Enabled: true, Redroid: &runnerconfig.RedroidConfig{Host: "redroid", ADBPort: 5555, AVDCTLSSHTarget: "root@redroid", AVDCTLSSHKnownHostsPath: path}}
	}
	applied := runnerconfig.Bootstrap()
	if err := runnerconfig.ApplyDefaults(&applied); err != nil {
		t.Fatal(err)
	}
	applied.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
	applied.Devices = []runnerconfig.DeviceConfig{redroid("/known/a"), redroid("/known/b")}
	shrunk := applied
	shrunk.Devices = []runnerconfig.DeviceConfig{redroid("/known/a")}
	_ = os.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(applied, true))
	for key, value := range map[string]string{
		servicemanager.AppliedServiceNeedsHostADBEnv:  "false",
		servicemanager.AppliedServiceNeedsUSBEnv:      "false",
		servicemanager.AppliedServiceNeedsEmulatorEnv: "false",
	} {
		_ = os.Setenv(key, value)
	}
	knownHosts, err := json.Marshal(servicemanager.ServiceRedroidKnownHostsForConfig(applied))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Setenv(servicemanager.AppliedServiceRedroidKnownHostsEnv, string(knownHosts))
	if ServiceRestartRequired(ValuesFromTypedConfig(shrunk), true) {
		t.Fatal("Redroid shrink was incorrectly marked as requiring service restart")
	}
}

func TestServiceConfigFingerprintProjectsOnlyServiceTopology(t *testing.T) {
	base := runnerconfig.Bootstrap()
	base.Server.APIListen = "127.0.0.1:8050"
	base.Server.DashboardListen = "127.0.0.1:8051"
	base.Exposure.Mode = "quick_tunnel"
	base.Devices = []runnerconfig.DeviceConfig{{
		ID: "org/runner/phone", Name: "Phone", Description: "old", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true,
		AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "A"},
	}}
	fingerprint := func(cfg runnerconfig.Config, configured bool) string {
		return servicemanager.ServiceConfigFingerprint(cfg, configured)
	}
	unchanged := []struct {
		name   string
		mutate func(*runnerconfig.Config)
	}{
		{"Temporal address", func(cfg *runnerconfig.Config) { cfg.Temporal.Address = "other:7233" }},
		{"runner description", func(cfg *runnerconfig.Config) { cfg.Runner.Description = "new" }},
		{"device description", func(cfg *runnerconfig.Config) { cfg.Devices[0].Description = "new" }},
		{"USB serial", func(cfg *runnerconfig.Config) { cfg.Devices[0].AndroidPhysical.Serial = "B" }},
		{"quick to named", func(cfg *runnerconfig.Config) { cfg.Exposure.Mode = "named_tunnel" }},
	}
	baseFingerprint := fingerprint(base, true)
	for _, tc := range unchanged {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			candidate.Devices = append([]runnerconfig.DeviceConfig(nil), base.Devices...)
			physical := *base.Devices[0].AndroidPhysical
			candidate.Devices[0].AndroidPhysical = &physical
			tc.mutate(&candidate)
			if got := fingerprint(candidate, true); got != baseFingerprint {
				t.Fatalf("fingerprint changed: %q != %q", got, baseFingerprint)
			}
		})
	}
	changed := []struct {
		name   string
		mutate func(*runnerconfig.Config)
	}{
		{"manual exposure", func(cfg *runnerconfig.Config) { cfg.Exposure.Mode = "manual" }},
		{"API port", func(cfg *runnerconfig.Config) { cfg.Server.APIListen = "127.0.0.1:8052" }},
		{"Dashboard port", func(cfg *runnerconfig.Config) { cfg.Server.DashboardListen = "127.0.0.1:8052" }},
		{"runner image", func(cfg *runnerconfig.Config) { cfg.Android.RunnerImage = "runner:other" }},
		{"network", func(cfg *runnerconfig.Config) { cfg.Android.Network = "other-network" }},
		{"Wi-Fi to USB", func(cfg *runnerconfig.Config) { cfg.Devices[0].AndroidPhysical.Transport = "wifi" }},
		{"emulator enabled", func(cfg *runnerconfig.Config) {
			cfg.Devices = append(cfg.Devices, runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidEmulator, Enabled: true})
		}},
	}
	for _, tc := range changed {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			candidate.Devices = append([]runnerconfig.DeviceConfig(nil), base.Devices...)
			physical := *base.Devices[0].AndroidPhysical
			candidate.Devices[0].AndroidPhysical = &physical
			tc.mutate(&candidate)
			if got := fingerprint(candidate, true); got == baseFingerprint {
				t.Fatal("topology change did not change fingerprint")
			}
		})
	}
	redroid := base
	redroid.Devices = []runnerconfig.DeviceConfig{{Type: runnerconfig.DeviceRedroid, Enabled: true, Redroid: &runnerconfig.RedroidConfig{AVDCTLSSHKnownHostsPath: "/home/alice/.ssh/known_hosts"}}}
	otherRedroid := redroid
	otherRedroid.Devices = []runnerconfig.DeviceConfig{{Type: runnerconfig.DeviceRedroid, Enabled: true, Redroid: &runnerconfig.RedroidConfig{AVDCTLSSHKnownHostsPath: "/home/alice/.ssh/other_known_hosts"}}}
	if fingerprint(redroid, true) == fingerprint(otherRedroid, true) {
		t.Fatal("Redroid known-hosts change did not change fingerprint")
	}
	if fingerprint(base, false) == baseFingerprint {
		t.Fatal("bootstrap/configured state did not change fingerprint")
	}
}

func TestServiceRestartRequiredTracksHostResolvedNetworkMode(t *testing.T) {
	base := runnerconfig.Bootstrap()
	base.Credimi.URL = "https://credimi.io"
	base.Temporal.Address = "temporal.credimi.io:7233"
	base.Exposure.Mode = "quick_tunnel"
	host := servicemanager.HostContext{OS: "linux", HostAddresses: []string{"192.168.178.120"}}
	local := base
	local.Credimi.URL = "http://192.168.178.120:8090"
	applied := servicemanager.ServiceConfigFingerprintForHost(base, true, host)
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, applied)
	t.Setenv(servicemanager.ServiceNetworkModeEnv, "bridge")
	addresses, _ := json.Marshal(host.HostAddresses)
	t.Setenv(servicemanager.AppliedServiceHostAddressesEnv, string(addresses))
	if !ServiceRestartRequired(ValuesFromTypedConfig(local), true) {
		t.Fatal("host-local dependency did not require service replacement")
	}
	if ServiceRestartRequired(ValuesFromTypedConfig(base), true) {
		t.Fatal("unchanged bridged dependency required service replacement")
	}
}

func TestFirstUSBExpansionRequiresServiceReplacement(t *testing.T) {
	host := servicemanager.HostContext{OS: "linux", HostAddresses: []string{"192.168.178.120"}}
	base := runnerconfig.Bootstrap()
	base.Credimi.URL = "https://credimi.io"
	base.Temporal.Address = "temporal.credimi.io:7233"
	base.Devices = []runnerconfig.DeviceConfig{{Type: runnerconfig.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{BaseName: "credimi"}}}
	desired := base
	desired.Devices = append(desired.Devices, runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "SERIAL123"}})
	if servicemanager.ServiceConfigsCompatibleWithHost(base, desired, true, host) {
		t.Fatal("adding the first USB device did not require service replacement")
	}
	if got := servicemanager.ServiceNetworkModeForConfig(base, host); got != "bridge" {
		t.Fatalf("emulator-only network mode = %q, want bridge", got)
	}
	if got := servicemanager.ServiceNetworkModeForConfig(desired, host); got != "host" {
		t.Fatalf("emulator-plus-USB network mode = %q, want host", got)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprintForHost(base, true, host))
	t.Setenv(servicemanager.ServiceNetworkModeEnv, "bridge")
	addresses, _ := json.Marshal(host.HostAddresses)
	t.Setenv(servicemanager.AppliedServiceHostAddressesEnv, string(addresses))
	diff := DiffValuesForOS(ValuesFromTypedConfig(base), ValuesFromTypedConfig(desired), "linux")
	if !hasClass(diff, ApplyServiceRestartRequired) {
		t.Fatalf("first USB configuration change was not classified as service replacement: %+v", diff)
	}
}

func TestAdditionalUSBUsesRetainedCapability(t *testing.T) {
	host := servicemanager.HostContext{OS: "linux"}
	applied := runnerconfig.Bootstrap()
	applied.Devices = []runnerconfig.DeviceConfig{{Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "A"}}}
	desired := applied
	desired.Devices = append(desired.Devices, runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "B"}})
	if !servicemanager.ServiceConfigsCompatibleWithHost(applied, desired, true, host) {
		t.Fatal("additional USB device unnecessarily required service replacement")
	}
}

func TestServiceConfigFingerprintUsesCapabilityUnion(t *testing.T) {
	base := runnerconfig.Bootstrap()
	base.Server.APIListen = "127.0.0.1:8050"
	base.Server.DashboardListen = "127.0.0.1:8051"
	base.Android.Network = "credimi-runner"
	emulator := runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidEmulator, Enabled: true}
	usb := runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "one"}}
	wifi := runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.0.2.10", WiFiPort: "5555"}}

	withDevices := func(devices ...runnerconfig.DeviceConfig) runnerconfig.Config {
		cfg := base
		cfg.Devices = append([]runnerconfig.DeviceConfig(nil), devices...)
		return cfg
	}
	fingerprint := func(cfg runnerconfig.Config) string {
		return servicemanager.ServiceConfigFingerprint(cfg, true)
	}

	if fingerprint(withDevices(emulator)) != fingerprint(withDevices(emulator, runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidEmulator, Enabled: true})) {
		t.Fatal("second enabled emulator changed an already-satisfied capability")
	}
	if fingerprint(withDevices(usb)) != fingerprint(withDevices(usb, runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "two"}})) {
		t.Fatal("second USB phone changed an already-satisfied capability")
	}
	if fingerprint(withDevices(wifi)) != fingerprint(withDevices(wifi, runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "wifi", WiFiIP: "192.0.2.11", WiFiPort: "5555"}})) {
		t.Fatal("second Wi-Fi phone changed an already-satisfied capability")
	}
	disabled := runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidEmulator, Enabled: false, AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{BaseName: "disabled"}}
	if fingerprint(withDevices()) != fingerprint(withDevices(disabled)) {
		t.Fatal("disabled emulator changed the service capability projection")
	}
	firstEnabledEmulator := withDevices(runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidEmulator, Enabled: true})
	if fingerprint(withDevices(disabled)) == fingerprint(firstEnabledEmulator) {
		t.Fatal("enabling the first emulator did not change the service capability projection")
	}
	if fingerprint(withDevices(emulator)) != fingerprint(withDevices(emulator, disabled)) {
		t.Fatal("disabled second emulator changed the service capability projection")
	}
	if fingerprint(withDevices()) != fingerprint(withDevices(runnerconfig.DeviceConfig{Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "no_device"}})) {
		t.Fatal("no_device phone unexpectedly added a service capability")
	}

	redroid := runnerconfig.DeviceConfig{Type: runnerconfig.DeviceRedroid, Enabled: true, Redroid: &runnerconfig.RedroidConfig{AVDCTLSSHTarget: "root@redroid-a"}}
	otherTarget := redroid
	otherTarget.Redroid = &runnerconfig.RedroidConfig{AVDCTLSSHTarget: "root@redroid-b"}
	if fingerprint(withDevices(redroid)) != fingerprint(withDevices(otherTarget)) {
		t.Fatal("Redroid SSH target changed the default known-hosts capability")
	}
	knownHosts := "/home/alice/.ssh/known_hosts"
	explicit := redroid
	explicit.Redroid = &runnerconfig.RedroidConfig{AVDCTLSSHTarget: "root@redroid", AVDCTLSSHKnownHostsPath: knownHosts}
	otherKnownHosts := explicit
	otherKnownHosts.Redroid = &runnerconfig.RedroidConfig{AVDCTLSSHTarget: "root@redroid", AVDCTLSSHKnownHostsPath: "/home/alice/.ssh/other_known_hosts"}
	if fingerprint(withDevices(explicit)) == fingerprint(withDevices(otherKnownHosts)) {
		t.Fatal("Redroid known-hosts path change was ignored")
	}
	duplicate := runnerconfig.DeviceConfig{Type: runnerconfig.DeviceRedroid, Enabled: true, Redroid: &runnerconfig.RedroidConfig{AVDCTLSSHTarget: "root@other", AVDCTLSSHKnownHostsPath: knownHosts}}
	if fingerprint(withDevices(explicit)) != fingerprint(withDevices(explicit, duplicate)) {
		t.Fatal("duplicate Redroid known-hosts capability was not deduplicated")
	}
}

func TestDeviceCapabilityShrinksDoNotRequireImmediateServiceRestart(t *testing.T) {
	base := runnerconfig.Bootstrap()
	base.Runner = runnerconfig.RunnerConfig{ID: "org/runner", Name: "runner", Organization: "org"}
	base.Credimi = runnerconfig.CredimiConfig{URL: "https://credimi.example", AuthMode: "user", UserAPIKey: "key"}
	base.Server.APIListen = "127.0.0.1:8050"
	base.Server.DashboardListen = "127.0.0.1:8051"
	base.Devices = []runnerconfig.DeviceConfig{
		{ID: "org/runner/phone", Name: "Phone", Type: runnerconfig.DeviceAndroidPhysical, Enabled: true, AndroidPhysical: &runnerconfig.AndroidPhysicalConfig{Transport: "usb", Serial: "one"}},
		{ID: "org/runner/emulator", Name: "Emulator", Type: runnerconfig.DeviceAndroidEmulator, Enabled: true, AndroidEmulator: &runnerconfig.AndroidEmulatorConfig{BaseName: "emu"}},
	}
	shrunk := base
	shrunk.Devices = shrunk.Devices[:1]
	if !servicemanager.ServiceConfigsCompatible(base, shrunk, true) {
		t.Fatal("applied capability superset was not compatible")
	}
	if diff := DiffValuesForOS(ValuesFromTypedConfig(base), ValuesFromTypedConfig(shrunk), "linux"); hasClass(diff, ApplyServiceRestartRequired) {
		t.Fatalf("capability shrink unexpectedly requires service restart: %+v", diff)
	}
	withoutEmulator := base
	withoutEmulator.Devices = withoutEmulator.Devices[:1]
	withFirstEmulator := withoutEmulator
	withFirstEmulator.Devices = append(withFirstEmulator.Devices, base.Devices[1])
	if diff := DiffValuesForOS(ValuesFromTypedConfig(withoutEmulator), ValuesFromTypedConfig(withFirstEmulator), "linux"); !hasClass(diff, ApplyServiceRestartRequired) {
		t.Fatalf("capability expansion did not require service restart: %+v", diff)
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(base, true))
	t.Setenv(servicemanager.AppliedServiceNeedsHostADBEnv, "true")
	t.Setenv(servicemanager.AppliedServiceNeedsUSBEnv, "true")
	t.Setenv(servicemanager.AppliedServiceNeedsEmulatorEnv, "true")
	if ServiceRestartRequired(ValuesFromTypedConfig(shrunk), true) {
		t.Fatal("applied service capability superset was reported stale")
	}
	t.Setenv(servicemanager.AppliedServiceNeedsUSBEnv, "invalid")
	if !ServiceRestartRequired(ValuesFromTypedConfig(shrunk), true) {
		t.Fatal("invalid applied capability metadata was accepted")
	}
	expanded := base
	expanded.Devices = append(expanded.Devices, runnerconfig.DeviceConfig{Type: runnerconfig.DeviceRedroid, Enabled: true, Redroid: &runnerconfig.RedroidConfig{Host: "redroid", AVDCTLSSHKnownHostsPath: "/tmp/known_hosts"}})
	if servicemanager.ServiceConfigsCompatible(base, expanded, true) {
		t.Fatal("new persistent Redroid capability was treated as compatible")
	}
}

func TestDiffValuesForOSClassifiesNativeRuntimeSettings(t *testing.T) {
	runnerDiff := DiffValuesForOS(Values{"RUNNER_HOST": "127.0.0.1", "RUNNER_PORT": "8050"}, Values{"RUNNER_HOST": "0.0.0.0", "RUNNER_PORT": "9050"}, "darwin")
	if !hasClass(runnerDiff, ApplyRuntimeReconcile) || hasClass(runnerDiff, ApplyServiceRestartRequired) {
		t.Fatalf("native runner listener change was misclassified: %+v", runnerDiff)
	}
	dashboardDiff := DiffValuesForOS(Values{"DASHBOARD_HOST": "127.0.0.1", "DASHBOARD_PORT": "8051"}, Values{"DASHBOARD_HOST": "0.0.0.0", "DASHBOARD_PORT": "9051"}, "darwin")
	if !hasClass(dashboardDiff, ApplyServiceRestartRequired) || hasClass(dashboardDiff, ApplyRuntimeReconcile) {
		t.Fatalf("native dashboard listener change was misclassified: %+v", dashboardDiff)
	}
	androidDiff := DiffValuesForOS(Values{"ANDROID_RUNNER_IMAGE": "a"}, Values{"ANDROID_RUNNER_IMAGE": "b"}, "darwin")
	if hasClass(androidDiff, ApplyServiceRestartRequired) {
		t.Fatal("Docker-only Android image change leaked into native service classification")
	}
}

func TestServiceRestartRequiredUsesAppliedFingerprint(t *testing.T) {
	cfg := runnerconfig.Bootstrap()
	values := ValuesFromTypedConfig(cfg)
	if ServiceRestartRequired(values, true) {
		t.Fatal("native execution unexpectedly requires service restart")
	}
	t.Setenv(servicemanager.AppliedServiceConfigFingerprintEnv, servicemanager.ServiceConfigFingerprint(cfg, true))
	if ServiceRestartRequired(values, true) {
		t.Fatal("matching applied service fingerprint reported stale")
	}
	changed := ValuesFromTypedConfig(cfg)
	changed["ANDROID_RUNNER_IMAGE"] = "runner:changed"
	if !ServiceRestartRequired(changed, true) {
		t.Fatal("changed service configuration was not reported stale")
	}
	if !ServiceRestartRequired(values, false) {
		t.Fatal("bootstrap/configured mismatch was not reported stale")
	}
}

func TestHTTPTimeoutsArePersistentServiceSettings(t *testing.T) {
	cfg := runnerconfig.Bootstrap()
	cfg.Server.ReadHeaderTimeout = runnerconfig.Duration(time.Minute)
	cfg.Server.ShutdownTimeout = runnerconfig.Duration(30 * time.Second)
	base := ValuesFromTypedConfig(cfg)
	if got, changed := servicemanager.ServiceConfigFingerprint(cfg, true), servicemanager.ServiceConfigFingerprint(cfg, true); got != changed {
		t.Fatal("identical service configs produced different fingerprints")
	}
	for _, tc := range []struct {
		name string
		edit func(Values)
	}{
		{"read header", func(values Values) { values["SERVER_READ_HEADER_TIMEOUT"] = "2m0s" }},
		{"shutdown", func(values Values) { values["SERVER_SHUTDOWN_TIMEOUT"] = "45s" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := cloneValues(base)
			tc.edit(changed)
			oldCfg, err := TypedConfigFromValues(base)
			if err != nil {
				t.Fatal(err)
			}
			newCfg, err := TypedConfigFromValues(changed)
			if err != nil {
				t.Fatal(err)
			}
			if servicemanager.ServiceConfigFingerprint(oldCfg, true) == servicemanager.ServiceConfigFingerprint(newCfg, true) {
				t.Fatal("timeout change did not change service fingerprint")
			}
			for _, goos := range []string{"linux", "darwin"} {
				diff := DiffValuesForOS(base, changed, goos)
				if !hasClass(diff, ApplyServiceRestartRequired) {
					t.Fatalf("%s diff = %+v", goos, diff)
				}
			}
		})
	}
}

func hasClass(diff ConfigDiff, want ApplyClass) bool {
	for _, class := range diff.Classes {
		if class == want {
			return true
		}
	}
	return false
}

func containsEnvironment(environment []string, want string) bool {
	for _, value := range environment {
		if value == want {
			return true
		}
	}
	return false
}
