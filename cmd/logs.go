package cmd

import (
	"github.com/forkbombeu/credimi-runner/internal/servicemanager"
	"github.com/spf13/cobra"
)

var logsFollow bool
var logsLines int
var logsCmd = &cobra.Command{Use: "logs", Short: "Show service logs", RunE: func(cmd *cobra.Command, _ []string) error {
	err := currentServiceManager().Logs(cmd.Context(), servicemanager.LogOptions{Follow: logsFollow, Lines: logsLines})
	if err == nil || cmd.Context().Err() != nil {
		return nil
	}
	return err
}}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow logs")
	logsCmd.Flags().IntVar(&logsLines, "lines", 200, "Number of lines")
	rootCmd.AddCommand(logsCmd)
}
