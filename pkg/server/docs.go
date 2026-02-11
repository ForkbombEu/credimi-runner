package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
)

func projectRootDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func (s *runnerService) Docs(ctx context.Context) (string, error) {
	content, err := os.ReadFile(filepath.Join(projectRootDir(), "pkg", "server", "docs", "index.html"))
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *runnerService) DocsOpenapi(ctx context.Context) (string, error) {
	content, err := os.ReadFile(filepath.Join(projectRootDir(), "pkg", "gen", "http", "openapi.yaml"))
	if err != nil {
		return "", err
	}
	return string(content), nil
}
