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
	content, err := os.ReadFile(filepath.Join(projectRootDir(), "pkg", "server", "docs", "spotlight.html"))
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *runnerService) DocsOpenapi3(ctx context.Context) (string, error) {
	content, err := os.ReadFile(filepath.Join(projectRootDir(), "pkg", "gen", "http", "openapi3.yaml"))
	if err != nil {
		return "", err
	}
	return string(content), nil
}
