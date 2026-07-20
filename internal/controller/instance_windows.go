//go:build windows

package controller

import (
	"errors"
	"os"
)

var errLockBusy = errors.New("controller lock is busy")

// Windows support keeps the API compilable. A Windows-specific named mutex
// should replace this fallback before production Windows lifecycle support.
func lockFile(*os.File) error   { return nil }
func unlockFile(*os.File) error { return nil }
