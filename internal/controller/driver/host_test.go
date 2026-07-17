package driver

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHostObserveUsesConfiguredListenerAndVerifiesIdentity(t *testing.T) {
	var gotAddress string
	driver := Host{
		Dial: func(_, address string, _ time.Duration) (net.Conn, error) {
			gotAddress = address
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		},
		PIDAtPort: func(port string) (int, error) {
			if port != "9123" {
				t.Fatalf("PIDAtPort port = %q", port)
			}
			return 42, nil
		},
		CommandOf: func(context.Context, int) (string, error) { return "credimi-runner serve --port 9123", nil },
	}

	result := driver.Observe(context.Background(), Request{HostBackend: true, RunnerHost: "192.0.2.3", RunnerPort: "9123"})
	if gotAddress != "192.0.2.3:9123" {
		t.Fatalf("dial address = %q", gotAddress)
	}
	if len(result.Services) != 1 || !result.Services[0].Running || !result.Services[0].Owned || result.Services[0].Detail != "pid 42" {
		t.Fatalf("services = %#v", result.Services)
	}
}

func TestHostPIDAtPortFindsCurrentListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	pid, err := hostPIDAtPort(port)
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Fatalf("pid = %d, want %d", pid, os.Getpid())
	}
	command, err := hostCommand(context.Background(), pid)
	if err != nil || strings.TrimSpace(command) == "" {
		t.Fatalf("command = %q, err = %v", command, err)
	}
}

func TestHostObserveTreatsUnverifiedListenerAsForeign(t *testing.T) {
	driver := Host{
		Dial: func(string, string, time.Duration) (net.Conn, error) {
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		},
		PIDAtPort: func(string) (int, error) { return 0, errors.New("not found") },
	}

	result := driver.Observe(context.Background(), Request{HostBackend: true})
	if len(result.Services) != 1 || result.Services[0].Owned || result.Services[0].Detail != "foreign listener" {
		t.Fatalf("services = %#v", result.Services)
	}
}

func TestHostObserveHandlesUnavailableAndForeignCommands(t *testing.T) {
	unavailable := Host{Dial: func(string, string, time.Duration) (net.Conn, error) { return nil, errors.New("offline") }}.Observe(context.Background(), Request{HostBackend: true})
	if len(unavailable.Services) != 1 || unavailable.Services[0].Running || unavailable.Services[0].Detail != "" {
		t.Fatalf("unavailable = %#v", unavailable.Services)
	}
	foreign := Host{
		Dial: func(string, string, time.Duration) (net.Conn, error) {
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		},
		PIDAtPort: func(string) (int, error) { return 42, nil },
		CommandOf: func(context.Context, int) (string, error) { return "python -m http.server", nil },
	}.Observe(context.Background(), Request{HostBackend: true})
	if len(foreign.Services) != 1 || foreign.Services[0].Owned || foreign.Services[0].Detail != "foreign listener" {
		t.Fatalf("foreign = %#v", foreign.Services)
	}
}
