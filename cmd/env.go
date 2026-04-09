package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

func loadDotEnv() (string, error) {
	const localEnvPath = ".env"

	if fileExists(localEnvPath) {
		if err := godotenv.Load(localEnvPath); err != nil {
			return "", fmt.Errorf("load %s: %w", localEnvPath, err)
		}
		return localEnvPath, nil
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", nil
	}

	configEnvPath := runtimeConfigEnvPath(configDir)
	if !fileExists(configEnvPath) {
		return "", nil
	}
	if err := godotenv.Load(configEnvPath); err != nil {
		return "", fmt.Errorf("load %s: %w", configEnvPath, err)
	}

	return configEnvPath, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func runtimeConfigEnvPath(configDir string) string {
	return filepath.Join(configDir, "credimi", "runner", ".env")
}

func validateRequiredRuntimeEnv() error {
	if strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_ID")) != "" {
		return nil
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("CREDIMI_RUNNER_ID is required")
	}

	return fmt.Errorf(
		"CREDIMI_RUNNER_ID is required; set it in .env, %s, or the process environment",
		runtimeConfigEnvPath(configDir),
	)
}
