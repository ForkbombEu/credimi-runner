package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/forkbombeu/credimi-runner/internal/atomicfile"
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
	var owner *atomicfile.Ownership
	if uid, gid, ok := configuredOwner(); ok {
		owner = &atomicfile.Ownership{UID: uid, GID: gid}
	}
	return atomicfile.WriteAtomic(path, 0o600, owner, func(writer io.Writer) error {
		_, err := writer.Write(content)
		return err
	})
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
