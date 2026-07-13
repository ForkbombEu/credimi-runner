package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	restartWaitPID int
	restartTarget  string
	restartStaged  string
	restartArgs    []string
	restartRename  = os.Rename
	restartStart   = func(target string, args []string) error {
		command := exec.Command(target, args...)
		command.Stdout, command.Stderr, command.Stdin = os.Stdout, os.Stderr, os.Stdin
		return command.Start()
	}
)

var restartDashboardHelperCmd = &cobra.Command{
	Use:    "restart-dashboard-helper",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runRestartDashboardHelper,
}

func runRestartDashboardHelper(_ *cobra.Command, _ []string) error {
	if restartWaitPID <= 0 || restartTarget == "" || restartStaged == "" {
		return errors.New("restart helper requires a parent PID, target binary, and staged binary")
	}
	deadline := time.Now().Add(30 * time.Second)
	for processExists(restartWaitPID) {
		if time.Now().After(deadline) {
			return fmt.Errorf("dashboard process %d did not stop", restartWaitPID)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := restartRename(restartStaged, restartTarget); err != nil {
		return fmt.Errorf("install upgraded runner: %w", err)
	}
	if err := restartStart(restartTarget, restartArgs); err != nil {
		return fmt.Errorf("restart dashboard: %w", err)
	}
	return nil
}

func processExists(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

func init() {
	restartDashboardHelperCmd.Flags().IntVar(&restartWaitPID, "wait-pid", 0, "PID to wait for")
	restartDashboardHelperCmd.Flags().StringVar(&restartTarget, "target", "", "Binary path to replace")
	restartDashboardHelperCmd.Flags().StringVar(&restartStaged, "staged", "", "Staged binary to install")
	restartDashboardHelperCmd.Flags().StringSliceVar(&restartArgs, "restart-arg", nil, "Argument passed to restarted Dashboard")
	rootCmd.AddCommand(restartDashboardHelperCmd)
}
