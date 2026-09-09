package androidtools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	adbEnvironment = os.Getenv
	adbLookPath    = exec.LookPath
	adbUserHomeDir = os.UserHomeDir
	adbExecutable  = func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
	}
)

// ResolveADB finds the Android platform-tools executable without depending on
// an interactive shell PATH (notably when running under launchd).
func ResolveADB() (string, error) {
	return resolveADB(runtime.GOOS)
}

func resolveADB(goos string) (string, error) {
	candidates := make([]string, 0, 5)
	for _, key := range []string{"ANDROID_SDK_ROOT", "ANDROID_HOME"} {
		if root := strings.TrimSpace(adbEnvironment(key)); root != "" {
			candidates = append(candidates, filepath.Join(root, "platform-tools", "adb"))
		}
	}
	if home, err := adbUserHomeDir(); err == nil && home != "" {
		if goos == "darwin" {
			candidates = append(candidates, filepath.Join(home, "Library", "Android", "sdk", "platform-tools", "adb"))
		} else {
			candidates = append(candidates, filepath.Join(home, "Android", "Sdk", "platform-tools", "adb"))
		}
	}
	if goos != "darwin" {
		candidates = append(candidates, "/opt/android-sdk/platform-tools/adb")
	}
	for _, candidate := range candidates {
		if adbExecutable(candidate) {
			return filepath.Abs(candidate)
		}
	}
	if path, err := adbLookPath("adb"); err == nil {
		return filepath.Abs(path)
	}
	return "", fmt.Errorf("ADB executable not found (checked ANDROID_SDK_ROOT, ANDROID_HOME, standard Android SDK locations, and PATH): %w", errors.New("adb unavailable"))
}
