package cmd

import (
	"context"
	"fmt"
	"net"
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

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(4)
	}
	port = listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	host = "127.0.0.1"
	debug = false
	_ = os.Setenv("CREDIMI_URL", "http://127.0.0.1:1")

	done := make(chan error, 1)
	go func() {
		done <- serverCmd.RunE(serverCmd, nil)
	}()

	addr := fmt.Sprintf("%s:%d", host, port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}

		select {
		case runErr := <-done:
			if runErr != nil {
				os.Exit(2)
			}
			os.Exit(5)
		default:
		}

		if time.Now().After(deadline) {
			os.Exit(6)
		}

		time.Sleep(50 * time.Millisecond)
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
