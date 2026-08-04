package controller

import (
	"context"
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
	// Compose can stop the runner and tunnel concurrently, so do not rely on
	// the process receiving enough time to send its own graceful pause request.
	// A failed notification must not prevent local shutdown: live health checks
	// still make the device catalog unavailable immediately.
	l.pauseRegisteredRunner(ctx)
	if err := l.Manager.Stop(ctx); err != nil {
		return err
	}
	l.clearAutoPublicURL()
	return nil
}

func (l RuntimeLifecycle) pauseRegisteredRunner(ctx context.Context) {
	apiKey := strings.TrimSpace(l.Values["CREDIMI_USER_API_KEY"])
	if apiKey == "" {
		apiKey = strings.TrimSpace(l.Values["CREDIMI_INTERNAL_ADMIN_KEY"])
	}
	runnerID := strings.TrimSpace(l.Values["CREDIMI_RUNNER_ID"])
	if apiKey == "" || runnerID == "" || strings.TrimSpace(l.Values["CREDIMI_URL"]) == "" {
		return
	}
	pauseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = (&dashboardruntime.CredimiClient{BaseURL: l.Values["CREDIMI_URL"], APIKey: apiKey, HTTPClient: l.httpClient()}).PauseMobileRunner(pauseCtx, dashboardruntime.PauseRunnerRequest{RunnerID: runnerID, Reason: "dashboard_stop"})
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
	if err := client.RegisterMobileRunnerResolvingName(ctx, dashboardruntime.RegisterRunnerRequest{
		RunnerID:     strings.TrimSpace(l.Values["CREDIMI_RUNNER_ID"]),
		Name:         strings.TrimSpace(l.Values["CREDIMI_RUNNER_NAME"]),
		IP:           publicURL,
		Description:  strings.TrimSpace(l.Values["CREDIMI_RUNNER_DESCRIPTION"]),
		Port:         publicPort,
		Organization: strings.TrimSpace(l.Values["CREDIMI_RUNNER_ORGANIZATION"]),
		Published:    boolPointer(isTruthy(l.Values["CREDIMI_RUNNER_PUBLISHED"])),
	}); err != nil {
		return err
	}
	inventory, err := dashboardruntime.ParseRuntimeConfig(l.Values)
	if err != nil {
		if strings.TrimSpace(l.Values["CREDIMI_DEVICE_COUNT"]) == "" {
			// Host setup may be registered before the first device is added.
			// serve itself rejects this state before workers start.
			return nil
		}
		return fmt.Errorf("load device inventory for registration: %w", err)
	}
	var deviceErrors []error
	for _, device := range inventory.Devices {
		if strings.TrimSpace(device.ID) == "" {
			deviceErrors = append(deviceErrors, fmt.Errorf("device %d has no canonical ID; preview it from the dashboard before starting", device.Index))
			continue
		}
		if err := client.RegisterMobileDevice(ctx, dashboardruntime.RegisterDeviceRequest{
			Organization: strings.TrimSpace(l.Values["CREDIMI_RUNNER_ORGANIZATION"]),
			DeviceID:     device.ID, RunnerID: inventory.Host["CREDIMI_RUNNER_ID"], Name: device.Name,
			Description: device.Description, Type: device.Type, Serial: device.Serial,
		}); err != nil {
			deviceErrors = append(deviceErrors, fmt.Errorf("register device %q: %w", device.ID, err))
		}
	}
	if len(deviceErrors) > 0 {
		return errors.Join(deviceErrors...)
	}
	return nil
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
	deadline, cancel := context.WithTimeout(ctx, RunnerReadinessTimeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(deadline, "tcp", address)
		if err == nil {
			_ = connection.Close()
			if healthErr := runnerHealth(deadline, client, host, port); healthErr == nil {
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
	switch {
	case errors.Is(cause, ErrDeviceMissing):
		return fmt.Errorf("runner did not become ready on %s: a configured device is not available; inspect its device readiness and connection: %w", address, cause)
	case errors.Is(cause, ErrDeviceOffline):
		return fmt.Errorf("runner did not become ready on %s: a configured device is offline; reconnect it and inspect its device readiness: %w", address, cause)
	case errors.Is(cause, ErrDeviceUnauthorized):
		return fmt.Errorf("runner did not become ready on %s: a configured device is unauthorized; unlock it and accept its USB debugging prompt: %w", address, cause)
	}

	return fmt.Errorf("runner did not become ready on %s: the runner never opened its listener; %s: %w", address, readinessNextStep(values), cause)
}

func readinessNextStep(values dashboardruntime.Values) string {
	if inventory, err := dashboardruntime.ParseRuntimeConfig(values); err == nil && len(inventory.Devices) > 0 {
		return "inspect each configured device's readiness and runner logs"
	}
	return "inspect runner logs"
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
		// LifecycleManager turns a positive tail into a Compose --since filter
		// using LastStartedAt. Never search historical quick-tunnel output: old
		// trycloudflare URLs expire on every restart and must not be registered.
		tail := quickTunnelLogTail
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

func runnerHealth(ctx context.Context, client *http.Client, host, port string) error {
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
	return nil
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
