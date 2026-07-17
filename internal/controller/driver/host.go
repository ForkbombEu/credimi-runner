package driver

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Host reports a listener only when its process command identifies it as a
// runner. A reachable unrelated listener is explicitly foreign, never adopted.
type Host struct {
	Dial      func(network, address string, timeout time.Duration) (net.Conn, error)
	CommandOf func(context.Context, int) (string, error)
	PIDAtPort func(string) (int, error)
}

func (d Host) Observe(ctx context.Context, request Request) Result {
	if !request.HostBackend {
		return Result{}
	}
	host := strings.TrimSpace(request.RunnerHost)
	if host == "" {
		host = "127.0.0.1"
	}
	port := strings.TrimSpace(request.RunnerPort)
	if port == "" {
		port = "8050"
	}
	address := net.JoinHostPort(host, port)
	dial := d.Dial
	if dial == nil {
		dial = net.DialTimeout
	}
	conn, err := dial("tcp", address, 500*time.Millisecond)
	if err != nil {
		return Result{Services: []Service{{ID: "runner_host_process", Name: "runner host", Role: "local runner process", Critical: true}}}
	}
	_ = conn.Close()
	if d.PIDAtPort == nil || d.CommandOf == nil {
		if d.PIDAtPort == nil {
			d.PIDAtPort = hostPIDAtPort
		}
		if d.CommandOf == nil {
			d.CommandOf = hostCommand
		}
	}
	pid, err := d.PIDAtPort(port)
	if err != nil {
		return Result{Services: []Service{{ID: "runner_host_process", Name: "runner host", Role: "local runner process", Detail: "foreign listener", Critical: true}}}
	}
	command, err := d.CommandOf(ctx, pid)
	owned := err == nil && strings.Contains(command, "credimi-runner") && strings.Contains(" "+command+" ", " serve ")
	detail := "foreign listener"
	if owned {
		detail = "pid " + strconv.Itoa(pid)
	}
	return Result{Services: []Service{{ID: "runner_host_process", Name: "runner host", Role: "local runner process", Detail: detail, Running: owned, Owned: owned, Critical: true}}}
}

func hostPIDAtPort(port string) (int, error) {
	want, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0, err
	}
	inodes := make(map[string]struct{})
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			parts := strings.Split(fields[1], ":")
			if len(parts) != 2 {
				continue
			}
			localPort, err := strconv.ParseUint(parts[1], 16, 16)
			if err == nil && localPort == want {
				inodes[fields[9]] = struct{}{}
			}
		}
		_ = file.Close()
	}
	if len(inodes) == 0 {
		return 0, os.ErrNotExist
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", entry.Name(), "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join("/proc", entry.Name(), "fd", fd.Name()))
			if err == nil && strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
				if _, ok := inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")]; ok {
					return pid, nil
				}
			}
		}
	}
	return 0, os.ErrNotExist
}

func hostCommand(_ context.Context, pid int) (string, error) {
	command, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.ReplaceAll(string(command), "\x00", " ")), nil
}
