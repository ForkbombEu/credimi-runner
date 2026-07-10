package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestUpgradeRunnerImageCommandIsRegistered(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"upgrade-runner-image"})
	if err != nil {
		t.Fatal(err)
	}
	if command != upgradeRunnerImageCmd {
		t.Fatalf("command = %v", command)
	}
}

func TestPrintRunnerCLIHeader(t *testing.T) {
	var output bytes.Buffer
	printRunnerCLIHeader(&output)
	if !strings.Contains(output.String(), "____") {
		t.Fatalf("header = %q", output.String())
	}
}
