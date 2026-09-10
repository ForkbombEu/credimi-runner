package server

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"
)

func TestDeviceArtifactRootScopesDeviceAndRun(t *testing.T) {
	path, err := deviceArtifactRoot("/managed/workflows", "org/runner/phone", "org/run-1")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("org/runner/phone"))
	if want := filepath.Join("/managed/workflows", fmt.Sprintf("device-%x", digest[:]), "org", "run-1"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	for _, value := range []struct{ device, run string }{{"../../other", "run"}, {"org/runner/phone", "../run"}, {"org/runner/phone", "run/../../other"}} {
		if _, err := deviceArtifactRoot("/managed/workflows", value.device, value.run); err == nil {
			t.Fatalf("unsafe path accepted: %#v", value)
		}
	}
}
