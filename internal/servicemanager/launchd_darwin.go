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

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	controller "github.com/forkbombeu/credimi-runner/internal/controller/identity"
)

const launchAgentLabel = "eu.forkbomb.credimi-runner"

type LaunchAgentManager struct {
	ConfigDir  string
	BinaryPath string
	HomeDir    string
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
func (m *LaunchAgentManager) paths() (string, string, string, error) {
	home := m.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", "", "", fmt.Errorf("resolve user home directory: %w", err)
		}
	}
	if m.BinaryPath == "" {
		binary, err := os.Executable()
		if err != nil {
			return "", "", "", fmt.Errorf("resolve executable: %w", err)
		}
		m.BinaryPath = binary
	}
	var err error
	m.BinaryPath, err = filepath.Abs(m.BinaryPath)
	if err != nil {
		return "", "", "", err
	}
	m.ConfigDir, err = filepath.Abs(m.ConfigDir)
	if err != nil {
		return "", "", "", err
	}
	if err := os.MkdirAll(m.ConfigDir, 0700); err != nil {
		return "", "", "", err
	}
	persistent := filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
	transient := filepath.Join(m.ConfigDir, "service-launchd.plist")
	return home, persistent, transient, nil
}

func (m *LaunchAgentManager) writePlist(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credimi-runner-*.plist")
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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func launchAgentTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel)
}

func (m *LaunchAgentManager) loaded(ctx context.Context) bool {
	return m.command(ctx, "launchctl", "print", launchAgentTarget()) == nil
}

func (m *LaunchAgentManager) Start(ctx context.Context) error {
	autostart, err := loadAutostart(m.ConfigDir)
	if err != nil {
		return err
	}
	_, persistent, transient, err := m.paths()
	if err != nil {
		return err
	}
	if m.loaded(ctx) {
		if !autostart {
			_ = os.Remove(persistent)
		}
		return nil
	}
	path := persistent
	if !autostart {
		_ = os.Remove(persistent)
		path = transient
	}
	if err := m.writePlist(path); err != nil {
		return err
	}
	if err := m.command(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path); err != nil {
		return err
	}
	return m.command(ctx, "launchctl", "kickstart", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel))
}
func (m *LaunchAgentManager) Stop(ctx context.Context) error {
	autostart, err := loadAutostart(m.ConfigDir)
	if err != nil {
		return err
	}
	err = m.command(ctx, "launchctl", "bootout", launchAgentTarget())
	if err != nil && !os.IsNotExist(err) && !strings.Contains(strings.ToLower(err.Error()), "could not find") && !strings.Contains(strings.ToLower(err.Error()), "no such process") {
		return err
	}
	if !autostart {
		_, persistent, transient, pathErr := m.paths()
		if pathErr != nil {
			return pathErr
		}
		_ = os.Remove(persistent)
		_ = os.Remove(transient)
	}
	return nil
}
func (m *LaunchAgentManager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}
func (m *LaunchAgentManager) Enable(ctx context.Context) error {
	if _, err := loadAutostart(m.ConfigDir); err != nil {
		return err
	}
	_, persistent, transient, err := m.paths()
	if err != nil {
		return err
	}
	if err := m.writePlist(persistent); err != nil {
		return err
	}
	_ = os.Remove(transient)
	if err := saveAutostart(m.ConfigDir, true); err != nil {
		return err
	}
	return nil
}
func (m *LaunchAgentManager) Disable(ctx context.Context) error {
	if _, err := loadAutostart(m.ConfigDir); err != nil {
		return err
	}
	_, persistent, transient, err := m.paths()
	if err != nil {
		return err
	}
	loaded := m.loaded(ctx)
	_ = os.Remove(persistent)
	if !loaded {
		_ = os.Remove(transient)
	}
	return saveAutostart(m.ConfigDir, false)
}
func (m *LaunchAgentManager) Status(ctx context.Context) (Status, error) {
	autostart, err := loadAutostart(m.ConfigDir)
	if err != nil {
		return Status{}, err
	}
	err = m.command(ctx, "launchctl", "print", launchAgentTarget())
	status := Status{Autostart: autostart, Running: err == nil, DashboardURL: "http://127.0.0.1:8051"}
	cfg, cfgErr := runnerconfig.LoadFile(filepath.Join(m.ConfigDir, "config.toml"))
	if cfgErr == nil {
		status.DashboardURL = desiredDashboardURL(cfg)
	}
	if status.Running {
		probeCtx, cancel := context.WithTimeout(ctx, controller.ProbeTimeout)
		if live, liveErr := readLiveController(probeCtx, m.ConfigDir); liveErr == nil {
			status.DashboardURL = strings.TrimRight(live.PublicURL, "/")
			if cfgErr == nil {
				wantHost, wantPort := effectiveDashboardListen(cfg.Server.DashboardListen)
				status.ServiceRestartRequired = !isEquivalentListener(wantHost, wantPort, live.ListenHost, strconv.Itoa(live.ListenPort))
			}
		}
		cancel()
	}
	populateRuntimeState(m.ConfigDir, &status)
	return status, nil
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
