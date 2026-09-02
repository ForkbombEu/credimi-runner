package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/controller"
)

func TestWaitForRunningControllerVerifiesFreshMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Credimi-Controller-Token") != "identity" {
			t.Fatal("identity token missing")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"controller_id": "c", "config_fingerprint": "f"})
	}))
	defer server.Close()
	dir := t.TempDir()
	metadata := controller.Metadata{Schema: 1, ControllerID: "c", ConfigDir: dir, ListenPort: 8051, ProbeURL: server.URL, PublicURL: server.URL, ConfigFingerprint: "f", IdentityToken: "identity"}
	raw, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := waitForRunningController(context.Background(), dir, "old")
	if err != nil || got.PublicURL != server.URL {
		t.Fatalf("metadata=%+v err=%v", got, err)
	}
}

func TestWaitForRunningControllerRejectsStaleIdentity(t *testing.T) {
	var currentToken atomic.Value
	currentToken.Store("old")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Credimi-Controller-Token") != currentToken.Load().(string) {
			http.Error(w, "stale", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"controller_id": "c", "config_fingerprint": "f"})
	}))
	defer server.Close()
	dir := t.TempDir()
	write := func(identity string) {
		raw, err := json.Marshal(controller.Metadata{Schema: 1, ControllerID: "c", ConfigDir: dir, ListenPort: 8051, ProbeURL: server.URL, PublicURL: server.URL, ConfigFingerprint: "f", IdentityToken: identity})
		if err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(dir, "controller.json.tmp")
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "controller.json")); err != nil {
			t.Fatal(err)
		}
	}
	write("old")
	result := make(chan controller.Metadata, 1)
	errCh := make(chan error, 1)
	observed := make(chan struct{})
	var observedOnce atomic.Bool
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		metadata, err := waitForRunningControllerUsing(ctx, dir, "old", func(configDir string) (controller.Metadata, error) {
			metadata, err := controller.ReadMetadata(configDir)
			if err == nil && metadata.IdentityToken == "old" && observedOnce.CompareAndSwap(false, true) {
				close(observed)
			}
			return metadata, err
		}, controller.Probe)
		if err != nil {
			errCh <- err
			return
		}
		result <- metadata
	}()
	select {
	case metadata := <-result:
		t.Fatalf("stale metadata returned: %+v", metadata)
	case err := <-errCh:
		t.Fatalf("stale metadata wait failed early: %v", err)
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("waiter did not observe old metadata")
	}
	select {
	case metadata := <-result:
		t.Fatalf("stale metadata returned: %+v", metadata)
	case err := <-errCh:
		t.Fatalf("stale metadata wait failed early: %v", err)
	default:
	}
	currentToken.Store("new")
	write("new")
	select {
	case metadata := <-result:
		if metadata.IdentityToken != "new" {
			t.Fatalf("identity=%q", metadata.IdentityToken)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("fresh metadata was not observed")
	}
}
