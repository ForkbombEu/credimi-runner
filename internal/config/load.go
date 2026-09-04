package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

func Load(explicitPath string) (Config, string, error) {
	path, err := ResolvePath(explicitPath)
	if err != nil {
		return Config{}, "", err
	}
	config, err := LoadFile(path)
	return config, path, err
}

func LoadFile(path string) (Config, error) {
	contents, err := readConfigFile(path)
	if err != nil {
		return Config{}, err
	}
	return loadConfigBytes(contents)
}

// LoadFileSnapshot reads, hashes, and parses one immutable config.toml
// snapshot. Callers that validate a request digest must use the returned
// Config rather than reading the file again.
func LoadFileSnapshot(path string) (Config, string, error) {
	contents, err := readConfigFile(path)
	if err != nil {
		return Config{}, "", err
	}
	cfg, err := loadConfigBytes(contents)
	if err != nil {
		return Config{}, "", err
	}
	return cfg, configBytesDigest(contents), nil
}

func readConfigFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("config file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("config path must be a regular file")
	}
	if info.Size() == 0 {
		return nil, errors.New("config file is empty")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("config file must not be group- or world-readable")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	return contents, nil
}

func loadConfigBytes(contents []byte) (Config, error) {
	if len(contents) == 0 {
		return Config{}, errors.New("config file is empty")
	}
	decoder := toml.NewDecoder(bytes.NewReader(contents)).DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode TOML config: %w", err)
	}
	if err := ApplyDefaults(&cfg); err != nil {
		return Config{}, err
	}
	if err := ValidateForPlatform(cfg, runtimeGOOS()); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
