package edge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestManualEdgeCloseAndExitNormalization(t *testing.T) {
	e := NewManual("https://runner.example")
	if _, err := e.Start(context.Background(), "origin"); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil || e.Running() {
		t.Fatalf("close err=%v running=%v", err, e.Running())
	}
	if err := normalizeExit(nil); err != nil {
		t.Fatal(err)
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

func TestCloudflaredStartHonorsCanceledContext(t *testing.T) {
	e := NewCloudflared("missing", "quick_tunnel", "", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Start(ctx, "origin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
func TestCloudflaredNamedTunnelUsesEnvironment(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "env")
	binary := fakeCloudflared(t, "printf '%s' \"$TUNNEL_TOKEN\" > "+marker+"\ntrap 'exit 0' TERM\nwhile true; do :; done\n")
	e := NewCloudflared(binary, "named_tunnel", "secret-token", "runner.example.com")
	url, err := e.Start(context.Background(), "http://127.0.0.1:8050")
	if err != nil || !strings.Contains(url, "runner.example.com") {
		t.Fatalf("url=%q err=%v", url, err)
	}
	var raw []byte
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		raw, _ = os.ReadFile(marker)
		if len(raw) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
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
