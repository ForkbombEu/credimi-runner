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

	configEnvPath, err := runtimeConfigEnvPath()
	if err != nil {
		return "", nil
	}
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

func runtimeConfigEnvPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, ".env"), nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return runtimeConfigEnvPathFromConfigHome(configDir), nil
}

func runtimeConfigEnvPathFromConfigHome(configDir string) string {
	return filepath.Join(configDir, "credimi", "runner", ".env")
}

func validateRequiredRuntimeEnv() error {
	if strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_ID")) != "" {
		return nil
	}

	configPath, err := runtimeConfigEnvPath()
	if err != nil {
		return fmt.Errorf("CREDIMI_RUNNER_ID is required")
	}

	return fmt.Errorf(
		"CREDIMI_RUNNER_ID is required; set it in .env, %s, or the process environment",
		configPath,
	)
}
