package cmd

import (
	"fmt"

	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the embedded build version",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), buildinfo.String())
		return err
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
