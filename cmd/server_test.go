package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServerCmdRunE_ListenError(t *testing.T) {
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

func TestServerCmdRunE_ShutdownOnSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	configDir := t.TempDir()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestServerCmdSignalHelper")
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CREDIMI_DEVICE_") && !strings.HasPrefix(entry, "CREDIMI_RUNNER_CONFIG_DIR=") {
			env = append(env, entry)
		}
	}
	cmd.Env = append(env, "GO_WANT_SERVER_HELPER=1", "CREDIMI_RUNNER_CONFIG_DIR="+configDir)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "signal helper output:\n%s", output)
}

func TestServerCmdSignalHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SERVER_HELPER") != "1" {
		return
	}

	host = "127.0.0.1"
	port = 0
	debug = false
	_ = os.Setenv("CREDIMI_URL", "http://127.0.0.1:1")
	_ = os.Setenv("CREDIMI_RUNNER_ID", "test-runner")
	_ = os.Setenv("CREDIMI_DEVICE_COUNT", "1")
	_ = os.Setenv("CREDIMI_DEVICE_1_ID", "test-runner/simulator")
	_ = os.Setenv("CREDIMI_DEVICE_1_TYPE", "ios_simulator")
	_ = os.Setenv("CREDIMI_DEVICE_1_MODE", "no_device")

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
			_, _ = fmt.Fprintln(os.Stderr, runErr)
			os.Exit(2)
		}
		os.Exit(5)
	case <-time.After(5 * time.Second):
		os.Exit(6)
	}

	// The listener is deliberately published before signal handling is installed
	// so clients can reach the runner while startup work continues.
	time.Sleep(100 * time.Millisecond)
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)

	select {
	case err := <-done:
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	case <-time.After(5 * time.Second):
		os.Exit(3)
	}
}
