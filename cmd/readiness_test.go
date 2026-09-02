package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
