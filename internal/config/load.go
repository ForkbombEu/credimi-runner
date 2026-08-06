package config

import (
	"errors"
	"fmt"
	"io"
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
	info, err := os.Lstat(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Config{}, errors.New("config file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return Config{}, errors.New("config path must be a regular file")
	}
	if info.Size() == 0 {
		return Config{}, errors.New("config file is empty")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, errors.New("config file must not be group- or world-readable")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	decoder := toml.NewDecoder(file).DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, errors.New("config file is empty")
		}
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
