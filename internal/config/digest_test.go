package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigFileDigestHashesPersistedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	wantBytes := []byte("[runner]\nname = \"runner\"\n")
	if err := os.WriteFile(path, wantBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	wantSum := sha256.Sum256(wantBytes)
	want := hex.EncodeToString(wantSum[:])
	got, err := ConfigFileDigest(path)
	if err != nil || got != want {
		t.Fatalf("digest=%q err=%v want=%q", got, err, want)
	}
}
