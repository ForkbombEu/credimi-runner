package cmd

import (
	"context"
	"os"
	"os/exec"
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
	host = "127.0.0.1:1"
	port = 8050
	debug = true

	err := serverCmd.RunE(serverCmd, nil)
	require.Error(t, err)
}

func TestServerCmdRunE_ShutdownOnSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestServerCmdSignalHelper")
	cmd.Env = append(os.Environ(), "GO_WANT_SERVER_HELPER=1")
	err := cmd.Run()
	require.NoError(t, err)
}

func TestServerCmdSignalHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SERVER_HELPER") != "1" {
		return
	}

	host = "127.0.0.1"
	port = 0
	debug = false
	_ = os.Setenv("CREDIMI_URL", "http://127.0.0.1:1")

	done := make(chan error, 1)
	go func() {
		done <- serverCmd.RunE(serverCmd, nil)
	}()

	time.Sleep(300 * time.Millisecond)
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
