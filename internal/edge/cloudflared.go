package edge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	namedStartupGrace = 500 * time.Millisecond
	killWait          = 5 * time.Second
)

type cloudflaredProcess struct {
	cmd          *exec.Cmd
	done         chan struct{}
	exitErr      error
	exited       bool
	expectedStop bool
	started      bool
}

type Cloudflared struct {
	mu        sync.Mutex
	Binary    string
	Mode      string
	Token     string
	Domain    string
	process   *cloudflaredProcess
	publicURL string
	failures  chan error
	logf      func(string, ...any)
}

const Version = "2026.8.2"

func NewCloudflared(binary, mode, token, domain string) *Cloudflared {
	return &Cloudflared{
		Binary:   binary,
		Mode:     mode,
		Token:    token,
		Domain:   domain,
		failures: make(chan error, 1),
		logf:     log.Printf,
	}
}

var quickURL = regexp.MustCompile(`https://[A-Za-z0-9.-]+\.trycloudflare\.com`)

func normalizePublicURL(domain string) (string, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return "", errors.New("cloudflared domain is empty")
	}
	if !strings.Contains(domain, "://") {
		domain = "https://" + domain
	}
	u, err := url.Parse(domain)
	if err != nil {
		return "", fmt.Errorf("parse cloudflared domain: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid cloudflared domain %q", domain)
	}
	return u.String(), nil
}

func (e *Cloudflared) Start(ctx context.Context, origin string) (string, error) {
	e.mu.Lock()
	if e.process != nil && !e.process.exited {
		publicURL := e.publicURL
		e.mu.Unlock()
		return publicURL, nil
	}

	var args []string
	var named bool
	var namedURL string
	switch e.Mode {
	case "quick_tunnel":
		args = []string{"tunnel", "--no-autoupdate", "--url", origin}
	case "named_tunnel":
		args = []string{"tunnel", "--no-autoupdate", "run"}
		named = true
		var err error
		namedURL, err = normalizePublicURL(e.Domain)
		if err != nil {
			e.mu.Unlock()
			return "", err
		}
	default:
		mode := e.Mode
		e.mu.Unlock()
		return "", fmt.Errorf("unsupported cloudflared mode %q", mode)
	}
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		return "", err
	}
	cmd := exec.Command(e.Binary, args...)
	cmd.Env = os.Environ()
	if named {
		cmd.Env = append(cmd.Env, "TUNNEL_TOKEN="+e.Token)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		e.mu.Unlock()
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		e.mu.Unlock()
		return "", err
	}
	if err := cmd.Start(); err != nil {
		e.mu.Unlock()
		return "", redactError(err, e.Token)
	}
	p := &cloudflaredProcess{cmd: cmd, done: make(chan struct{})}
	e.process = p
	e.publicURL = ""
	e.mu.Unlock()

	urlCh := make(chan string, 1)
	go e.consumeOutput(stdout, "stdout", urlCh)
	go e.consumeOutput(stderr, "stderr", urlCh)
	go e.waitProcess(p)

	if named {
		timer := time.NewTimer(namedStartupGrace)
		defer timer.Stop()
		select {
		case <-p.done:
			return "", e.startExitError(p, "cloudflared named tunnel exited during startup")
		case <-ctx.Done():
			_ = e.Stop(context.Background())
			return "", ctx.Err()
		case <-timer.C:
		}
		e.mu.Lock()
		if p.exited {
			err := p.exitErr
			token := e.Token
			e.mu.Unlock()
			return "", formatExitError("cloudflared named tunnel exited during startup", err, token)
		}
		p.started = true
		e.publicURL = namedURL
		e.mu.Unlock()
		return namedURL, nil
	}

	select {
	case publicURL := <-urlCh:
		e.mu.Lock()
		if p.exited {
			err := p.exitErr
			e.mu.Unlock()
			return "", fmt.Errorf("cloudflared exited before publishing quick tunnel URL: %s", redactErrorText(err, e.Token))
		}
		p.started = true
		e.publicURL = publicURL
		e.mu.Unlock()
		return publicURL, nil
	case <-p.done:
		return "", e.startExitError(p, "cloudflared exited before publishing quick tunnel URL")
	case <-ctx.Done():
		_ = e.Stop(context.Background())
		return "", ctx.Err()
	}
}

