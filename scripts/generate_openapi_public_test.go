package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratePublicOpenAPI(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "openapi3.json")
	outputPath := filepath.Join(dir, "openapi3-public.json")

	spec := map[string]any{
		"openapi": "3.0.3",
		"paths": map[string]any{
			"/": map[string]any{
				"get": map[string]any{
					"tags": []any{"docs"},
				},
			},
			"/docs":               map[string]any{},
			"/docs/openapi3.yaml": map[string]any{},
			"/workers": map[string]any{
				"get": map[string]any{
					"tags": []any{"worker"},
				},
			},
			"/mixed": map[string]any{
				"get": map[string]any{
					"tags": []any{"docs"},
				},
				"post": map[string]any{
					"tags": []any{"worker"},
				},
			},
		},
		"tags": []any{
			map[string]any{"name": "docs"},
			map[string]any{"name": "worker"},
		},
	}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(inputPath, raw, 0o644))

	require.NoError(t, generatePublicOpenAPI(inputPath, outputPath))

	output, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	require.True(t, len(output) > 0)
	require.Equal(t, byte('\n'), output[len(output)-1])

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(output, &decoded))

	paths := decoded["paths"].(map[string]any)
	_, hasRoot := paths["/"]
	_, hasDocs := paths["/docs"]
	_, hasDocsSpec := paths["/docs/openapi3.yaml"]
	_, hasAPI := paths["/workers"]
	mixedPath, hasMixed := paths["/mixed"]
	mixedMethods := mixedPath.(map[string]any)
	_, mixedHasGet := mixedMethods["get"]
	_, mixedHasPost := mixedMethods["post"]
	require.False(t, hasRoot)
	require.False(t, hasDocs)
	require.False(t, hasDocsSpec)
	require.True(t, hasAPI)
	require.True(t, hasMixed)
	require.False(t, mixedHasGet)
	require.True(t, mixedHasPost)

	tags := decoded["tags"].([]any)
	require.Len(t, tags, 1)
	require.Equal(t, "worker", tags[0].(map[string]any)["name"])
}

func TestGeneratePublicOpenAPI_Errors(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "bad.json")
	outputPath := filepath.Join(dir, "out.json")

	require.NoError(t, os.WriteFile(inputPath, []byte("{"), 0o644))
	err := generatePublicOpenAPI(inputPath, outputPath)
	require.ErrorContains(t, err, "decode input JSON")

	specWithoutPaths := `{"openapi":"3.0.3"}`
	require.NoError(t, os.WriteFile(inputPath, []byte(specWithoutPaths), 0o644))
	err = generatePublicOpenAPI(inputPath, outputPath)
	require.ErrorContains(t, err, "paths not found")
}

func TestFilterPublicSpec(t *testing.T) {
	t.Run("invalid paths type", func(t *testing.T) {
		spec := map[string]any{
			"paths": []any{},
		}
		err := filterPublicSpec(spec, "input.json")
		require.ErrorContains(t, err, "expected object")
	})

	t.Run("keeps non-map tags while dropping docs", func(t *testing.T) {
		spec := map[string]any{
			"paths": map[string]any{
				"/docs": map[string]any{},
				"/": map[string]any{
					"get": map[string]any{
						"tags": []any{"docs"},
					},
				},
				"/ping": map[string]any{
					"get": map[string]any{
						"tags": []any{"worker"},
					},
				},
			},
			"tags": []any{
				"raw-tag",
				map[string]any{"name": "docs"},
				map[string]any{"name": "worker"},
			},
		}

		err := filterPublicSpec(spec, "input.json")
		require.NoError(t, err)

		paths := spec["paths"].(map[string]any)
		require.NotContains(t, paths, "/docs")
		require.NotContains(t, paths, "/")
		require.Contains(t, paths, "/ping")

		tags := spec["tags"].([]any)
		require.Len(t, tags, 2)
		require.Equal(t, "raw-tag", tags[0])
		require.Equal(t, "worker", tags[1].(map[string]any)["name"])
	})
}

func TestPublicServerURLFromEnv(t *testing.T) {
	t.Run("defaults to root when empty", func(t *testing.T) {
		t.Setenv("RUNNER_DOMAIN", "")
		require.Equal(t, "/", publicServerURLFromEnv())
	})

	t.Run("defaults to root on :80", func(t *testing.T) {
		t.Setenv("RUNNER_DOMAIN", ":80")
		require.Equal(t, "/", publicServerURLFromEnv())
	})

	t.Run("keeps explicit scheme", func(t *testing.T) {
		t.Setenv("RUNNER_DOMAIN", "http://example.com")
		require.Equal(t, "http://example.com", publicServerURLFromEnv())
	})

	t.Run("adds https scheme", func(t *testing.T) {
		t.Setenv("RUNNER_DOMAIN", "api.example.com")
		require.Equal(t, "https://api.example.com", publicServerURLFromEnv())
	})
}

func TestOperationAndMethodHelpers(t *testing.T) {
	t.Run("operationHasTag handles missing and invalid tags", func(t *testing.T) {
		require.False(t, operationHasTag(map[string]any{}, "docs"))
		require.False(t, operationHasTag(map[string]any{"tags": "docs"}, "docs"))
		require.False(t, operationHasTag(map[string]any{"tags": []any{123, "worker"}}, "docs"))
		require.True(t, operationHasTag(map[string]any{"tags": []any{"worker", "docs"}}, "docs"))
	})

	t.Run("hasHTTPMethods and isHTTPMethod", func(t *testing.T) {
		require.False(t, hasHTTPMethods(map[string]any{"summary": "x"}))
		require.True(t, hasHTTPMethods(map[string]any{"GET": map[string]any{}}))
		require.True(t, isHTTPMethod("PoSt"))
		require.False(t, isHTTPMethod("connect"))
	})
}

func TestMain(t *testing.T) {
	base := t.TempDir()
	scriptsDir := filepath.Join(base, "scripts")
	inputDir := filepath.Join(base, "gen", "http")
	require.NoError(t, os.MkdirAll(scriptsDir, 0o755))
	require.NoError(t, os.MkdirAll(inputDir, 0o755))

	input := `{"openapi":"3.0.3","paths":{"/":{"get":{"tags":["docs"]}},"/docs":{},"/ping":{"get":{"tags":["worker"]}}},"tags":[{"name":"docs"},{"name":"worker"}]}`
	require.NoError(t, os.WriteFile(filepath.Join(inputDir, "openapi3.json"), []byte(input), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	require.NoError(t, os.Chdir(scriptsDir))

	main()

	out, err := os.ReadFile(filepath.Join(inputDir, "openapi3-public.json"))
	require.NoError(t, err)
	require.Contains(t, string(out), "/ping")
	require.NotContains(t, string(out), "/docs")
	require.NotContains(t, string(out), "\"/\":")
}

func TestFailExitsProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFailHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")

	err := cmd.Run()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.ExitCode())
}

func TestFailHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	fail("forced", errors.New("boom"))
}
