package cmd

import (
	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{Use: "version", Short: "Print Credimi Runner version", Run: func(cmd *cobra.Command, _ []string) { cmd.Println(buildinfo.String()) }}

func init() { rootCmd.AddCommand(versionCmd) }
