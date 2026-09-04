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

func TestLoadFileSnapshotReturnsParsedConfigAndMatchingDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteFile(path, validConfig()); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, digest, err := LoadFileSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runner.Name != "Runner" || digest != configBytesDigest(contents) {
		t.Fatalf("snapshot = %#v, digest=%q", cfg.Runner, digest)
	}
}
