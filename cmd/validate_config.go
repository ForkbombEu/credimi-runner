package cmd

import (
	"fmt"

	config "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/spf13/cobra"
)

func init() {
	var path string
	command := &cobra.Command{
		Use:   "validate-config",
		Short: "Validate the strict Credimi Runner TOML configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, resolved, err := config.Load(path)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Configuration is valid: "+resolved)
			return nil
		},
	}
	command.Flags().StringVar(&path, "config", "", "Path to config.toml")
	rootCmd.AddCommand(command)
}
