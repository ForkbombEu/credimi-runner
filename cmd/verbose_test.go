package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestEnableVerboseLogCreatesPrivateTimestampedLog(t *testing.T) {
	original := debugVerbose
	debugVerbose = true
	t.Cleanup(func() { debugVerbose = original })

	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	dir := t.TempDir()
	closeLog, err := enableVerboseLog(command, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "-verbose.log") {
		t.Fatalf("verbose log entries = %#v", entries)
	}
	info, err := os.Stat(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("verbose log permissions = %o, want 600", info.Mode().Perm())
	}
}
