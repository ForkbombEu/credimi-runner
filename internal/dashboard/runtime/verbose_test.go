package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenVerboseLogDisabledOrUnavailable(t *testing.T) {
	t.Setenv(verboseLogPathEnv, "")
	if got := openVerboseLog(); got != nil {
		t.Fatalf("openVerboseLog() = %#v, want nil", got)
	}

	t.Setenv(verboseLogPathEnv, filepath.Join(t.TempDir(), "missing", "verbose.log"))
	if got := openVerboseLog(); got != nil {
		t.Fatalf("openVerboseLog() = %#v, want nil", got)
	}
}

func TestVerboseLogWriteAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verbose.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(verboseLogPathEnv, path)
	log := openVerboseLog()
	if log == nil {
		t.Fatal("openVerboseLog() = nil")
	}
	if _, err := log.Write([]byte("runner output\n")); err != nil {
		t.Fatal(err)
	}
	log.Printf("event=%s", "started")
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if written, err := log.Write([]byte("ignored")); err != nil || written != len("ignored") {
		t.Fatalf("Write after Close = (%d, %v)", written, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" {
		t.Fatal("verbose log is empty")
	}
}
