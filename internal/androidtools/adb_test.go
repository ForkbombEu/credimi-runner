package androidtools

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveADBPrecedence(t *testing.T) {
	originalEnvironment, originalLookPath := adbEnvironment, adbLookPath
	originalHome, originalExecutable := adbUserHomeDir, adbExecutable
	t.Cleanup(func() {
		adbEnvironment, adbLookPath = originalEnvironment, originalLookPath
		adbUserHomeDir, adbExecutable = originalHome, originalExecutable
	})

	adbEnvironment = func(key string) string {
		switch key {
		case "ANDROID_SDK_ROOT":
			return "/sdk-root"
		case "ANDROID_HOME":
			return "/android-home"
		default:
			return ""
		}
	}
	adbUserHomeDir = func() (string, error) { return "/user", nil }
	adbLookPath = func(string) (string, error) { return "/on-path/adb", nil }
	adbExecutable = func(path string) bool {
		return path == filepath.Join("/sdk-root", "platform-tools", "adb")
	}

	got, err := resolveADB("darwin")
	if err != nil {
		t.Fatalf("resolveADB() error = %v", err)
	}
	want, _ := filepath.Abs(filepath.Join("/sdk-root", "platform-tools", "adb"))
	if got != want {
		t.Fatalf("resolveADB() = %q, want %q", got, want)
	}
}

func TestResolveADBStandardLocationsAndPathFallback(t *testing.T) {
	originalEnvironment, originalLookPath := adbEnvironment, adbLookPath
	originalHome, originalExecutable := adbUserHomeDir, adbExecutable
	t.Cleanup(func() {
		adbEnvironment, adbLookPath = originalEnvironment, originalLookPath
		adbUserHomeDir, adbExecutable = originalHome, originalExecutable
	})
	adbEnvironment = func(string) string { return "" }
	adbUserHomeDir = func() (string, error) { return "/user", nil }

	t.Run("macOS standard SDK", func(t *testing.T) {
		adbLookPath = func(string) (string, error) { return "", errors.New("not found") }
		adbExecutable = func(path string) bool {
			return path == filepath.Join("/user", "Library", "Android", "sdk", "platform-tools", "adb")
		}
		got, err := resolveADB("darwin")
		if err != nil {
			t.Fatalf("resolveADB() error = %v", err)
		}
		want, _ := filepath.Abs(filepath.Join("/user", "Library", "Android", "sdk", "platform-tools", "adb"))
		if got != want {
			t.Fatalf("resolveADB() = %q, want %q", got, want)
		}
	})

	t.Run("ANDROID_HOME", func(t *testing.T) {
		adbEnvironment = func(key string) string {
			if key == "ANDROID_HOME" {
				return "/android-home"
			}
			return ""
		}
		adbExecutable = func(path string) bool {
			return path == filepath.Join("/android-home", "platform-tools", "adb")
		}
		got, err := resolveADB("linux")
		if err != nil || got != "/android-home/platform-tools/adb" {
			t.Fatalf("resolveADB() = %q, %v", got, err)
		}
		adbEnvironment = func(string) string { return "" }
	})

	t.Run("PATH fallback", func(t *testing.T) {
		adbExecutable = func(string) bool { return false }
		adbLookPath = func(string) (string, error) { return "relative/adb", nil }
		got, err := resolveADB("linux")
		if err != nil {
			t.Fatalf("resolveADB() error = %v", err)
		}
		want, _ := filepath.Abs("relative/adb")
		if got != want {
			t.Fatalf("resolveADB() = %q, want %q", got, want)
		}
	})

	t.Run("not found", func(t *testing.T) {
		adbLookPath = func(string) (string, error) { return "", errors.New("not found") }
		if _, err := resolveADB("linux"); err == nil {
			t.Fatal("resolveADB() error = nil")
		}
	})
}
