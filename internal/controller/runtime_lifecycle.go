package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

const (
	quickTunnelLogTail     = 1000
	RunnerReadinessTimeout = 2 * time.Minute
)

type runtimeProgressStarter interface {
	StartWithProgress(context.Context, func(string)) error
}

type runtimeTunnelLogger interface {
	TunnelLogs(context.Context, int) ([]dashboardruntime.LogLine, error)
}

// RuntimeLifecycle is the sole orchestration path for runtime actions. It
// deliberately owns readiness and Credimi registration as well as Docker or
// host process control, so each caller has identical semantics.
type RuntimeLifecycle struct {
	Manager dashboardruntime.Manager
	Values  dashboardruntime.Values
	GOOS    string

	HTTPClient *http.Client
	WaitReady  func(context.Context, dashboardruntime.Values) error
}

func (l RuntimeLifecycle) Start(ctx context.Context, progress func(string)) error {
	if l.Manager == nil {
		return errors.New("runtime manager unavailable")
	}
	l.clearAutoPublicURL()
	if starter, ok := l.Manager.(runtimeProgressStarter); ok {
		if err := starter.StartWithProgress(ctx, progress); err != nil {
			return err
		}
	} else if err := l.Manager.Start(ctx); err != nil {
		return err
	}
	if err := l.waitReady(ctx); err != nil {
		return runtimeStartFailure(err)
	}
	if err := l.Register(ctx); err != nil {
		return runtimeStartFailure(err)
	}
	return nil
}

// runtimeStartFailure leaves the started runtime available for diagnosis. In
// particular, a listener that never becomes ready may still have useful
// container logs; removing its Compose project would make that cause
// impossible to inspect.
func runtimeStartFailure(startErr error) error {
	return fmt.Errorf("%w; runtime remains running for inspection (use `credimi-runner runner stop` when finished)", startErr)
}

func (l RuntimeLifecycle) Stop(ctx context.Context) error {
	if l.Manager == nil {
		return errors.New("runtime manager unavailable")
	}
	if err := l.Manager.Stop(ctx); err != nil {
		return err
	}
	l.clearAutoPublicURL()
	return nil
}

func (l RuntimeLifecycle) Restart(ctx context.Context, progress func(string)) error {
	if err := l.Stop(ctx); err != nil {
		return err
	}
	return l.Start(ctx, progress)
}

func (l RuntimeLifecycle) RegisterRunning(ctx context.Context) error {
	if err := l.waitReady(ctx); err != nil {
		return err
	}
	return l.Register(ctx)
}

func (l RuntimeLifecycle) Register(ctx context.Context) error {
	if l.Manager == nil {
		return errors.New("runtime manager unavailable")
	}
	apiKey := strings.TrimSpace(l.Values["CREDIMI_USER_API_KEY"])
	if apiKey == "" {
		apiKey = strings.TrimSpace(l.Values["CREDIMI_INTERNAL_ADMIN_KEY"])
	}
	if apiKey == "" {
		return errors.New("missing Credimi API key")
	}
	publicURL, publicPort, err := l.registrationEndpoint(ctx)
	if err != nil {
		return err
	}
	l.Manager.SetPublicURL(publicURL)
	client := &dashboardruntime.CredimiClient{
		BaseURL:    strings.TrimSpace(l.Values["CREDIMI_URL"]),
		APIKey:     apiKey,
		HTTPClient: l.httpClient(),
	}
	return client.RegisterMobileRunnerResolvingName(ctx, dashboardruntime.RegisterRunnerRequest{
		RunnerID:     strings.TrimSpace(l.Values["CREDIMI_RUNNER_ID"]),
		Name:         strings.TrimSpace(l.Values["CREDIMI_RUNNER_NAME"]),
		IP:           publicURL,
		Description:  strings.TrimSpace(l.Values["CREDIMI_RUNNER_DESCRIPTION"]),
		Type:         strings.TrimSpace(l.Values["CREDIMI_RUNNER_TYPE"]),
		Port:         publicPort,
		Serial:       strings.TrimSpace(l.Values["CREDIMI_RUNNER_SERIAL"]),
		Organization: strings.TrimSpace(l.Values["CREDIMI_RUNNER_ORGANIZATION"]),
		Published:    boolPointer(isTruthy(l.Values["CREDIMI_RUNNER_PUBLISHED"])),
	})
}

func (l RuntimeLifecycle) waitReady(ctx context.Context) error {
	if !dashboardruntime.RunnerReadinessRequiredBeforeRegistration(l.Values, l.GOOS) {
		return nil
	}
	if l.WaitReady != nil {
		return l.WaitReady(ctx, l.Values)
	}
	return waitForRunnerReady(ctx, l.httpClient(), l.Values)
}

// WaitForRunnerReady verifies the runner listener, health endpoint, and
// readiness identity. It is shared by dashboard and direct CLI control.
func WaitForRunnerReady(ctx context.Context, values dashboardruntime.Values) error {
	return waitForRunnerReady(ctx, http.DefaultClient, values)
}

func waitForRunnerReady(ctx context.Context, client *http.Client, values dashboardruntime.Values) error {
	host := strings.TrimSpace(values["RUNNER_HOST"])
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := strings.TrimSpace(values["RUNNER_PORT"])
	if port == "" {
		port = dashboardruntime.DefaultRunnerPort
	}
	address := net.JoinHostPort(host, port)
	deviceRequired := dashboardruntime.DeviceReadinessRequired(values, "")
	serial := ""
	if deviceRequired {
		serial = strings.TrimSpace(values["CREDIMI_RUNNER_SERIAL"])
	}
	deadline, cancel := context.WithTimeout(ctx, RunnerReadinessTimeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(deadline, "tcp", address)
		if err == nil {
			_ = connection.Close()
			if healthErr := runnerHealth(deadline, client, host, port, serial); healthErr == nil {
				if _, readyErr := ValidateReadiness(deadline, client, "http://"+address, values); readyErr == nil {
					return nil
				} else {
					lastErr = readyErr
				}
			} else {
				lastErr = healthErr
			}
		} else {
			lastErr = err
		}
		select {
		case <-deadline.Done():
			return ReadinessFailure(values, address, lastErr, deadline.Err())
		case <-ticker.C:
		}
	}
}

