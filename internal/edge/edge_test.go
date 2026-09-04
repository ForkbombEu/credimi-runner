package edge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestManualEdgeLifecycle(t *testing.T) {
	e := NewManual("https://runner.example")
	if _, err := e.Start(context.Background(), "http://127.0.0.1:8050"); err != nil {
		t.Fatal(err)
	}
	if !e.Running() {
		t.Fatal("manual edge not running")
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.Running() {
		t.Fatal("manual edge still running")
	}
}

func TestManualEdgeStartIsIdempotentAndStopIsSafe(t *testing.T) {
	e := NewManual("https://runner.example")
	first, err := e.Start(context.Background(), "origin-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Start(context.Background(), "origin-b")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !e.Running() {
		t.Fatalf("first=%q second=%q running=%v", first, second, e.Running())
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestManualEdgeRequiresURL(t *testing.T) {
	if _, err := NewManual("").Start(context.Background(), ""); err == nil {
		t.Fatal("expected missing URL error")
	}
}

func TestManualEdgeClose(t *testing.T) {
	e := NewManual("https://runner.example")
	if _, err := e.Start(context.Background(), "origin"); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil || e.Running() {
		t.Fatalf("close err=%v running=%v", err, e.Running())
	}
}

func TestCloudflaredCloseWithoutProcessIsSafe(t *testing.T) {
	e := NewCloudflared("missing", "quick_tunnel", "", "")
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if e.Running() {
		t.Fatal("idle edge reports running")
	}
}

func fakeCloudflared(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestCloudflaredQuickTunnelLifecycle(t *testing.T) {
	binary := fakeCloudflared(t, "echo 'https://demo.trycloudflare.com'\ntrap 'exit 0' TERM\nwhile true; do :; done\n")
	e := NewCloudflared(binary, "quick_tunnel", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	url, err := e.Start(ctx, "http://127.0.0.1:8050")
	if err != nil || url != "https://demo.trycloudflare.com" {
		t.Fatalf("url=%q err=%v", url, err)
	}
	if !e.Running() {
		t.Fatal("edge not running")
	}
	stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := e.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if e.Running() {
		t.Fatal("edge still running")
	}
}

func TestCloudflaredQuickTunnelFailsWithoutPublishedURL(t *testing.T) {
	binary := fakeCloudflared(t, "echo 'starting'; exit 0\n")
	e := NewCloudflared(binary, "quick_tunnel", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := e.Start(ctx, "http://127.0.0.1:8050"); err == nil {
		t.Fatal("expected quick tunnel startup error")
	}
	if e.Running() {
		t.Fatal("failed quick tunnel retained process")
	}
}

func TestCloudflaredQuickTunnelReadsURLFromStderr(t *testing.T) {
	binary := fakeCloudflared(t, "echo 'https://stderr.trycloudflare.com' >&2\ntrap 'exit 0' TERM\nwhile true; do :; done\n")
	e := NewCloudflared(binary, "quick_tunnel", "", "")
	url, err := e.Start(context.Background(), "http://127.0.0.1:8050")
	if err != nil || url != "https://stderr.trycloudflare.com" {
		t.Fatalf("url=%q err=%v", url, err)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloudflaredCommandsAndTokenRedaction(t *testing.T) {
	t.Setenv("TUNNEL_TOKEN", "ambient-secret")
	quickArgs := filepath.Join(t.TempDir(), "quick-args")
	quickToken := filepath.Join(t.TempDir(), "quick-token")
	quickBinary := fakeCloudflared(t, "echo \"$*\" > "+quickArgs+"\nprintf '%s' \"${TUNNEL_TOKEN-}\" > "+quickToken+"\necho 'https://demo.trycloudflare.com'\ntrap 'exit 0' TERM\nwhile true; do :; done\n")
	quick := NewCloudflared(quickBinary, "quick_tunnel", "secret-token", "stale.example.com")
	if _, err := quick.Start(context.Background(), "http://127.0.0.1:8050"); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(quickArgs)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "tunnel --no-autoupdate --url http://127.0.0.1:8050\n" {
		t.Fatalf("quick args=%q", args)
	}
	token, err := os.ReadFile(quickToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 0 {
		t.Fatalf("quick inherited tunnel token=%q", token)
	}
	if err := quick.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	namedArgs := filepath.Join(t.TempDir(), "named-args")
	var logged string
	var logMu sync.Mutex
	namedBinary := fakeCloudflared(t, "echo \"$*\" > "+namedArgs+"\necho \"$TUNNEL_TOKEN\"\ntrap 'exit 0' TERM\nwhile true; do :; done\n")
	named := NewCloudflared(namedBinary, "named_tunnel", "secret-token", "stale.example.com")
	named.logf = func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logged += fmt.Sprintf(format, args...)
	}
	if _, err := named.Start(context.Background(), "http://127.0.0.1:8050"); err != nil {
		t.Fatal(err)
	}
	args, err = os.ReadFile(namedArgs)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "tunnel --no-autoupdate run\n" {
		t.Fatalf("named args=%q", args)
	}
	logMu.Lock()
	if strings.Contains(logged, "secret-token") || !strings.Contains(logged, "[REDACTED]") {
		logMu.Unlock()
		t.Fatalf("logged output=%q", logged)
	}
	logMu.Unlock()
	if err := named.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloudflaredQuickTunnelExitIncludesStatus(t *testing.T) {
	binary := fakeCloudflared(t, "echo 'starting' >&2\nexit 42\n")
	e := NewCloudflared(binary, "quick_tunnel", "", "")
	_, err := e.Start(context.Background(), "http://127.0.0.1:8050")
	if err == nil || !strings.Contains(err.Error(), "exit status 42") {
		t.Fatalf("error=%v", err)
	}
}

func TestCloudflaredQuickTunnelSpontaneousExit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	binary := fakeCloudflared(t, "echo $$ > "+pidFile+"\necho 'https://demo.trycloudflare.com'\nwhile true; do :; done\n")
	e := NewCloudflared(binary, "quick_tunnel", "", "")
	if _, err := e.Start(context.Background(), "http://127.0.0.1:8050"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(raw), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-e.Failures():
		if failure == nil || !strings.Contains(failure.Error(), "exited unexpectedly") {
			t.Fatalf("failure=%v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("spontaneous failure was not reported")
	}
	if e.Running() {
		t.Fatal("dead cloudflared still reports running")
	}
	if url, err := e.Start(context.Background(), "http://127.0.0.1:8050"); err != nil || url != "https://demo.trycloudflare.com" {
		t.Fatalf("replacement url=%q err=%v", url, err)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloudflaredStartHonorsCanceledContext(t *testing.T) {
	e := NewCloudflared("missing", "quick_tunnel", "", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Start(ctx, "origin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestCloudflaredStartupCancellationKillsTermResistantChild(t *testing.T) {
	for _, mode := range []string{"quick_tunnel", "named_tunnel"} {
		t.Run(mode, func(t *testing.T) {
			binary := fakeCloudflared(t, "echo started\ntrap '' TERM\nwhile true; do :; done\n")
			e := NewCloudflared(binary, mode, "secret-token", "runner.example.com")
			started := make(chan struct{})
			var once sync.Once
			e.logf = func(format string, args ...any) {
				if strings.Contains(fmt.Sprint(args...), "started") {
					once.Do(func() { close(started) })
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := e.Start(ctx, "http://127.0.0.1:8050")
				result <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("cloudflared child did not start")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("startup error=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("startup cancellation did not return")
			}
			if e.Running() {
				t.Fatal("TERM-resistant child still reports running")
			}
		})
	}
}

func TestCloudflaredRejectsUnsupportedModeAndInvalidDomain(t *testing.T) {
	if _, err := NewCloudflared("missing", "unsupported", "", "").Start(context.Background(), "origin"); err == nil {
		t.Fatal("expected unsupported mode error")
	}
	binary := fakeCloudflared(t, "trap 'exit 0' TERM\nwhile true; do :; done\n")
	e := NewCloudflared(binary, "named_tunnel", "secret-token", "ftp://runner.example.com")
	if _, err := e.Start(context.Background(), "origin"); err == nil || !strings.Contains(err.Error(), "invalid cloudflared domain") {
		t.Fatalf("error=%v", err)
	}
}
func TestCloudflaredNamedTunnelUsesEnvironment(t *testing.T) {
	t.Setenv("TUNNEL_TOKEN", "ambient-secret")
	marker := filepath.Join(t.TempDir(), "env")
	binary := fakeCloudflared(t, "printf '%s' \"$TUNNEL_TOKEN\" > "+marker+"\ntrap 'exit 0' TERM\nwhile true; do :; done\n")
	e := NewCloudflared(binary, "named_tunnel", "secret-token", "runner.example.com")
	url, err := e.Start(context.Background(), "http://127.0.0.1:8050")
	if err != nil || !strings.Contains(url, "runner.example.com") {
		t.Fatalf("url=%q err=%v", url, err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "secret-token" {
		t.Fatalf("token=%q", raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCloudflaredNamedTunnelRejectsImmediateExit(t *testing.T) {
	binary := fakeCloudflared(t, "exit 7\n")
	e := NewCloudflared(binary, "named_tunnel", "secret-token", "runner.example.com")
	_, err := e.Start(context.Background(), "http://127.0.0.1:8050")
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("error=%v", err)
	}
}

func TestCloudflaredNamedTunnelSpontaneousExit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	binary := fakeCloudflared(t, "echo $$ > "+pidFile+"\nwhile true; do :; done\n")
	e := NewCloudflared(binary, "named_tunnel", "secret-token", "runner.example.com")
	if _, err := e.Start(context.Background(), "http://127.0.0.1:8050"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(raw), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-e.Failures():
		if failure == nil || !strings.Contains(failure.Error(), "exited unexpectedly") {
			t.Fatalf("failure=%v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("named tunnel failure was not reported")
	}
	if e.Running() {
		t.Fatal("dead named tunnel still reports running")
	}
}

func TestCloudflaredForcedKillConfirmsCleanup(t *testing.T) {
	binary := fakeCloudflared(t, "echo 'https://demo.trycloudflare.com'\ntrap '' TERM\nwhile true; do :; done\n")
	e := NewCloudflared(binary, "quick_tunnel", "", "")
	if _, err := e.Start(context.Background(), "http://127.0.0.1:8050"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if e.Running() {
		t.Fatal("forced kill left cloudflared running")
	}
}

func TestNormalizePublicURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"runner.example.com", "https://runner.example.com"},
		{"https://runner.example.com", "https://runner.example.com"},
		{"http://runner.example.com", "http://runner.example.com"},
	}
	for _, tt := range tests {
		got, err := normalizePublicURL(tt.input)
		if err != nil || got != tt.want {
			t.Fatalf("normalizePublicURL(%q)=%q, %v; want %q", tt.input, got, err, tt.want)
		}
	}
	for _, domain := range []string{"", "ftp://runner.example.com", "https://"} {
		if _, err := normalizePublicURL(domain); err == nil {
			t.Fatalf("normalizePublicURL(%q) unexpectedly succeeded", domain)
		}
	}
}

func TestCloudflaredErrorRedaction(t *testing.T) {
	err := redactError(errors.New("token=secret-token"), "secret-token")
	if err == nil || err.Error() != "token=[REDACTED]" {
		t.Fatalf("redacted error=%v", err)
	}
	if redactError(nil, "secret-token") != nil {
		t.Fatal("nil error was changed")
	}
}
