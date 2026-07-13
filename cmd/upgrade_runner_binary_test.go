package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestUpgradeRunnerBinaryCommandIsRegistered(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"upgrade-runner-binary"})
	if err != nil || command != upgradeRunnerBinaryCmd {
		t.Fatalf("command = %v, %v", command, err)
	}
	if command.Short != "Replace the runner binary with the latest available release" {
		t.Fatalf("Short = %q", command.Short)
	}
}

func TestRunUpgradeRunnerBinaryStopsAndReplacesExecutable(t *testing.T) {
	originalExecutable, originalDownload, originalConfigDir := upgradeBinaryExecutable, upgradeBinaryDownload, dashboardConfigDir
	t.Cleanup(func() {
		upgradeBinaryExecutable, upgradeBinaryDownload, dashboardConfigDir = originalExecutable, originalDownload, originalConfigDir
	})
	dashboardConfigDir = t.TempDir()
	t.Setenv("GOOS_OVERRIDE", "darwin")
	runnerPort := availableTestPort(t)
	dashboardPort := availableTestPort(t)
	config := fmt.Sprintf("CREDIMI_RUNNER_BACKEND=host\nCREDIMI_RUNNER_TYPE=ios_simulator\nCREDIMI_SERVICE_MODE=manual\nRUNNER_PUBLIC_URL=http://example.test\nRUNNER_HOST=127.0.0.1\nRUNNER_PORT=%d\nDASHBOARD_HOST=127.0.0.1\nDASHBOARD_PORT=%d\n", runnerPort, dashboardPort)
	if err := os.WriteFile(filepath.Join(dashboardConfigDir, ".env"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "credimi-runner")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	upgradeBinaryExecutable = func() (string, error) { return target, nil }
	var downloaded string
	upgradeBinaryDownload = func(_ context.Context, _ *http.Client, path string, progress func(string)) error {
		downloaded = path
		progress("downloaded")
		return nil
	}
	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)
	if err := runUpgradeRunnerBinary(command, nil); err != nil {
		t.Fatal(err)
	}
	if downloaded != target || !strings.Contains(output.String(), "downloaded") {
		t.Fatalf("path=%q output=%q", downloaded, output.String())
	}
}

func availableTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestUpgradeBinaryPortHelpers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().String()
	if err := verifyAddressFree(address); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("error = %v", err)
	}
	if got := normalizeListenHost(""); got != "0.0.0.0" {
		t.Fatalf("host = %q", got)
	}
	if got := normalizeListenHost("[::1]"); got != "::1" {
		t.Fatalf("IPv6 host = %q", got)
	}
	if got := defaultString("", "fallback"); got != "fallback" {
		t.Fatalf("default = %q", got)
	}
	if got := defaultString(" value ", "fallback"); got != "value" {
		t.Fatalf("value = %q", got)
	}
	if got := portFromAddress(address); got == "" || got == "PORT" {
		t.Fatalf("port = %q", got)
	}
	if got := portFromAddress("invalid"); got != "PORT" {
		t.Fatalf("invalid port = %q", got)
	}
}
