package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/spf13/cobra"
)

type fakeUpgradeManager struct {
	status     dashboardruntime.RuntimeStatus
	logs       []dashboardruntime.LogLine
	upgradeErr error
	upgraded   bool
}

func (f *fakeUpgradeManager) Start(context.Context) error                           { return nil }
func (f *fakeUpgradeManager) Stop(context.Context) error                            { return nil }
func (f *fakeUpgradeManager) Restart(context.Context) error                         { return nil }
func (f *fakeUpgradeManager) UpdateImage(context.Context) error                     { return nil }
func (f *fakeUpgradeManager) Configure(dashboardruntime.Values)                     {}
func (f *fakeUpgradeManager) SetPublicURL(value string)                             { f.status.PublicURL = value }
func (f *fakeUpgradeManager) Status(context.Context) dashboardruntime.RuntimeStatus { return f.status }
func (f *fakeUpgradeManager) Logs(context.Context, int) ([]dashboardruntime.LogLine, error) {
	return f.logs, nil
}
func (f *fakeUpgradeManager) UpgradeRunnerImage(_ context.Context, progress func(string)) error {
	f.upgraded = true
	progress("upgrade progress")
	return f.upgradeErr
}

func TestUpgradeRunnerImageCommandIsRegistered(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"upgrade-runner-image"})
	if err != nil {
		t.Fatal(err)
	}
	if command != upgradeRunnerImageCmd {
		t.Fatalf("command = %v", command)
	}
}

func TestPrintRunnerCLIHeader(t *testing.T) {
	var output bytes.Buffer
	printRunnerCLIHeader(&output)
	if !strings.Contains(output.String(), "____") {
		t.Fatalf("header = %q", output.String())
	}
}

func TestRunUpgradeRunnerImageUpdatesAutoTunnelRegistration(t *testing.T) {
	registered := make(chan string, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		registered <- string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()

	dir := t.TempDir()
	env := "CREDIMI_URL=" + api.URL + "\nCREDIMI_USER_API_KEY=key\nCREDIMI_RUNNER_ID=acme/runner\nCREDIMI_RUNNER_NAME=runner\nCREDIMI_RUNNER_ORGANIZATION=acme\nCREDIMI_RUNNER_TYPE=android_phone\nCREDIMI_RUNNER_BACKEND=container\nCREDIMI_SERVICE_MODE=auto\nRUNNER_IMAGE=example.test/runner:latest\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := &fakeUpgradeManager{
		status: dashboardruntime.RuntimeStatus{},
		logs:   []dashboardruntime.LogLine{{Message: "https://fresh.example.trycloudflare.com"}},
	}
	originalExecutable, originalFactory, originalReady, originalConfigDir := upgradeRunnerExecutable, newUpgradeRunnerManager, waitForUpgradeRunnerReady, dashboardConfigDir
	t.Cleanup(func() {
		upgradeRunnerExecutable, newUpgradeRunnerManager, waitForUpgradeRunnerReady, dashboardConfigDir = originalExecutable, originalFactory, originalReady, originalConfigDir
	})
	upgradeRunnerExecutable = func() (string, error) { return "/tmp/credimi-runner", nil }
	newUpgradeRunnerManager = func(string, string, dashboardruntime.Values) runnerImageUpgradeManager { return manager }
	waitForUpgradeRunnerReady = func(context.Context, dashboardruntime.Values) error { return nil }
	dashboardConfigDir = dir

	command := &cobra.Command{}
	command.SetContext(context.Background())
	var output bytes.Buffer
	command.SetOut(&output)
	if err := runUpgradeRunnerImage(command, nil); err != nil {
		t.Fatal(err)
	}
	if !manager.upgraded || !strings.Contains(output.String(), "Credimi registration updated") {
		t.Fatalf("upgraded=%v output=%q", manager.upgraded, output.String())
	}
	if body := <-registered; !strings.Contains(body, "https://fresh.example.trycloudflare.com") {
		t.Fatalf("registration body = %s", body)
	}
}

func TestRunUpgradeRunnerImageReportsExecutableAndUpgradeErrors(t *testing.T) {
	originalExecutable, originalFactory, originalReady, originalConfigDir := upgradeRunnerExecutable, newUpgradeRunnerManager, waitForUpgradeRunnerReady, dashboardConfigDir
	t.Cleanup(func() {
		upgradeRunnerExecutable, newUpgradeRunnerManager, waitForUpgradeRunnerReady, dashboardConfigDir = originalExecutable, originalFactory, originalReady, originalConfigDir
	})
	waitForUpgradeRunnerReady = func(context.Context, dashboardruntime.Values) error { return nil }
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("CREDIMI_RUNNER_BACKEND=container\nCREDIMI_SERVICE_MODE=manual\nRUNNER_IMAGE=image\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dashboardConfigDir = dir
	configFile := filepath.Join(t.TempDir(), "config-file")
	if err := os.WriteFile(configFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	dashboardConfigDir = configFile
	command := &cobra.Command{}
	command.SetContext(context.Background())
	if err := runUpgradeRunnerImage(command, nil); err == nil || !strings.Contains(err.Error(), "load runner configuration") {
		t.Fatalf("error = %v", err)
	}
	dashboardConfigDir = dir
	upgradeRunnerExecutable = func() (string, error) { return "", errors.New("executable failed") }
	if err := runUpgradeRunnerImage(command, nil); err == nil || !strings.Contains(err.Error(), "executable failed") {
		t.Fatalf("error = %v", err)
	}
	upgradeRunnerExecutable = func() (string, error) { return "/tmp/runner", nil }
	newUpgradeRunnerManager = func(string, string, dashboardruntime.Values) runnerImageUpgradeManager {
		return &fakeUpgradeManager{upgradeErr: errors.New("upgrade failed")}
	}
	if err := runUpgradeRunnerImage(command, nil); err == nil || !strings.Contains(err.Error(), "upgrade failed") {
		t.Fatalf("error = %v", err)
	}
	newUpgradeRunnerManager = func(string, string, dashboardruntime.Values) runnerImageUpgradeManager {
		return &fakeUpgradeManager{}
	}
	if err := runUpgradeRunnerImage(command, nil); err == nil || !strings.Contains(err.Error(), "missing Credimi API key") {
		t.Fatalf("error = %v", err)
	}
}
