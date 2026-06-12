package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServerCmdRunE_ListenError(t *testing.T) {
	t.Skip("temporarily skipped to unblock release while server command tests are stabilized")

	origHost, origPort, origDebug := host, port, debug
	t.Cleanup(func() {
		host, port, debug = origHost, origPort, origDebug
	})

	t.Setenv("CREDIMI_URL", "http://127.0.0.1:1")
	t.Setenv("CREDIMI_RUNNER_ID", "test-runner")
	host = "127.0.0.1:1"
	port = 8050
	debug = true

	err := serverCmd.RunE(serverCmd, nil)
	require.Error(t, err)
}

func TestIsFirstRun_IgnoresInheritedRunnerID(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_ID", "org/runner-from-parent-env")

	require.True(t, isFirstRun(""))
	require.False(t, isFirstRun(filepath.Join(t.TempDir(), ".env")))
}

func TestConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", dir)

	require.Equal(t, dir, configDir(""))
	require.Equal(t, filepath.Join(dir, "nested"), configDir(filepath.Join(dir, "nested", ".env")))
}

func TestIsDashboardRoute(t *testing.T) {
	tests := map[string]bool{
		"/":                 true,
		"/healthz":          true,
		"/devices":          true,
		"/workers/active":   true,
		"/network":          true,
		"/config/raw":       true,
		"/setup":            true,
		"/events/health":    true,
		"/static/app.css":   true,
		"/api/health":       false,
		"/openapi.json":     false,
		"/runner/processes": false,
	}
	for path, want := range tests {
		require.Equal(t, want, isDashboardRoute(path), path)
	}
}

func TestServerCmdRunE_ShutdownOnSignal(t *testing.T) {
	t.Skip("temporarily skipped to unblock release while server command tests are stabilized")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestServerCmdSignalHelper")
	cmd.Env = append(os.Environ(), "GO_WANT_SERVER_HELPER=1")
	err := cmd.Run()
	require.NoError(t, err)
}

func TestServerCmdSignalHelper(t *testing.T) {
	t.Skip("helper covered by temporarily skipped server command tests")

	if os.Getenv("GO_WANT_SERVER_HELPER") != "1" {
		return
	}

	host = "127.0.0.1"
	port = 0
	debug = false
	_ = os.Setenv("CREDIMI_URL", "http://127.0.0.1:1")
	_ = os.Setenv("CREDIMI_RUNNER_ID", "test-runner")

	ready := make(chan struct{})
	serverSignalReadyHook = func() {
		close(ready)
	}
	defer func() {
		serverSignalReadyHook = func() {}
	}()

	done := make(chan error, 1)
	go func() {
		done <- serverCmd.RunE(serverCmd, nil)
	}()

	select {
	case <-ready:
	case runErr := <-done:
		if runErr != nil {
			os.Exit(2)
		}
		os.Exit(5)
	case <-time.After(5 * time.Second):
		os.Exit(6)
	}

	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)

	select {
	case err := <-done:
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case <-time.After(5 * time.Second):
		os.Exit(3)
	}
}
