package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControllerLeaseExcludesSecondOwnerAndPublishesMetadata(t *testing.T) {
	dir := t.TempDir()
	lease, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	metadata := Metadata{
		ControllerID: "controller-1", PID: os.Getpid(), ConfigDir: dir,
		ListenHost: "0.0.0.0", ListenPort: 8051,
		ProbeURL: "http://127.0.0.1:8051/internal/controller/identity", StartedAt: time.Now(),
		ConfigFingerprint: "test-fingerprint", IdentityToken: "test-token",
	}
	if err := lease.Publish(metadata); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.ControllerID != metadata.ControllerID || got.ListenPort != metadata.ListenPort {
		t.Fatalf("metadata = %#v", got)
	}
	second, err := Acquire(dir)
	if !errors.Is(err, ErrAlreadyRunning) || second != nil {
		t.Fatalf("second acquire = %#v, %v", second, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "controller.json")); !os.IsNotExist(err) {
		t.Fatalf("metadata should be removed on close: %v", err)
	}
	third, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
}

func TestControllerProbeRejectsUnavailableEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := Probe(ctx, Metadata{ProbeURL: "http://127.0.0.1:1/healthz"})
	if err == nil {
		t.Fatal("expected unavailable controller probe")
	}
}

func TestControllerProbeVerifiesBootIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Credimi-Controller-Token") != "token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"controller_id":"controller-1","config_fingerprint":"fingerprint"}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	metadata := Metadata{ControllerID: "controller-1", ConfigFingerprint: "fingerprint", IdentityToken: "token", ProbeURL: server.URL}
	if err := Probe(ctx, metadata); err != nil {
		t.Fatal(err)
	}
	metadata.ConfigFingerprint = "other"
	if err := Probe(ctx, metadata); err == nil {
		t.Fatal("expected controller identity mismatch")
	}
}

func TestControllerIdentityTokenAndMetadataValidation(t *testing.T) {
	token, err := NewIdentityToken()
	if err != nil || len(token) < 40 {
		t.Fatalf("identity token=%q err=%v", token, err)
	}
	dir := t.TempDir()
	if _, err := ReadMetadata(dir); !os.IsNotExist(err) {
		t.Fatalf("missing metadata error = %v", err)
	}
	raw, err := json.Marshal(Metadata{Schema: 1, ControllerID: "controller-1", IdentityToken: "token", ProbeURL: "http://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMetadata(dir); err == nil {
		t.Fatal("expected invalid metadata error")
	}
}
