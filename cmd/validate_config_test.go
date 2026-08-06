package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateConfigCommandValidatesExplicitFile(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	command, _, err := rootCmd.Find([]string{"validate-config"})
	if err != nil || command == rootCmd {
		t.Fatalf("validate-config command = %v, err = %v", command, err)
	}
	var output bytes.Buffer
	originalOut := command.OutOrStdout()
	t.Cleanup(func() {
		command.SetOut(originalOut)
		_ = command.Flags().Set("config", "")
	})
	command.SetOut(&output)
	if err := command.Flags().Set("config", path); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Configuration is valid: "+path) {
		t.Fatalf("output = %q", output.String())
	}
}