// ReadinessFailure retains the technical cause while adding the configured
// target and the next diagnostic action. It is shared by dashboard startup and
// direct CLI control so users receive the same actionable failure everywhere.
func ReadinessFailure(values dashboardruntime.Values, address string, lastErr, deadlineErr error) error {
	cause := lastErr
	if cause == nil {
		cause = deadlineErr
	}
	if cause == nil {
		cause = errors.New("readiness deadline exceeded")
	}
	serial := strings.TrimSpace(values["CREDIMI_RUNNER_SERIAL"])
	switch {
	case errors.Is(cause, ErrDeviceMissing):
		return fmt.Errorf("runner did not become ready on %s: configured device %q is not available; connect it and verify it is authorized with `adb -s %s get-state`: %w", address, serial, serial, cause)
	case errors.Is(cause, ErrDeviceOffline):
		return fmt.Errorf("runner did not become ready on %s: configured device %q is offline; reconnect it and wait for `adb -s %s get-state` to report device: %w", address, serial, serial, cause)
	case errors.Is(cause, ErrDeviceUnauthorized):
		return fmt.Errorf("runner did not become ready on %s: configured device %q is unauthorized; unlock it and accept the USB debugging prompt: %w", address, serial, cause)
	}

	return fmt.Errorf("runner did not become ready on %s: the runner never opened its listener; %s: %w", address, readinessNextStep(values), cause)
}

func readinessNextStep(values dashboardruntime.Values) string {
	serial := strings.TrimSpace(values["CREDIMI_RUNNER_SERIAL"])
	switch strings.TrimSpace(values["CREDIMI_RUNNER_TYPE"]) {
	case "android_phone":
		if serial != "" {
			return fmt.Sprintf("check that Android device %q is connected and authorized (`adb -s %s get-state`), then inspect runner logs", serial, serial)
		}
		return "check that an Android device is connected and authorized, then inspect runner logs"
	case "android_emulator", "redroid":
		return "check that the configured Android runtime is running, then inspect runner logs"
	case "ios_simulator":
		return "check that the configured iOS simulator is booted, then inspect runner logs"
	default:
		return "inspect runner logs"
	}
}

func (l RuntimeLifecycle) registrationEndpoint(ctx context.Context) (string, string, error) {
	switch strings.TrimSpace(l.Values["CREDIMI_SERVICE_MODE"]) {
	case "manual":
		url := strings.TrimSpace(l.Values["RUNNER_PUBLIC_URL"])
		if url == "" {
			return "", "", errors.New("RUNNER_PUBLIC_URL is required for manual service mode")
		}
		return url, strings.TrimSpace(l.Values["RUNNER_PUBLIC_PORT"]), nil
	case "cloudflare-managed":
		domain := strings.TrimSpace(l.Values["RUNNER_DOMAIN"])
		if domain == "" {
			return "", "", errors.New("RUNNER_DOMAIN is required for managed tunnel mode")
		}
		if !strings.Contains(domain, "://") {
			domain = "https://" + domain
		}
		return domain, "", nil
	default:
		status := l.Manager.Status(ctx)
		if publicURL := strings.TrimSpace(status.PublicURL); publicURL != "" {
			return publicURL, "", nil
		}
		tail := quickTunnelLogTail
		if !status.LastStartedAt.IsZero() {
			tail = -quickTunnelLogTail
		}
		matcher := regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.trycloudflare\.com`)
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		var lastErr error
		for {
			logs, err := l.tunnelLogs(deadline, tail)
			if err != nil {
				lastErr = err
			} else {
				for i := len(logs) - 1; i >= 0; i-- {
					if found := matcher.FindString(logs[i].Message); found != "" {
						return found, "", nil
					}
				}
				lastErr = errors.New("no trycloudflare URL found in runtime logs")
			}
			select {
			case <-deadline.Done():
				return "", "", lastErr
			case <-ticker.C:
			}
		}
	}
}

func (l RuntimeLifecycle) tunnelLogs(ctx context.Context, tail int) ([]dashboardruntime.LogLine, error) {
	if logger, ok := l.Manager.(runtimeTunnelLogger); ok {
		return logger.TunnelLogs(ctx, tail)
	}
	return l.Manager.Logs(ctx, tail)
}

func (l RuntimeLifecycle) clearAutoPublicURL() {
	if strings.TrimSpace(l.Values["CREDIMI_SERVICE_MODE"]) == "auto" {
		l.Manager.SetPublicURL("")
	}
}

func (l RuntimeLifecycle) httpClient() *http.Client {
	if l.HTTPClient != nil {
		return l.HTTPClient
	}
	return http.DefaultClient
}

func runnerHealth(ctx context.Context, client *http.Client, host, port, serial string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/health", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("runner health returned %s", response.Status)
	}
	if serial == "" {
		return nil
	}
	var payload struct {
		Devices []struct {
			Serial string `json:"serial"`
			State  string `json:"state"`
		} `json:"devices"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode runner health: %w", err)
	}
	for _, device := range payload.Devices {
		if strings.TrimSpace(device.Serial) == serial && strings.TrimSpace(device.State) == "device" {
			return nil
		}
	}
	return fmt.Errorf("configured device %q is not ready", serial)
}

func boolPointer(value bool) *bool { return &value }

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
