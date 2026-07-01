package buildinfo

import "testing"

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
