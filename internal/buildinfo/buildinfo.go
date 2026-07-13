package buildinfo

import (
	"runtime/debug"
	"strings"
	"time"
)

// Version is injected at build time via -ldflags.
var Version = "dev"
var BuildTime string

func String() string {
	version := strings.TrimSpace(Version)
	if version == "" {
		return "dev"
	}
	return version
}

func BuiltAt() time.Time {
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(BuildTime)); err == nil {
		return parsed
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.time" {
				parsed, _ := time.Parse(time.RFC3339, setting.Value)
				return parsed
			}
		}
	}
	return time.Time{}
}
