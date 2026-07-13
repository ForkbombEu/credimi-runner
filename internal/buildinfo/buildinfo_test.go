package buildinfo

import (
	"testing"
	"time"
)

func TestString(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	Version = "  v1.2.3  "
	if got := String(); got != "v1.2.3" {
		t.Fatalf("String trimmed = %q", got)
	}
	Version = "   "
	if got := String(); got != "dev" {
		t.Fatalf("String blank = %q", got)
	}
}

func TestBuiltAtUsesEmbeddedTimestamp(t *testing.T) {
	original := BuildTime
	t.Cleanup(func() { BuildTime = original })
	BuildTime = "2026-07-13T16:00:00Z"
	if got := BuiltAt(); !got.Equal(time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("BuiltAt = %v", got)
	}
}

func TestBuiltAtFallsBackWhenEmbeddedTimestampIsInvalid(t *testing.T) {
	original := BuildTime
	t.Cleanup(func() { BuildTime = original })
	BuildTime = "not-a-time"
	_ = BuiltAt()
}
