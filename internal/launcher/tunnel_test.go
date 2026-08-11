package launcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuickTunnelURLStateIsPrivateAndCleared(t *testing.T) {
	dir := t.TempDir()
	const want = "https://current.trycloudflare.com"
	if err := WriteQuickTunnelURL(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadQuickTunnelURL(dir)
	if err != nil || got != want {
		t.Fatalf("quick tunnel URL = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, quickTunnelURLFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("quick tunnel state mode = %o, want 600", info.Mode().Perm())
	}
	if err := ClearQuickTunnelURL(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadQuickTunnelURL(dir); err == nil {
		t.Fatal("cleared quick tunnel URL remained readable")
	}
}

func TestQuickTunnelURLStateRejectsNonHTTPSValues(t *testing.T) {
	if err := WriteQuickTunnelURL(t.TempDir(), "http://not-secure.example"); err == nil {
		t.Fatal("non-HTTPS quick tunnel URL was accepted")
	}
}

func TestQuickTunnelURLStateRejectsMalformedStateAndMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, quickTunnelURLFile), []byte("not a URL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadQuickTunnelURL(dir); err == nil {
		t.Fatal("malformed quick tunnel state was accepted")
	}
	if err := ClearQuickTunnelURL(dir); err != nil {
		t.Fatal(err)
	}
	if err := ClearQuickTunnelURL(dir); err != nil {
		t.Fatalf("clearing a missing quick tunnel state failed: %v", err)
	}
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteQuickTunnelURL(path, "https://current.trycloudflare.com"); err == nil {
		t.Fatal("quick tunnel state accepted a file as its directory")
	}
}

func TestQuickTunnelURLStateReportsPublishAndClearFailures(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, quickTunnelURLFile)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteQuickTunnelURL(dir, "https://current.trycloudflare.com"); err == nil {
		t.Fatal("quick tunnel state replaced a non-empty directory")
	}
	if err := ClearQuickTunnelURL(dir); err == nil {
		t.Fatal("quick tunnel state removed a non-empty directory")
	}
}