func (e *Cloudflared) consumeOutput(reader io.ReadCloser, stream string, urls chan<- string) {
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := e.redact(scanner.Text())
		e.mu.Lock()
		logf := e.logf
		e.mu.Unlock()
		if logf == nil {
			logf = log.Printf
		}
		logf("cloudflared %s: %s", stream, line)
		if match := quickURL.FindString(line); match != "" {
			select {
			case urls <- match:
			default:
			}
		}
	}
	if err := scanner.Err(); err != nil {
		e.mu.Lock()
		logf := e.logf
		e.mu.Unlock()
		if logf == nil {
			logf = log.Printf
		}
		logf("cloudflared %s: %s", stream, e.redact(err.Error()))
	}
}

func (e *Cloudflared) waitProcess(p *cloudflaredProcess) {
	err := p.cmd.Wait()
	e.mu.Lock()
	p.exitErr = err
	p.exited = true
	current := e.process == p
	started := p.started
	expected := p.expectedStop
	if current {
		e.process = nil
		e.publicURL = ""
	}
	token := e.Token
	e.mu.Unlock()
	if started && !expected {
		if err == nil {
			e.reportFailure(errors.New("cloudflared exited unexpectedly"))
		} else {
			e.reportFailure(fmt.Errorf("cloudflared exited unexpectedly: %s", redactErrorText(err, token)))
		}
	}
	close(p.done)
}

func (e *Cloudflared) startExitError(p *cloudflaredProcess, prefix string) error {
	e.mu.Lock()
	err := p.exitErr
	token := e.Token
	e.mu.Unlock()
	return formatExitError(prefix, err, token)
}

func formatExitError(prefix string, err error, token string) error {
	if err == nil {
		return errors.New(prefix)
	}
	return fmt.Errorf("%s: %s", prefix, redactErrorText(err, token))
}

func (e *Cloudflared) reportFailure(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	if e.failures == nil {
		e.failures = make(chan error, 1)
	}
	failures := e.failures
	e.mu.Unlock()
	select {
	case failures <- err:
	default:
	}
}

func (e *Cloudflared) redact(value string) string {
	e.mu.Lock()
	token := e.Token
	e.mu.Unlock()
	if token == "" {
		return value
	}
	return strings.ReplaceAll(value, token, "[REDACTED]")
}

func redactErrorText(err error, token string) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if token != "" {
		value = strings.ReplaceAll(value, token, "[REDACTED]")
	}
	return value
}

func redactError(err error, token string) error {
	if err == nil {
		return nil
	}
	return errors.New(redactErrorText(err, token))
}

func (e *Cloudflared) Stop(ctx context.Context) error {
	e.mu.Lock()
	p := e.process
	if p == nil || p.exited {
		e.mu.Unlock()
		return nil
	}
	p.expectedStop = true
	cmd := p.cmd
	token := e.Token
	e.mu.Unlock()

	if cmd.Process != nil {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			if !errors.Is(err, os.ErrProcessDone) {
				select {
				case <-p.done:
					return nil
				default:
				}
				return fmt.Errorf("terminate cloudflared: %s", redactErrorText(err, token))
			}
		}
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
	}

	if cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil {
			if !errors.Is(err, os.ErrProcessDone) {
				select {
				case <-p.done:
					return nil
				default:
				}
				return fmt.Errorf("kill cloudflared: %s", redactErrorText(err, token))
			}
		}
	}
	timer := time.NewTimer(killWait)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
		return fmt.Errorf("cloudflared did not exit after termination: %w", ctx.Err())
	}
}

func (e *Cloudflared) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return e.Stop(ctx)
}

func (e *Cloudflared) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.process != nil && !e.process.exited
}

func (e *Cloudflared) Failures() <-chan error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failures == nil {
		e.failures = make(chan error, 1)
	}
	return e.failures
}
