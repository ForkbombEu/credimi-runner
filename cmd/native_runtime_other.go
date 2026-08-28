//go:build !darwin

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// The public runtime dispatcher never selects this on non-macOS hosts. It
// keeps cross-platform command compilation explicit while native ownership is
// implemented only where it can run.
func runNativeApplicationRuntime(*cobra.Command, []string) error {
	return fmt.Errorf("native runtime is available only on macOS")
}
