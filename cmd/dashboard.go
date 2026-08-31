package cmd

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var (
	dashboardConfigDir string
	dashboardOpen      bool
)

var openDashboardBrowserFunc = openDashboardBrowser

func init() {
	rootCmd.Flags().StringVar(&dashboardConfigDir, "config-dir", "", "Runner configuration directory")
	rootCmd.Flags().BoolVar(&dashboardOpen, "open-browser", true, "Open the dashboard in a browser after startup")
}

func dashboardCanOpenBrowser() bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return true
	}
	return strings.TrimSpace(os.Getenv("DISPLAY")) != "" || strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != ""
}

func openDashboardBrowser(url string) error {
	if strings.TrimSpace(url) == "" {
		return errors.New("dashboard URL is empty")
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	return command.Start()
}
