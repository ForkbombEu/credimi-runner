//go:build darwin

package servicemanager

import (
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const launchAgentLabel = "eu.forkbomb.credimi-runner"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

type LaunchAgentManager struct {
	ConfigDir  string
	BinaryPath string
	Run        func(context.Context, string, ...string) error
}

func (m *LaunchAgentManager) command(ctx context.Context, args ...string) error {
	if m.Run != nil {
		return m.Run(ctx, args[0], args[1:]...)
	}
	return exec.CommandContext(ctx, args[0], args[1:]...).Run()
}
func (m *LaunchAgentManager) plist() string {
	logPath := filepath.Join(m.ConfigDir, "service.log")
	return fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict><key>Label</key><string>%s</string><key>ProgramArguments</key><array><string>%s</string><string>internal-service</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string><key>EnvironmentVariables</key><dict><key>CREDIMI_RUNNER_CONFIG_DIR</key><string>%s</string></dict></dict></plist>\n", html.EscapeString(launchAgentLabel), html.EscapeString(m.BinaryPath), html.EscapeString(logPath), html.EscapeString(logPath), html.EscapeString(m.ConfigDir))
}
func (m *LaunchAgentManager) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(launchAgentPath()), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(launchAgentPath()), ".credimi-runner-*.plist")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(m.plist()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, launchAgentPath()); err != nil {
		return err
	}
	if err := m.command(ctx, "launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel)); err == nil {
		return nil
	}
	if err := m.command(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), launchAgentPath()); err != nil {
		return err
	}
	return m.command(ctx, "launchctl", "kickstart", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel))
}
func (m *LaunchAgentManager) Stop(ctx context.Context) error {
	err := m.command(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel))
	if err != nil && !os.IsNotExist(err) && !strings.Contains(strings.ToLower(err.Error()), "could not find") && !strings.Contains(strings.ToLower(err.Error()), "no such process") {
		return err
	}
	return nil
}
func (m *LaunchAgentManager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}
func (m *LaunchAgentManager) Status(ctx context.Context) (Status, error) {
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel)
	err := m.command(ctx, "launchctl", "print", target)
	return Status{Running: err == nil, DashboardURL: "http://127.0.0.1:8051"}, nil
}
func (m *LaunchAgentManager) Logs(ctx context.Context, o LogOptions) error {
	lines := o.Lines
	if lines <= 0 {
		lines = 200
	}
	args := []string{"-n", strconv.Itoa(lines)}
	if o.Follow {
		args = append(args, "-F")
	}
	args = append(args, filepath.Join(m.ConfigDir, "service.log"))
	return m.command(ctx, append([]string{"tail"}, args...)...)
}
