package runtimesupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

const endpointVerificationTimeout = 2 * time.Minute
const endpointVerificationAttemptTimeout = 5 * time.Second

// Register publishes one activated runtime generation and its configured
// devices. It is called only by Supervisor after the generation has acquired
// its API listener and edge.
func Register(ctx context.Context, cfg config.Config, publicURL string) error {
	apiKey := strings.TrimSpace(cfg.Credimi.UserAPIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(cfg.Credimi.InternalAdminKey)
	}
	if apiKey == "" {
		return errors.New("missing Credimi API key")
	}
	endpoint, publicPort, err := registrationEndpoint(cfg, publicURL)
	if err != nil {
		return err
	}
	client := &dashboardruntime.CredimiClient{BaseURL: strings.TrimSpace(cfg.Credimi.URL), APIKey: apiKey, HTTPClient: http.DefaultClient}
	if err := client.RegisterMobileRunnerResolvingName(ctx, dashboardruntime.RegisterRunnerRequest{
		RunnerID: strings.TrimSpace(cfg.Runner.ID),
		Name:     strings.TrimSpace(cfg.Runner.Name),
		IP:       endpoint, Port: publicPort,
		Description:  strings.TrimSpace(cfg.Runner.Description),
		Organization: strings.TrimSpace(cfg.Runner.Organization),
		Published:    boolPointer(cfg.Runner.Published),
	}); err != nil {
		return err
	}
	// The runner record may already exist if a later device registration fails.
	// Notify the supervisor so rollback can pause only remotely owned runners.
	registrationSucceeded(ctx)
	inventory := dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.ValuesFromTypedConfig(cfg)}
	parsed, err := dashboardruntime.ParseRuntimeConfig(inventory.Host)
	if err != nil {
		if len(cfg.Devices) == 0 {
			return nil
		}
		return fmt.Errorf("load device inventory for registration: %w", err)
	}
	deviceIDs := make([]string, 0, len(parsed.Devices))
	var deviceErrors []error
	for _, device := range parsed.Devices {
		if strings.TrimSpace(device.ID) == "" {
			deviceErrors = append(deviceErrors, fmt.Errorf("device %d has no canonical ID", device.Index))
			continue
		}
		if err := client.RegisterMobileDevice(ctx, dashboardruntime.RegisterDeviceRequest{
			Organization: cfg.Runner.Organization,
			DeviceID:     device.ID, RunnerID: cfg.Runner.ID,
			Name: device.Name, Description: device.Description,
			Type: device.Type, Serial: device.Serial,
		}); err != nil {
			deviceErrors = append(deviceErrors, fmt.Errorf("register device %q: %w", device.ID, err))
			continue
		}
		deviceIDs = append(deviceIDs, device.ID)
	}
	if len(deviceErrors) > 0 {
		return errors.Join(deviceErrors...)
	}
	if err := client.ReconcileMobileDevices(ctx, dashboardruntime.ReconcileDevicesRequest{
		Organization: cfg.Runner.Organization, RunnerID: cfg.Runner.ID, DeviceIDs: deviceIDs,
	}); err != nil {
		return fmt.Errorf("reconcile configured devices: %w", err)
	}
	return nil
}

func registrationEndpoint(cfg config.Config, publicURL string) (string, string, error) {
	switch cfg.Exposure.Mode {
	case "manual":
		if strings.TrimSpace(cfg.Exposure.PublicURL) == "" {
			return "", "", errors.New("manual exposure requires public URL")
		}
		return strings.TrimSpace(cfg.Exposure.PublicURL), strings.TrimSpace(cfg.Exposure.PublicPort), nil
	case "named_tunnel":
		if strings.TrimSpace(cfg.Exposure.Domain) == "" {
			return "", "", errors.New("managed tunnel exposure requires domain")
		}
		domain := strings.TrimSpace(cfg.Exposure.Domain)
		if !strings.Contains(domain, "://") {
			domain = "https://" + domain
		}
		return domain, "", nil
	default:
		if strings.TrimSpace(publicURL) == "" {
			return "", "", errors.New("quick tunnel URL is unavailable")
		}
		return strings.TrimSpace(publicURL), "", nil
	}
}

