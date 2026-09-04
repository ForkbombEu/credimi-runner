package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReadMetadataAndProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Credimi-Controller-Token") != "token" {
			t.Fatal("identity token missing")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"controller_id": "controller", "config_fingerprint": "fingerprint"})
	}))
	defer server.Close()
	dir := t.TempDir()
	metadata := Metadata{Schema: 1, ControllerID: "controller", ListenPort: 8051, ProbeURL: server.URL, ConfigFingerprint: "fingerprint", IdentityToken: "token"}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Probe(context.Background(), got); err != nil {
		t.Fatal(err)
	}
}

func TestReadMetadataRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "controller.json"), []byte(`{"schema":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMetadata(dir); err == nil {
		t.Fatal("invalid metadata accepted")
	}
}
