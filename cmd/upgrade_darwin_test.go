//go:build darwin

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/controller"
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
	"github.com/spf13/cobra"
)

type darwinUpgradeManager struct {
	status  servicemanager.Status
	restart int
}

func (m *darwinUpgradeManager) Start(context.Context) error { return nil }
func (m *darwinUpgradeManager) Stop(context.Context) error  { return nil }
func (m *darwinUpgradeManager) Restart(context.Context) error {
	m.restart++
	return nil
}
func (m *darwinUpgradeManager) Enable(context.Context) error  { return nil }
func (m *darwinUpgradeManager) Disable(context.Context) error { return nil }
func (m *darwinUpgradeManager) Status(context.Context) (servicemanager.Status, error) {
	return m.status, nil
}
func (m *darwinUpgradeManager) Logs(context.Context, servicemanager.LogOptions) error { return nil }

func TestUpgradeBinaryDarwinStoppedServiceRemainsStopped(t *testing.T) {
	oldFactory, oldDownload := serviceManagerFactory, downloadLatestBinary
	t.Cleanup(func() { serviceManagerFactory, downloadLatestBinary = oldFactory, oldDownload })
	fake := &darwinUpgradeManager{}
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return fake }
	called := false
	downloadLatestBinary = func(context.Context, *http.Client, string, func(string)) error {
		called = true
		return nil
	}
	if err := runUpgradeBinary(&cobra.Command{}, nil); err != nil {
		t.Fatal(err)
	}
	if !called || fake.restart != 0 {
		t.Fatalf("download=%v restart=%d", called, fake.restart)
	}
}

func TestUpgradeBinaryDarwinVerificationFailureDoesNotRestart(t *testing.T) {
	oldFactory, oldDownload, oldDir := serviceManagerFactory, downloadLatestBinary, dashboardConfigDir
	t.Cleanup(func() {
		serviceManagerFactory, downloadLatestBinary, dashboardConfigDir = oldFactory, oldDownload, oldDir
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"controller_id": "controller", "config_fingerprint": "fingerprint"})
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	dashboardConfigDir = dir
	metadata := controller.Metadata{Schema: 1, ControllerID: "controller", ListenPort: 8051, ProbeURL: server.URL, ConfigFingerprint: "fingerprint", IdentityToken: "token"}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &darwinUpgradeManager{status: servicemanager.Status{Running: true}}
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return fake }
	downloadLatestBinary = func(context.Context, *http.Client, string, func(string)) error {
		return errors.New("checksum verification failed")
	}
	if err := runUpgradeBinary(&cobra.Command{}, nil); err == nil || !strings.Contains(err.Error(), "checksum verification failed") {
		t.Fatalf("error=%v", err)
	}
	if fake.restart != 0 {
		t.Fatalf("restart=%d after failed verification", fake.restart)
	}
}
