package cmd

import (
	stdlog "log"
	"os"
	"path/filepath"

	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	"github.com/forkbombeu/credimi-runner/internal/dashboard"
	"github.com/spf13/cobra"
)

var debugVerbose bool
var configPath string

var rootCmd = &cobra.Command{
	Use:           "credimi-runner",
	Short:         "Credimi mobile runner",
	Version:       buildinfo.String(),
	SilenceErrors: true,
	SilenceUsage:  false,
	RunE:          runDashboard,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		stdlog.Fatal(err)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&debugVerbose, "debug-verbose", false, "Write detailed dashboard, runtime, and container diagnostics to a private log file")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "Path to config.toml")
	rootCmd.AddCommand(&cobra.Command{
		Use:    "internal-runtime",
		Short:  "Run the foreground runtime inside the managed container",
		Hidden: true,
		RunE:   runInternalRuntime,
	})
}

// runInternalRuntime keeps the container topology single-process from the
// user's perspective while exposing both listeners required by Credimi and
// the dashboard. The normal command remains the only public entrypoint.
func runInternalRuntime(cmd *cobra.Command, args []string) error {
	configDir := dashboardConfigDir
	if configPath != "" {
		configDir = filepath.Dir(configPath)
	}
	if configDir == "" {
		configDir = dashboard.ConfigDir()
	}
	if err := os.Setenv("CREDIMI_RUNNER_CONFIG_DIR", configDir); err != nil {
		return err
	}
	serverHost := host
	if serverHost == "127.0.0.1" || serverHost == "" {
		serverHost = "0.0.0.0"
	}
	previousHost := host
	host = serverHost
	defer func() { host = previousHost }()
	errCh := make(chan error, 2)
	go func() { errCh <- serverCmd.RunE(cmd, args) }()
	go func() { errCh <- runDashboard(cmd, args) }()
	return <-errCh
}
