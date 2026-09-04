package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// ConfigFileDigest identifies the exact persisted configuration bytes that a
// host-side service operation will load.
func ConfigFileDigest(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read config file for digest: %w", err)
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}
