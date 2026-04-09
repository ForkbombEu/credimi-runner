package buildinfo

import "strings"

// Version is injected at build time via -ldflags.
var Version = "dev"

func String() string {
	version := strings.TrimSpace(Version)
	if version == "" {
		return "dev"
	}
	return version
}
