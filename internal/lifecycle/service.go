// Package lifecycle reports one runner host and its independently observed
// devices to Credimi. It has no dashboard, environment, or Compose dependency.
package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/device"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

type Host struct {
	ID, Name, Organization, Description, PublicURL, Port string
	Published                                            bool
}

type Config struct {
	CredimiURL, APIKey string
	Host               Host
	HeartbeatInterval  time.Duration
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}
type Probe func(context.Context, device.Device) device.Status

type Service struct {
	config   Config
	registry *device.Registry
	client   HTTPClient
	probe    Probe
	now      func() time.Time
}

func New(config Config, registry *device.Registry, client HTTPClient, probe Probe) (*Service, error) {
	if strings.TrimSpace(config.CredimiURL) == "" || strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("Credimi URL and API key are required")
	}
	if strings.TrimSpace(config.Host.ID) == "" {
		return nil, errors.New("runner ID is required")
	}
	if registry == nil {
		return nil, errors.New("device registry is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	if probe == nil {
		probe = func(_ context.Context, target device.Device) device.Status {
			status := device.Status{DeviceID: target.ID, Enabled: target.Enabled, ObservedAt: time.Now()}
			// Redroid is an ephemeral runtime. Its configured serial is not
			// expected to exist until a Credimi activity creates the remote
			// runtime, so idle configuration is a valid ready state.
			if target.Type == runnerconfig.DeviceRedroid {
				status.Online, status.Ready, status.Reason = true, true, "configured; idle runtime"
				return status
			}
			status.Reason = "probe is not configured"
			return status
		}
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 30 * time.Second
	}
	return &Service{config: config, registry: registry, client: client, probe: probe, now: time.Now}, nil
}

func (s *Service) Register(ctx context.Context) error {
	host := s.config.Host
	if err := s.post(ctx, []string{"api", "mobile-runner"}, map[string]any{
		"runner_id": host.ID, "name": host.Name, "organization": host.Organization,
		"description": host.Description, "ip": host.PublicURL, "port": host.Port, "published": host.Published,
	}); err != nil {
		return fmt.Errorf("register runner: %w", err)
	}

	var errs []error
	ids := make([]string, 0, len(s.registry.List()))
	for _, target := range s.registry.List() {
		ids = append(ids, target.ID)
		if err := s.post(ctx, []string{"api", "mobile-device"}, deviceRegistration(host, target)); err != nil {
			errs = append(errs, fmt.Errorf("register device %q: %w", target.ID, err))
		}
	}
	if err := s.post(ctx, []string{"api", "mobile-device", "reconcile"}, map[string]any{"organization": host.Organization, "runner_id": host.ID, "device_ids": ids}); err != nil {
		errs = append(errs, fmt.Errorf("reconcile devices: %w", err))
	}
	return errors.Join(errs...)
}

func deviceRegistration(host Host, target device.Device) map[string]any {
	serial := ""
	if target.AndroidPhysical != nil {
		serial = target.AndroidPhysical.Serial
		if target.AndroidPhysical.Transport == "wifi" {
			serial = net.JoinHostPort(target.AndroidPhysical.WiFiIP, target.AndroidPhysical.WiFiPort)
		}
	}
	if target.Redroid != nil {
		serial = net.JoinHostPort(target.Redroid.Host, strconv.Itoa(target.Redroid.ADBPort))
	}
	if target.IOSSimulator != nil {
		serial = target.IOSSimulator.UDID
	}
	return map[string]any{"organization": host.Organization, "runner_id": host.ID, "device_id": target.ID, "name": target.Name, "description": target.Description, "type": target.Type, "serial": serial}
}

type DeviceState struct {
	DeviceID   string    `json:"device_id"`
	Online     bool      `json:"online"`
	Reason     string    `json:"reason,omitempty"`
	Ready      bool      `json:"-"`
	Busy       bool      `json:"-"`
	ObservedAt time.Time `json:"-"`
}

func (s *Service) DeviceStates(ctx context.Context) []DeviceState {
	targets := s.registry.List()
	states := make([]DeviceState, 0, len(targets))
	for _, target := range targets {
		if !target.Enabled {
			states = append(states, DeviceState{DeviceID: target.ID, Reason: "disabled"})
			continue
		}
		status := s.probe(ctx, target)
		reason := strings.TrimSpace(status.Reason)
		if reason == "" && (!status.Online || !status.Ready) {
			reason = "not ready"
		}
		states = append(states, DeviceState{DeviceID: target.ID, Online: status.Online, Ready: status.Ready, Busy: status.Busy, ObservedAt: status.ObservedAt, Reason: reason})
	}
	return states
}

func (s *Service) Resume(ctx context.Context, reason string) error {
	return s.postLifecycle(ctx, "resume", reason)
}
func (s *Service) Heartbeat(ctx context.Context) error { return s.postLifecycle(ctx, "heartbeat", "") }
func (s *Service) Pause(ctx context.Context, reason string) error {
	return s.postLifecycle(ctx, "pause", reason)
}

func (s *Service) postLifecycle(ctx context.Context, action, reason string) error {
	payload := map[string]any{"runner_id": s.config.Host.ID, "devices": s.DeviceStates(ctx)}
	if strings.TrimSpace(reason) != "" {
		payload["reason"] = reason
	}
	return s.post(ctx, []string{"api", "mobile-runner", "lifecycle", action}, payload)
}

func (s *Service) Start(ctx context.Context) (func(), error) {
	if err := s.Heartbeat(ctx); err != nil {
		return nil, err
	}
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(s.config.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				_ = s.Heartbeat(ctx)
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }) }, nil
}

func (s *Service) post(ctx context.Context, path []string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, utils.JoinURL(s.config.CredimiURL, path...), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Credimi-Api-Key", s.config.APIKey)
	response, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("unexpected status %s: %s", response.Status, strings.TrimSpace(string(message)))
}