// VerifyPublicEndpoint waits until the URL served by the current edge belongs
// to the current runner generation.
func VerifyPublicEndpoint(ctx context.Context, cfg config.Config, publicURL string) error {
	return (&endpointVerifier{
		normalClient: http.DefaultClient,
		quickTunnelClient: func() *http.Client {
			return newQuickTunnelHTTPClient()
		},
		overallTimeout: endpointVerificationTimeout,
		attemptTimeout: endpointVerificationAttemptTimeout,
		retryDelay:     500 * time.Millisecond,
	}).verify(ctx, cfg, publicURL)
}

type endpointVerifier struct {
	normalClient      *http.Client
	quickTunnelClient func() *http.Client
	overallTimeout    time.Duration
	attemptTimeout    time.Duration
	retryDelay        time.Duration
}

func (v *endpointVerifier) verify(ctx context.Context, cfg config.Config, publicURL string) error {
	baseURL := strings.TrimSpace(publicURL)
	if cfg.Exposure.Mode == "manual" {
		baseURL = strings.TrimSpace(cfg.Exposure.PublicURL)
	}
	if baseURL == "" {
		return nil
	}
	expectedBootID := strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_BOOT_ID"))
	if expectedBootID == "" {
		return errors.New("current runner boot ID is unavailable")
	}
	endpoint, err := publicEndpointVerificationURL(cfg, baseURL)
	if err != nil {
		return err
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse public endpoint verification URL %q: %w", endpoint, err)
	}
	useQuickTunnelFallback := cfg.Exposure.Mode == "quick_tunnel" && isTryCloudflareHostname(parsedEndpoint.Hostname())
	normalClient := v.normalClient
	if normalClient == nil {
		normalClient = http.DefaultClient
	}
	quickTunnelClient := v.quickTunnelClient
	if quickTunnelClient == nil {
		quickTunnelClient = newQuickTunnelHTTPClient
	}
	overallTimeout := v.overallTimeout
	if overallTimeout <= 0 {
		overallTimeout = endpointVerificationTimeout
	}
	attemptTimeout := v.attemptTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = endpointVerificationAttemptTimeout
	}
	retryDelay := v.retryDelay
	if retryDelay < 0 {
		retryDelay = 0
	}
	deadline, cancel := context.WithTimeout(ctx, overallTimeout)
	defer cancel()
	var lastErr error
	var fallbackClient *http.Client
	for {
		attemptCtx, attemptCancel := context.WithTimeout(deadline, attemptTimeout)
		requestErr := verifyPublicEndpointAttempt(attemptCtx, normalClient, endpoint, cfg, expectedBootID)
		attemptCancel()
		if requestErr == nil {
			return nil
		}
		lastErr = requestErr
		if useQuickTunnelFallback && isDNSNotFound(requestErr) {
			if fallbackClient == nil {
				fallbackClient = quickTunnelClient()
			}
			fallbackCtx, fallbackCancel := context.WithTimeout(deadline, attemptTimeout)
			fallbackErr := verifyPublicEndpointAttempt(fallbackCtx, fallbackClient, endpoint, cfg, expectedBootID)
			fallbackCancel()
			if fallbackErr == nil {
				return nil
			}
			if isEndpointIdentityError(fallbackErr) {
				return fallbackErr
			}
			if isDNSNotFound(fallbackErr) {
				lastErr = fmt.Errorf("quick tunnel hostname did not become resolvable: system DNS: %v; Cloudflare DNS: %w", requestErr, fallbackErr)
			} else {
				lastErr = fmt.Errorf("quick tunnel fallback verification: %w", fallbackErr)
			}
		}
		if isEndpointIdentityError(requestErr) {
			return requestErr
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-deadline.Done():
			timer.Stop()
			if lastErr == nil {
				return deadline.Err()
			}
			return fmt.Errorf("public endpoint did not become ready: %w", lastErr)
		case <-timer.C:
		}
	}
}

type endpointIdentityError struct{ message string }

func (e *endpointIdentityError) Error() string { return e.message }

func isEndpointIdentityError(err error) bool {
	var identityErr *endpointIdentityError
	return errors.As(err, &identityErr)
}

