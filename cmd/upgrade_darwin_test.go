//go:build darwin

package cmd

import (
	"context"
	"net/http"
	"testing"

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
