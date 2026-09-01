package runtimesupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/config"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

const endpointVerificationTimeout = 2 * time.Minute

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
	if strings.TrimSpace(publicURL) == "" {
		return nil
	}
	deadline, cancel := context.WithTimeout(ctx, endpointVerificationTimeout)
	defer cancel()
	endpoint := strings.TrimRight(strings.TrimSpace(publicURL), "/") + "/readyz"
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(deadline, http.MethodGet, endpoint, nil)
		if err == nil {
			response, requestErr := http.DefaultClient.Do(req)
			if requestErr == nil {
				var ready struct {
					RunnerID string `json:"runner_id"`
				}
				decodeErr := json.NewDecoder(response.Body).Decode(&ready)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK && decodeErr == nil && strings.TrimSpace(ready.RunnerID) == strings.TrimSpace(cfg.Runner.ID) {
					return nil
				}
				if response.StatusCode == http.StatusOK && decodeErr == nil {
					return fmt.Errorf("public endpoint belongs to runner %q, expected %q", ready.RunnerID, cfg.Runner.ID)
				}
				lastErr = fmt.Errorf("public endpoint returned %s", response.Status)
			} else {
				lastErr = requestErr
			}
		} else {
			lastErr = err
		}
		timer := time.NewTimer(500 * time.Millisecond)
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

func boolPointer(value bool) *bool { return &value }
