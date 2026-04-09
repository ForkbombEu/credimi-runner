package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

	configEnvPath := filepath.Join(configDir, "credimi", "runner", ".env")
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
