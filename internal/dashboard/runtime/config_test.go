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
	if store.Values["DASHBOARD_HOST"] != "0.0.0.0" {
		t.Fatalf("default DASHBOARD_HOST = %q", store.Values["DASHBOARD_HOST"])
	}
}

func TestDefaultConfigDirHonorsOverride(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", "/tmp/credimi-runner-config")
	if got := DefaultConfigDir(); got != "/tmp/credimi-runner-config" {
		t.Fatalf("DefaultConfigDir = %q", got)
	}
}

func TestDefaultConfigDirUsesUserConfigDirectory(t *testing.T) {
	t.Setenv("CREDIMI_RUNNER_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := DefaultConfigDir(); !strings.HasSuffix(got, filepath.Join("credimi", "runner")) {
		t.Fatalf("DefaultConfigDir = %q", got)
	}
}

func TestLoadStoreAndSaveReportFilesystemErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStore(file); err == nil {
		t.Fatal("LoadStore should reject a file as config directory")
	}
	store := &Store{Path: filepath.Join(file, ".env"), Values: DefaultValues()}
	if err := store.Save(store.Snapshot()); err == nil {
		t.Fatal("Save should report an invalid parent directory")
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

func TestStoreLoadsRunnerNameAsKnownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "CREDIMI_RUNNER_ID=filippo-s-organization/test-runner-dashboard\nCREDIMI_RUNNER_NAME=Test-Runner-Dashboard\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Values["CREDIMI_RUNNER_NAME"]; got != "Test-Runner-Dashboard" {
		t.Fatalf("CREDIMI_RUNNER_NAME = %q", got)
	}
	for _, line := range store.UnknownLines {
		if strings.Contains(line, "CREDIMI_RUNNER_NAME") {
			t.Fatalf("runner name should not be unknown: %q", line)
		}
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

func TestConfigHelpers(t *testing.T) {
	if got := quote(`hello world`); got != `"hello world"` {
		t.Fatalf("quote = %q", got)
	}
	if got := unquote(`"hello world"`); got != "hello world" {
		t.Fatalf("unquote = %q", got)
	}
}

func TestRuntimeConfigParsesIndexedDevicesAndWritesStableBlocks(t *testing.T) {
	dir := t.TempDir()
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	config := RunnerRuntimeConfig{
		Host: Values{"CREDIMI_RUNNER_ID": "acme/lab", "CREDIMI_URL": "https://credimi.example"},
		Devices: []DeviceRuntimeConfig{
			{ID: "acme/lab/pixel", Name: "Pixel USB", Type: "android_phone", Mode: "usb", Values: Values{"SERIAL": "usb-1"}},
			{ID: "acme/lab/sim", Name: "Simulator", Type: "ios_simulator", Mode: "no_device", Values: Values{"IOS_UDID": "udid-1"}},
		},
	}
	if err := store.SaveRuntimeConfig(config); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.RuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Devices) != 2 || got.Devices[0].ID != "acme/lab/pixel" || got.Devices[1].Values["IOS_UDID"] != "udid-1" {
		t.Fatalf("runtime config = %#v", got)
	}
	contents, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# --- Runner host (managed by Credimi Runner) ---", "CREDIMI_DEVICE_COUNT=2", "# --- Device 1: Pixel USB ---", "CREDIMI_DEVICE_2_IOS_UDID=udid-1"} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("missing %q in:\n%s", want, contents)
		}
	}
}

func TestRuntimeConfigRejectsInvalidInventory(t *testing.T) {
	cases := []struct{ name, env, want string }{
		{"missing count", "CREDIMI_RUNNER_ID=acme/lab\n", "CREDIMI_DEVICE_COUNT is required"},
		{"gap", "CREDIMI_RUNNER_ID=acme/lab\nCREDIMI_DEVICE_COUNT=2\nCREDIMI_DEVICE_1_ID=acme/lab/a\nCREDIMI_DEVICE_1_NAME=A\nCREDIMI_DEVICE_1_TYPE=android_phone\nCREDIMI_DEVICE_1_MODE=usb\n", "device index 2 is missing"},
		{"outside runner", "CREDIMI_RUNNER_ID=acme/lab\nCREDIMI_DEVICE_COUNT=1\nCREDIMI_DEVICE_1_ID=acme/other\nCREDIMI_DEVICE_1_NAME=A\nCREDIMI_DEVICE_1_TYPE=android_phone\nCREDIMI_DEVICE_1_MODE=usb\n", "must be a child"},
		{"unindexed id", "CREDIMI_RUNNER_ID=acme/lab\nCREDIMI_DEVICE_COUNT=1\nCREDIMI_DEVICE_ID=acme/lab/a\n", "without an index is invalid"},
		{"beyond count", "CREDIMI_RUNNER_ID=acme/lab\nCREDIMI_DEVICE_COUNT=1\nCREDIMI_DEVICE_1_ID=acme/lab/a\nCREDIMI_DEVICE_1_NAME=A\nCREDIMI_DEVICE_1_TYPE=android_phone\nCREDIMI_DEVICE_1_MODE=usb\nCREDIMI_DEVICE_2_ID=acme/lab/b\n", "beyond CREDIMI_DEVICE_COUNT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tc.env), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := LoadStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.RuntimeConfig()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMigrateLegacySingleTargetMakesBackupAndRequiresRegistration(t *testing.T) {
	dir := t.TempDir()
	legacy := "CREDIMI_RUNNER_ID=acme/lab\nCREDIMI_RUNNER_NAME=Pixel USB\nCREDIMI_RUNNER_TYPE=android_phone\nCREDIMI_RUNNER_DEVICE_MODE=usb\nCREDIMI_RUNNER_SERIAL=serial-1\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	device, migrated, err := store.MigrateLegacySingleTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || device.ID != "acme/lab/pixel-usb" || device.Serial != "serial-1" {
		t.Fatalf("migration = %#v, %t", device, migrated)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".env.before-multi-device-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, %v", backups, err)
	}
	if _, migrated, err := store.MigrateLegacySingleTarget(); err != nil || migrated {
		t.Fatalf("idempotent migration = %t, %v", migrated, err)
	}
}
