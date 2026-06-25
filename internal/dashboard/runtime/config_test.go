package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStoreMissingFile(t *testing.T) {
	store, err := LoadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store.Exists() {
		t.Fatal("missing file should not exist")
	}
	if store.Values["RUNNER_PORT"] != DefaultRunnerPort {
		t.Fatalf("default RUNNER_PORT = %q", store.Values["RUNNER_PORT"])
	}
}

func TestStoreSaveCreates0600File(t *testing.T) {
	dir := t.TempDir()
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	values := store.Snapshot()
	values["CREDIMI_RUNNER_ID"] = "acme/runner"
	if err := store.Save(values); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestStorePreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "CREDIMI_RUNNER_ID=acme/runner\nUNKNOWN_KEY=value\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	values := store.Snapshot()
	values["RUNNER_PORT"] = "9000"
	if err := store.Save(values); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "UNKNOWN_KEY=value") {
		t.Fatalf("unknown key not preserved:\n%s", string(out))
	}
}

func TestStoreIgnoresInvalidKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("BAD-KEY=value\nGOOD_KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range store.UnknownLines {
		if strings.Contains(line, "BAD-KEY") {
			t.Fatalf("invalid key should be ignored: %q", line)
		}
	}
}

func TestSecretMaskingAndDiffClassification(t *testing.T) {
	impact := FieldImpacts["CREDIMI_USER_API_KEY"]
	if !impact.Secret {
		t.Fatal("expected CREDIMI_USER_API_KEY to be secret")
	}
	diff := DiffValues(Values{"RUNNER_IMAGE": "a"}, Values{"RUNNER_IMAGE": "b"})
	if len(diff.Classes) == 0 || diff.Classes[0] != ApplyComposeRecreate {
		t.Fatalf("diff classes = %#v", diff.Classes)
	}
}