func verifyPublicEndpointAttempt(ctx context.Context, client *http.Client, endpoint string, cfg config.Config, expectedBootID string) error {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("public endpoint returned %s", response.Status)
	}
	var ready struct {
		RunnerID string `json:"runner_id"`
		BootID   string `json:"boot_id"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&ready)
	if decodeErr != nil {
		return fmt.Errorf("public endpoint returned %s with malformed readiness JSON: %w", response.Status, decodeErr)
	}
	if strings.TrimSpace(ready.RunnerID) != strings.TrimSpace(cfg.Runner.ID) {
		return &endpointIdentityError{message: fmt.Sprintf("public endpoint belongs to runner %q, expected %q", ready.RunnerID, cfg.Runner.ID)}
	}
	if strings.TrimSpace(ready.BootID) != expectedBootID {
		return &endpointIdentityError{message: fmt.Sprintf("public endpoint belongs to boot %q, expected current boot %q", ready.BootID, expectedBootID)}
	}
	return nil
}

func isDNSNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

func isTryCloudflareHostname(hostname string) bool {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	const suffix = ".trycloudflare.com"
	if !strings.HasSuffix(hostname, suffix) || len(hostname) <= len(suffix) || net.ParseIP(hostname) != nil {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(hostname, suffix), ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func newQuickTunnelHTTPClient() *http.Client {
	return newQuickTunnelHTTPClientWithDNSServers([]string{"1.1.1.1:53", "1.0.0.1:53"})
}

func newQuickTunnelHTTPClientWithDNSServers(servers []string) *http.Client {
	servers = append([]string(nil), servers...)
	resolver := &net.Resolver{PreferGo: true}
	if len(servers) == 0 {
		return &http.Client{Transport: http.DefaultTransport}
	}
	var next atomic.Uint32
	resolver.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		server := servers[(next.Add(1)-1)%uint32(len(servers))]
		if network == "" {
			network = "udp"
		}
		return (&net.Dialer{}).DialContext(ctx, network, server)
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	} else {
		transport = transport.Clone()
	}
	// A proxy can resolve the URL host on its own, bypassing the explicit DNS
	// path below. Quick-tunnel verification must connect directly so the
	// configured resolver remains authoritative for this request.
	transport.Proxy = nil
	transport.DialContext = quickTunnelDialContext(resolver)
	return &http.Client{Transport: transport}
}

func quickTunnelDialContext(resolver *net.Resolver) func(context.Context, string, string) (net.Conn, error) {
	var dialer net.Dialer
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		ordered := make([]net.IPAddr, 0, len(ips))
		for _, ip := range ips {
			if ip.IP.To4() != nil {
				ordered = append(ordered, ip)
			}
		}
		for _, ip := range ips {
			if ip.IP.To4() == nil {
				ordered = append(ordered, ip)
			}
		}
		var lastErr error
		for _, ip := range ordered {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			return nil, fmt.Errorf("quick tunnel DNS returned no addresses for %s", host)
		}
		return nil, lastErr
	}
}

func publicEndpointVerificationURL(cfg config.Config, publicURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil {
		return "", fmt.Errorf("parse public endpoint URL %q: %w", publicURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("public endpoint URL %q must be absolute", publicURL)
	}
	if port := parsed.Port(); port != "" {
		parsedPort, err := strconv.Atoi(port)
		if err != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", fmt.Errorf("public endpoint URL %q has invalid port %q", publicURL, port)
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return "", fmt.Errorf("public endpoint URL %q has an empty port", publicURL)
	}
	if cfg.Exposure.Mode == "manual" && parsed.Port() == "" {
		port := strings.TrimSpace(cfg.Exposure.PublicPort)
		if port != "" {
			parsedPort, err := strconv.Atoi(port)
			if err != nil || parsedPort < 1 || parsedPort > 65535 {
				return "", fmt.Errorf("invalid manual public port %q", port)
			}
			parsed.Host = net.JoinHostPort(parsed.Hostname(), port)
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/readyz"
	if parsed.RawPath != "" {
		parsed.RawPath = strings.TrimRight(parsed.RawPath, "/") + "/readyz"
	}
	return parsed.String(), nil
}

func boolPointer(value bool) *bool { return &value }
