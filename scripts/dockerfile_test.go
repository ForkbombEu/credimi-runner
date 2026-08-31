package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDockerfileBuildsOnlyTheRunnerImage(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	require.NoError(t, err)
	dockerfile := string(content)
	require.Contains(t, dockerfile, "FROM ubuntu:24.04")
	require.Contains(t, dockerfile, "ENTRYPOINT [\"/usr/local/bin/credimi-runner\"]")
	require.NotContains(t, dockerfile, "AS agent")
	require.NotContains(t, dockerfile, "agent-config.json")
	require.Contains(t, dockerfile, "/usr/local/bin/aapt2")
	require.NotContains(t, dockerfile, `"platform-tools" "emulator" "build-tools;35.0.0"`)
	require.NotContains(t, dockerfile, "/opt/android-sdk-bootstrap/emulator")
	require.NotContains(t, dockerfile, "SHA256SUMS")
}

func TestDockerfileBootstrapsADBForFreshPersistentVolumes(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	require.NoError(t, err)
	dockerfile := string(content)

	require.Contains(t, dockerfile,
		`PATH=/opt/android-sdk/cmdline-tools/latest/bin:/opt/android-sdk/platform-tools:/opt/android-sdk/emulator:/opt/android-sdk-bootstrap/cmdline-tools/latest/bin:/opt/android-sdk-bootstrap/platform-tools:/root/.maestro/bin:$PATH`,
	)
	require.Contains(t, dockerfile,
		`sdkmanager --sdk_root="$ANDROID_SDK_BOOTSTRAP" "platform-tools" "build-tools;35.0.0"`,
	)
}

func TestDockerfileKeepsPersistentEmulatorAndAVDCTL(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(content)
	for _, want := range []string{
		`FROM ghcr.io/forkbombeu/avdctl:latest AS avdctl`,
		`COPY --from=avdctl /usr/local/bin/avdctl /usr/local/bin/avdctl`,
		`PATH=/opt/android-sdk/cmdline-tools/latest/bin:/opt/android-sdk/platform-tools:/opt/android-sdk/emulator:`,
	} {
		if !strings.Contains(dockerfile, want) {
			t.Fatalf("Dockerfile missing %q", want)
		}
	}
}

func TestDockerfileAuthenticatesPrivateGeneration(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	require.NoError(t, err)
	dockerfile := string(content)

	generateStart := strings.Index(dockerfile, "&& go generate ./...")
	require.GreaterOrEqual(t, generateStart, 0)
	secretStart := strings.LastIndex(dockerfile[:generateStart], "--mount=type=secret,id=credimi_extra_pat,required=true")
	require.GreaterOrEqual(t, secretStart, 0)
}
