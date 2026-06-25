//go:build !windows

package runtime

import (
	"os"
	"path/filepath"
)

func osUserHomeDir() (string, error) {
	return os.UserHomeDir()
}

func filepathJoin(elem ...string) string {
	return filepath.Join(elem...)
}
