package cmd

import (
	"fmt"
	"io"
	stdlog "log"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

const verboseLogPathEnv = "CREDIMI_RUNNER_VERBOSE_LOG_PATH"

// enableVerboseLog mirrors this process's terminal output to a private,
// timestamped diagnostic file. Runtime managers read the same path and append
// Docker command output and container logs to it.
func enableVerboseLog(command *cobra.Command, configDir string) (func(), error) {
	if !debugVerbose {
		return func() {}, nil
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("create verbose log directory: %w", err)
	}
	path := filepath.Join(configDir, fmt.Sprintf("%d-verbose.log", time.Now().Unix()))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create verbose log: %w", err)
	}
	if err := os.Setenv(verboseLogPathEnv, path); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("set verbose log path: %w", err)
	}

	previousLogWriter := stdlog.Writer()
	previousStdout := command.OutOrStdout()
	previousStderr := command.ErrOrStderr()
	stdlog.SetOutput(io.MultiWriter(previousLogWriter, file))
	command.SetOut(io.MultiWriter(previousStdout, file))
	command.SetErr(io.MultiWriter(previousStderr, file))
	_, _ = fmt.Fprintf(file, "verbose diagnostics started at %s\n", time.Now().UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(os.Stderr, "Verbose diagnostics: %s\n", path)

	return func() {
		stdlog.SetOutput(previousLogWriter)
		command.SetOut(previousStdout)
		command.SetErr(previousStderr)
		_ = os.Unsetenv(verboseLogPathEnv)
		_ = file.Close()
	}, nil
}
