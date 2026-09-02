package cmd

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
	"github.com/spf13/cobra"
)

func TestUpgradeBinaryDownloadFailureDoesNotRestart(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Linux host binary upgrade semantics")
	}
	oldDownload := downloadLatestBinary
	t.Cleanup(func() { downloadLatestBinary = oldDownload })
	called := false
	downloadLatestBinary = func(context.Context, *http.Client, string, func(string)) error {
		called = true
		return errors.New("download failed")
	}
	if err := runUpgradeBinary(&cobra.Command{}, nil); err == nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestUpgradeBinaryLinuxSuccessLeavesServiceAlone(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Linux host binary upgrade semantics")
	}
	old := downloadLatestBinary
	t.Cleanup(func() { downloadLatestBinary = old })
	downloadLatestBinary = func(context.Context, *http.Client, string, func(string)) error { return nil }
	cmd := &cobra.Command{Use: "upgrade-binary"}
	var out strings.Builder
	cmd.SetOut(&out)
	if err := runUpgradeBinary(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "persistent Docker service image was not changed") {
		t.Fatalf("output=%q", out.String())
	}
}

type imageUpgradeFake struct {
	statusManagerFake
	upgraded bool
}

func (m *imageUpgradeFake) Status(context.Context) (servicemanager.Status, error) {
	return servicemanager.Status{}, nil
}
func (m *imageUpgradeFake) UpgradeImage(context.Context, func(string)) error {
	m.upgraded = true
	return nil
}

func TestUpgradeImageStoppedServicePullsWithoutWaiting(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("image upgrade is unsupported on Darwin")
	}
	old := serviceManagerFactory
	t.Cleanup(func() { serviceManagerFactory = old })
	fake := &imageUpgradeFake{}
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return fake }
	if err := runUpgradeImage(&cobra.Command{}, nil); err != nil {
		t.Fatal(err)
	}
	if !fake.upgraded {
		t.Fatal("image upgrade was not called")
	}
}

func TestVersionCommandPrintsBuildVersion(t *testing.T) {
	cmd := &cobra.Command{Use: "version"}
	var out strings.Builder
	cmd.SetOut(&out)
	versionCmd.Run(cmd, nil)
	if strings.TrimSpace(out.String()) == "" {
		t.Fatalf("version output=%q", out.String())
	}
}

func TestServiceStatusOutput(t *testing.T) {
	old := serviceManagerFactory
	t.Cleanup(func() { serviceManagerFactory = old })
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return &statusManagerFake{} }
	cmd := &cobra.Command{Use: "status"}
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	if err := runServiceStatus(cmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Service: running", "Dashboard:", "Runtime desired: running", "Runtime actual: running", "Runtime error: none"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %s", want, out.String())
		}
	}
}

type statusManagerFake struct{}

func (*statusManagerFake) Start(context.Context) error                           { return nil }
func (*statusManagerFake) Stop(context.Context) error                            { return nil }
func (*statusManagerFake) Restart(context.Context) error                         { return nil }
func (*statusManagerFake) Logs(context.Context, servicemanager.LogOptions) error { return nil }
func (*statusManagerFake) Status(context.Context) (servicemanager.Status, error) {
	return servicemanager.Status{Running: true, DashboardURL: "http://127.0.0.1:9051", RuntimeDesired: "running", RuntimeActual: "running", RuntimeError: "none"}, nil
}

func TestUpgradeImageUnsupportedManager(t *testing.T) {
	old := serviceManagerFactory
	t.Cleanup(func() { serviceManagerFactory = old })
	serviceManagerFactory = func(string, servicemanager.BootstrapOptions) servicemanager.Manager { return &unsupportedManagerFake{} }
	if err := runUpgradeImage(&cobra.Command{}, nil); !strings.Contains(err.Error(), "only available") {
		t.Fatalf("error=%v", err)
	}
}

type unsupportedManagerFake struct{ statusManagerFake }

func (*unsupportedManagerFake) Status(context.Context) (servicemanager.Status, error) {
	return servicemanager.Status{}, nil
}
