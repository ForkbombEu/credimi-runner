package edge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Cloudflared struct {
	mu        sync.Mutex
	Binary    string
	Mode      string
	Token     string
	Domain    string
	cmd       *exec.Cmd
	done      chan error
	publicURL string
}

const Version = "2026.8.2"

func NewCloudflared(binary, mode, token, domain string) *Cloudflared {
	return &Cloudflared{Binary: binary, Mode: mode, Token: token, Domain: domain}
}

var quickURL = regexp.MustCompile(`https://[A-Za-z0-9.-]+\.trycloudflare\.com`)

func (e *Cloudflared) Start(ctx context.Context, origin string) (string, error) {
	e.mu.Lock()
	if e.cmd != nil {
		url := e.publicURL
		e.mu.Unlock()
		return url, nil
	}
	args := []string{"tunnel", "--no-autoupdate"}
	if e.Mode == "named_tunnel" || e.Mode == "cloudflare-managed" || e.Domain != "" {
		args = append(args, "run")
	} else {
		args = append(args, "--url", origin)
	}
	// The startup context only bounds process creation and URL discovery. Once
	// cloudflared is running, its lifetime is owned by this Edge and is ended
	// explicitly by Stop; binding it to CommandContext would kill a healthy
	// tunnel when the supervisor's short startup context is canceled.
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		return "", err
	}
	cmd := exec.Command(e.Binary, args...)
	cmd.Env = os.Environ()
	if e.Mode == "named_tunnel" || e.Mode == "cloudflare-managed" || e.Domain != "" {
		cmd.Env = append(cmd.Env, "TUNNEL_TOKEN="+e.Token)
	}
	named := e.Mode == "named_tunnel" || e.Mode == "cloudflare-managed" || e.Domain != ""
	var stdout, stderr io.ReadCloser
	var err error
	if named {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	} else {
		stdout, err = cmd.StdoutPipe()
		if err != nil {
			e.mu.Unlock()
			return "", err
		}
		stderr, err = cmd.StderrPipe()
		if err != nil {
			e.mu.Unlock()
			return "", err
		}
	}
	if err := cmd.Start(); err != nil {
		e.mu.Unlock()
		return "", err
	}
	done := make(chan error, 1)
	e.cmd, e.done = cmd, done
	e.mu.Unlock()
	if named {
		go func() { done <- cmd.Wait() }()
		return "https://" + strings.TrimPrefix(strings.TrimSpace(e.Domain), "https://"), nil
	}
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
	}()
	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = e.Stop(cleanupCtx)
				cleanupCancel()
				return "", errors.New("cloudflared exited before publishing a quick tunnel URL")
			}
			if match := quickURL.FindString(line); match != "" {
				e.mu.Lock()
				e.publicURL = match
				e.mu.Unlock()
				return match, nil
			}
		case <-ctx.Done():
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = e.Stop(cleanupCtx)
			cleanupCancel()
			return "", ctx.Err()
		}
	}
}

func (e *Cloudflared) Stop(ctx context.Context) error {
	e.mu.Lock()
	cmd, done := e.cmd, e.done
	e.mu.Unlock()
	if cmd == nil {
		return nil
	}
	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case err := <-done:
		e.clearProcess(cmd)
		if err != nil {
			return normalizeExit(err)
		}
		return nil
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
			e.clearProcess(cmd)
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("cloudflared did not exit after termination: %w", ctx.Err())
		}
	}
}

func (e *Cloudflared) clearProcess(cmd *exec.Cmd) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd == cmd {
		e.cmd, e.done, e.publicURL = nil, nil, ""
	}
}
func normalizeExit(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ProcessState != nil {
		return nil
	}
	return err
}
func (e *Cloudflared) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return e.Stop(ctx)
}
func (e *Cloudflared) Running() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.cmd != nil }
