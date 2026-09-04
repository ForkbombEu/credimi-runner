package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

func TestSetupDevicesKeepsRedroidAVDCTLValuesPerCard(t *testing.T) {
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	sequence := 0
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		sequence++
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_id":"acme/runner/redroid-` + string(rune('0'+sequence)) + `"}`))}, nil
	})
	s := newTestServer(t)
	values := s.cfg.Snapshot()
	form := url.Values{
		"SETUP_DEVICE_COUNT":  {"2"},
		"SETUP_DEVICE_1_NAME": {"Remote One"}, "SETUP_DEVICE_1_TYPE": {"redroid"}, "SETUP_DEVICE_1_MODE": {"no_device"}, "SETUP_DEVICE_1_WIFI_IP": {"192.0.2.41"}, "SETUP_DEVICE_1_WIFI_PORT": {"5556"},
		"SETUP_DEVICE_1_AVDCTL_SSH_TARGET": {"root@one"}, "SETUP_DEVICE_1_AVDCTL_SSH_PASSWORD": {"one-secret"}, "SETUP_DEVICE_1_AVDCTL_SSH_KNOWN_HOSTS_PATH": {"/known/one"}, "SETUP_DEVICE_1_AVDCTL_SUDO": {"false"}, "SETUP_DEVICE_1_AVDCTL_SUDO_PASSWORD": {"one-sudo"},
		"SETUP_DEVICE_2_NAME": {"Remote Two"}, "SETUP_DEVICE_2_TYPE": {"redroid"}, "SETUP_DEVICE_2_MODE": {"no_device"}, "SETUP_DEVICE_2_WIFI_IP": {"192.0.2.42"}, "SETUP_DEVICE_2_WIFI_PORT": {"5557"},
		"SETUP_DEVICE_2_AVDCTL_SSH_TARGET": {"root@two"}, "SETUP_DEVICE_2_AVDCTL_SSH_PASSWORD": {"two-secret"}, "SETUP_DEVICE_2_AVDCTL_SSH_KNOWN_HOSTS_PATH": {"/known/two"}, "SETUP_DEVICE_2_AVDCTL_SUDO": {"true"}, "SETUP_DEVICE_2_AVDCTL_SUDO_PASSWORD": {"two-sudo"},
	}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	devices, err := s.setupDevices(req.WithContext(context.Background()), values)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(devices))
	}
	if devices[0].Values["AVDCTL_SSH_TARGET"] != "root@one" || devices[1].Values["AVDCTL_SSH_TARGET"] != "root@two" {
		t.Fatalf("per-card SSH targets = %#v, %#v", devices[0].Values, devices[1].Values)
	}
	if devices[0].Values["AVDCTL_SUDO"] != "false" || devices[1].Values["AVDCTL_SUDO"] != "true" {
		t.Fatalf("per-card sudo values = %q, %q", devices[0].Values["AVDCTL_SUDO"], devices[1].Values["AVDCTL_SUDO"])
	}
}

func TestDashboardAddRedroidPersistsAVDCTLInTypedTOML(t *testing.T) {
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_id":"acme/runner/redroid"}`))}, nil
	})
	s := newTestServer(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("host ssh-ed25519 key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"name": {"Remote"}, "type": {"redroid"}, "mode": {"no_device"}, "CREDIMI_RUNNER_WIFI_IP": {"192.0.2.50"}, "CREDIMI_RUNNER_WIFI_PORT": {"5560"},
		"AVDCTL_SSH_TARGET": {"admin@redroid"}, "AVDCTL_SSH_PASSWORD": {"ssh-secret"}, "AVDCTL_SSH_KNOWN_HOSTS_PATH": {knownHosts}, "AVDCTL_SUDO": {"true"}, "AVDCTL_SUDO_PASSWORD": {"sudo-secret"},
	}
	req := httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := s.saveDevicesConfigSync(req, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := runnerconfig.LoadFile(filepath.Join(filepath.Dir(s.cfg.Path()), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	redroid := loaded.Devices[0].Redroid
	want := map[string]string{"target": "admin@redroid", "password": "ssh-secret", "known": knownHosts, "sudoPassword": "sudo-secret"}
	if redroid.AVDCTLSSHTarget != want["target"] || redroid.AVDCTLSSHPassword != want["password"] || redroid.AVDCTLSSHKnownHostsPath != want["known"] || !redroid.AVDCTLSudo || redroid.AVDCTLSudoPassword != want["sudoPassword"] {
		t.Fatalf("typed Redroid AVDCTL = %#v", redroid)
	}
}

func TestDashboardEditRedroidPreservesBlankSecrets(t *testing.T) {
	s := newTestServer(t)
	dir := filepath.Dir(s.cfg.Path())
	store, err := dashboardruntime.LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRuntimeConfig(dashboardruntime.RunnerRuntimeConfig{
		Host: dashboardruntime.Values(s.cfg.Snapshot()),
		Devices: []dashboardruntime.DeviceRuntimeConfig{{
			ID: "acme/runner/redroid", Name: "Remote", Type: "redroid", Mode: "redroid", Enabled: true,
			WiFiIP: "192.0.2.50", WiFiPort: "5555", Serial: "192.0.2.50:5555",
			Values: dashboardruntime.Values{
				"WIFI_IP": "192.0.2.50", "WIFI_PORT": "5555", "AVDCTL_SSH_TARGET": "admin@redroid",
				"AVDCTL_SSH_PASSWORD": "ssh-secret", "AVDCTL_SSH_KNOWN_HOSTS_PATH": "/home/admin/.ssh/known_hosts",
				"AVDCTL_SUDO": "true", "AVDCTL_SUDO_PASSWORD": "sudo-secret",
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	s.cfg = loadConfigSnapshot(store, s.cfg)
	form := url.Values{
		"CREDIMI_DEVICE_ID": {"acme/runner/redroid"}, "CREDIMI_DEVICE_NAME": {"Remote"},
		"CREDIMI_RUNNER_TYPE": {"redroid"}, "CREDIMI_RUNNER_DEVICE_MODE": {"redroid"},
		"CREDIMI_RUNNER_WIFI_IP": {"192.0.2.51"}, "CREDIMI_RUNNER_WIFI_PORT": {"5560"},
		"AVDCTL_SSH_TARGET": {"admin@redroid"}, "AVDCTL_SSH_KNOWN_HOSTS_PATH": {"/home/admin/.ssh/known_hosts"}, "AVDCTL_SUDO": {"true"},
	}
	req := httptest.NewRequest(http.MethodPost, "/devices/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := s.saveDevicesConfigSync(req, nil); err != nil {
		t.Fatal(err)
	}
	reloaded, err := runnerconfig.LoadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	redroid := reloaded.Devices[0].Redroid
	if redroid.AVDCTLSSHPassword != "ssh-secret" || redroid.AVDCTLSudoPassword != "sudo-secret" || redroid.Host != "192.0.2.51" || redroid.ADBPort != 5560 {
		t.Fatalf("edited Redroid = %#v", redroid)
	}
}
