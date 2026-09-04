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
	require.Contains(t, dockerfile, "FROM ubuntu:24.04\nARG TARGETARCH\nARG CLOUDFLARED_VERSION=2026.8.2")
}

func TestDockerfileVerifiesPinnedCloudflaredBeforeInstallation(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	require.NoError(t, err)
	dockerfile := string(content)
	for _, want := range []string{
		"ARG CLOUDFLARED_SHA256_AMD64=fcfb02b575a52ca1af2e3267af4e1517bcdeb30ac48c834c69abaed3c0576ad2",
		"ARG CLOUDFLARED_SHA256_ARM64=7747d94570fb390cf47dcb4f9555c193c6355cda9793f0d878d9049e5d6a7790",
		"cloudflared_asset=\"cloudflared-linux-${TARGETARCH}\"",
		"sha256sum -c -",
		"unsupported architecture: $TARGETARCH",
	} {
		require.Contains(t, dockerfile, want)
	}
	download := strings.Index(dockerfile, "-o /tmp/cloudflared")
	verify := strings.Index(dockerfile, "sha256sum -c -")
	install := strings.Index(dockerfile, "install -m 0555 /tmp/cloudflared /usr/local/bin/cloudflared")
	require.GreaterOrEqual(t, download, 0)
	require.Greater(t, verify, download)
	require.Greater(t, install, verify)
}

func TestGoReleaserReleaseContractMatchesInstallerAssets(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", ".goreleaser.yaml"))
	require.NoError(t, err)
	goreleaser := string(content)
	require.Contains(t, goreleaser, `name_template: "{{ .ProjectName }}_v{{ .Version }}_checksums.txt"`)
	for _, fragment := range []string{
		"name_template:",
		"credimi-runner-",
		"eq .Os \"linux\"",
		"eq .Os \"darwin\"",
		"x86_64",
		"aarch64",
		"arm64",
	} {
		require.Contains(t, goreleaser, fragment)
	}
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
