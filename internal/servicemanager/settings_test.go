package servicemanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutostartSettingsDefaultAndPersistence(t *testing.T) {
	dir := t.TempDir()
	got, err := loadAutostart(dir)
	if err != nil || got {
		t.Fatalf("missing settings = %v, %v", got, err)
	}
	for _, want := range []bool{true, false} {
		if err := saveAutostart(dir, want); err != nil {
			t.Fatal(err)
		}
		got, err := loadAutostart(dir)
		if err != nil || got != want {
			t.Fatalf("saved %v = %v, %v", want, got, err)
		}
		info, err := os.Stat(filepath.Join(dir, serviceSettingsName))
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0o600 {
			t.Fatalf("settings mode = %o, want 600", gotMode)
		}
	}
}

func TestAutostartSettingsRejectCorruptState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, serviceSettingsName), []byte(`{"autostart":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAutostart(dir); err == nil || !strings.Contains(err.Error(), "decode service settings") {
		t.Fatalf("corrupt settings error = %v", err)
	}
}
