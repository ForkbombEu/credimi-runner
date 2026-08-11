package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func WriteFile(path string, cfg Config) error {
	if err := ApplyDefaults(&cfg); err != nil {
		return err
	}
	if err := ValidateForPlatform(cfg, runtime.GOOS); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("config file must not be a symlink")
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect config path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	content, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode TOML config: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config.toml-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if uid, gid, ok := configuredOwner(); ok && os.Geteuid() == 0 {
		if err := temporary.Chown(uid, gid); err != nil {
			temporary.Close()
			return fmt.Errorf("set config owner: %w", err)
		}
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace config atomically: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		if runtime.GOOS != "windows" {
			_ = dir.Sync()
		}
		_ = dir.Close()
	}
	return nil
}

func configuredOwner() (uid, gid int, ok bool) {
	uid, errUID := strconv.Atoi(strings.TrimSpace(os.Getenv("CREDIMI_CONFIG_OWNER_UID")))
	gid, errGID := strconv.Atoi(strings.TrimSpace(os.Getenv("CREDIMI_CONFIG_OWNER_GID")))
	return uid, gid, errUID == nil && errGID == nil && uid > 0 && gid > 0
}

func (cfg Config) Redacted() Config {
	copy := cfg
	if copy.Credimi.UserAPIKey != "" {
		copy.Credimi.UserAPIKey = "[redacted]"
	}
	if copy.Credimi.InternalAdminKey != "" {
		copy.Credimi.InternalAdminKey = "[redacted]"
	}
	if copy.Server.DashboardToken != "" {
		copy.Server.DashboardToken = "[redacted]"
	}
	if copy.Exposure.CloudflareToken != "" {
		copy.Exposure.CloudflareToken = "[redacted]"
	}
	return copy
}
