package cmd

import (
	stdlog "log"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "credimi-runner",
	Short: "Credimi mobile runner",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		stdlog.Fatal(err)
	}
}
