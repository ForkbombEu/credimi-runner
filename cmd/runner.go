package cmd

import (
	stdlog "log"

	"github.com/forkbombeu/credimi-runner/pkg/telemetry"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "credimi-runner",
	Short: "Credimi mobile runner",
}

func Execute() {
	cleanup := telemetry.InitTracer()
	defer cleanup()
	cleanupMetrics := telemetry.InitMetrics()
	defer cleanupMetrics()

	if err := rootCmd.Execute(); err != nil {
		stdlog.Fatal(err)
	}
}
