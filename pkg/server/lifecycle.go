package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

const (
	defaultLifecycleEnabled           = true
	defaultHeartbeatInterval          = 30 * time.Second
	defaultLifecycleRequestTimeout    = 15 * time.Second
	defaultLifecycleRequestAttempts   = 3
	lifecycleEnabledEnvName           = "CREDIMI_RUNNER_LIFECYCLE_ENABLED"
	lifecycleHeartbeatIntervalEnvName = "CREDIMI_RUNNER_HEARTBEAT_INTERVAL"
	lifecycleRequestTimeoutEnvName    = "CREDIMI_RUNNER_LIFECYCLE_REQUEST_TIMEOUT"
)

type RunnerLifecycleConfig struct {
	Enabled           bool
	RunnerID          string
	CredimiURL        string
	APIKey            string
	HeartbeatInterval time.Duration
	RequestTimeout    time.Duration
	Devices           []LifecycleDevice
}

type lifecyclePayload struct {
	RunnerID  string            `json:"runner_id"`
	RequestID string            `json:"request_id,omitempty"`
	Devices   []LifecycleDevice `json:"devices,omitempty"`
	Reason    string            `json:"reason,omitempty"`
}

var lifecycleRequestSequence atomic.Uint64

type LifecycleDevice struct {
	DeviceID string `json:"device_id"`
	Online   bool   `json:"online"`
	Reason   string `json:"reason,omitempty"`
}

type lifecycleTicker interface {
	Stop()
	Chan() <-chan time.Time
}

type timeTicker struct {
	ticker *time.Ticker
}

func (t timeTicker) Stop() {
	t.ticker.Stop()
}

func (t timeTicker) Chan() <-chan time.Time {
	return t.ticker.C
}

type RunnerLifecycleClient struct {
	cfg        RunnerLifecycleConfig
	httpClient HTTPClient
	store      *ProcessStore
	newTicker  func(time.Duration) lifecycleTicker
	warnf      func(string, ...any)
	devices    []LifecycleDevice
	inventory  func() ([]LifecycleDevice, bool)
	readiness  func() Readiness
	retryDelay func(int) time.Duration
}

func LoadRunnerLifecycleConfig(instance utils.Instance, loaders ...func() (dashboardruntime.RunnerRuntimeConfig, error)) RunnerLifecycleConfig {
	cfg := RunnerLifecycleConfig{
		Enabled:           loadLifecycleEnabled(),
		RunnerID:          strings.TrimSpace(utils.GetEnvironmentVariable("CREDIMI_RUNNER_ID")),
		CredimiURL:        strings.TrimSpace(instance.URL),
		HeartbeatInterval: loadLifecycleDuration(lifecycleHeartbeatIntervalEnvName, defaultHeartbeatInterval),
		RequestTimeout:    loadLifecycleDuration(lifecycleRequestTimeoutEnvName, defaultLifecycleRequestTimeout),
	}

	if strings.TrimSpace(instance.UserAPIKey) != "" {
		cfg.APIKey = strings.TrimSpace(instance.UserAPIKey)
	} else {
		cfg.APIKey = strings.TrimSpace(instance.InternalAdminKey)
	}
	var inventory dashboardruntime.RunnerRuntimeConfig
	var err error
	if len(loaders) > 0 && loaders[0] != nil {
		inventory, err = loaders[0]()
	} else {
		inventory, err = dashboardruntime.RuntimeConfigFromEnvironment()
	}
	if err == nil {
		for _, device := range inventory.Devices {
			cfg.Devices = append(cfg.Devices, LifecycleDevice{DeviceID: device.ID, Online: device.Enabled})
		}
	}

	return cfg
}

func NewRunnerLifecycleClient(cfg RunnerLifecycleConfig, httpClient HTTPClient, store *ProcessStore, loaders ...func() (dashboardruntime.RunnerRuntimeConfig, error)) *RunnerLifecycleClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	client := &RunnerLifecycleClient{
		cfg:        cfg,
		httpClient: httpClient,
		store:      store,
		newTicker: func(interval time.Duration) lifecycleTicker {
			return timeTicker{ticker: time.NewTicker(interval)}
		},
		warnf:     log.Printf,
		devices:   append([]LifecycleDevice(nil), cfg.Devices...),
		inventory: currentLifecycleInventory,
		readiness: func() Readiness {
			return NewReadinessService().Check()
		},
		retryDelay: func(attempt int) time.Duration {
			return time.Duration(attempt) * 250 * time.Millisecond
		},
	}
	if len(loaders) > 0 && loaders[0] != nil {
		loader := loaders[0]
		client.inventory = func() ([]LifecycleDevice, bool) {
			inventory, err := loader()
			if err != nil {
				return nil, false
			}
			devices := make([]LifecycleDevice, 0, len(inventory.Devices))
			for _, device := range inventory.Devices {
				devices = append(devices, LifecycleDevice{DeviceID: device.ID, Online: device.Enabled})
			}
			return devices, true
		}
		readiness := NewReadinessService()
		readiness.RuntimeConfig = loader
		client.readiness = func() Readiness { return readiness.Check() }
	}
	return client
}

func (c *RunnerLifecycleClient) Resume(ctx context.Context, reason string) error {
	return c.post(ctx, []string{"api", "mobile-runner", "lifecycle", "resume"}, lifecyclePayload{
		RunnerID: c.cfg.RunnerID,
		Devices:  c.currentDevices(),
		Reason:   reason,
	})
}

func (c *RunnerLifecycleClient) Heartbeat(ctx context.Context) error {
	devices := c.currentDevices()
	reason := ""
	if len(devices) == 0 {
		reason = "heartbeat"
	}
	return c.post(ctx, []string{"api", "mobile-runner", "lifecycle", "heartbeat"}, lifecyclePayload{
		RunnerID: c.cfg.RunnerID,
		Devices:  devices,
		Reason:   reason,
	})
}

// currentDevices refreshes heartbeat state for every device in the configured
// inventory. A disabled or unavailable device stays in the inventory but is
// explicitly reported offline, so it never hides healthy siblings behind a
// host pause.
func (c *RunnerLifecycleClient) currentDevices() []LifecycleDevice {
	devices := append([]LifecycleDevice(nil), c.devices...)
	// Production supplies a generation-scoped loader here. Standalone callers
	// may retain the legacy environment-backed fallback configured above.
	if c.inventory != nil {
		if current, ok := c.inventory(); ok {
			devices = current
		}
	}
	if c.readiness == nil {
		return devices
	}
	ready := c.readiness()
	for index := range devices {
		state, ok := ready.Devices[devices[index].DeviceID]
		if !devices[index].Online {
			devices[index].Reason = "disabled"
			continue
		}
		if !ok {
			devices[index].Online = false
			devices[index].Reason = "not reported by runner readiness"
			continue
		}
		if !state.Ready {
			devices[index].Online = false
			devices[index].Reason = state.State
			if devices[index].Reason == "" {
				devices[index].Reason = "not ready"
			}
		}
	}
	return devices
}

func currentLifecycleInventory() ([]LifecycleDevice, bool) {
	inventory, err := dashboardruntime.RuntimeConfigFromEnvironment()
	if err != nil {
		return nil, false
	}
	devices := make([]LifecycleDevice, 0, len(inventory.Devices))
	for _, device := range inventory.Devices {
		devices = append(devices, LifecycleDevice{DeviceID: device.ID, Online: device.Enabled})
	}
	return devices, true
}

func (c *RunnerLifecycleClient) SetDevices(devices []LifecycleDevice) {
	if c == nil {
		return
	}
	c.devices = append([]LifecycleDevice(nil), devices...)
}

func (c *RunnerLifecycleClient) Pause(ctx context.Context, reason string) error {
	return c.post(ctx, []string{"api", "mobile-runner", "lifecycle", "pause"}, lifecyclePayload{
		RunnerID: c.cfg.RunnerID,
		Reason:   reason,
	})
}

func (c *RunnerLifecycleClient) StartHeartbeatLoop(ctx context.Context) func() {
	if c == nil || !c.cfg.Enabled {
		return func() {}
	}

	ticker := c.newTicker(c.cfg.HeartbeatInterval)
	stopCh := make(chan struct{})
	var once sync.Once

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.Chan():
				if err := c.Heartbeat(ctx); err != nil {
					c.warnf("warning: failed to send runner heartbeat: %v", err)
				}
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(stopCh)
		})
	}
}

func (c *RunnerLifecycleClient) post(ctx context.Context, path []string, payload lifecyclePayload) error {
	if c == nil || !c.cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(c.cfg.CredimiURL) == "" {
		c.warnf("warning: skipping runner lifecycle request to /%s: missing credimi url", strings.Join(path, "/"))
		return nil
	}
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		c.warnf("warning: skipping runner lifecycle request to /%s: missing credimi api key", strings.Join(path, "/"))
		return nil
	}
	if strings.TrimSpace(c.cfg.RunnerID) == "" {
		c.warnf("warning: skipping runner lifecycle request to /%s: missing runner id", strings.Join(path, "/"))
		return nil
	}

	if payload.RequestID == "" {
		payload.RequestID = newLifecycleRequestID()
	}
	var lastErr error
	for attempt := 1; attempt <= defaultLifecycleRequestAttempts; attempt++ {
		lastErr = c.postOnce(ctx, path, payload)
		if lastErr == nil || !isLifecycleRetryable(lastErr) || attempt == defaultLifecycleRequestAttempts {
			return lastErr
		}
		delay := time.Duration(0)
		if c.retryDelay != nil {
			delay = c.retryDelay(attempt)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (c *RunnerLifecycleClient) postOnce(ctx context.Context, path []string, payload lifecyclePayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal lifecycle payload: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, utils.JoinURL(c.cfg.CredimiURL, path...), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create lifecycle request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	setAPIKeyHeader(req, c.cfg.APIKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send lifecycle request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return fmt.Errorf("lifecycle request failed with status %s and unreadable body: %w", resp.Status, readErr)
	}
	return fmt.Errorf("lifecycle request failed with status %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
}

func isLifecycleRetryable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary())
}

func newLifecycleRequestID() string {
	return fmt.Sprintf("%s-%d-%d", strings.TrimSpace(utils.GetEnvironmentVariable("CREDIMI_RUNNER_BOOT_ID")), time.Now().UnixNano(), lifecycleRequestSequence.Add(1))
}

func loadLifecycleEnabled() bool {
	raw := strings.TrimSpace(utils.GetEnvironmentVariable(lifecycleEnabledEnvName))
	if raw == "" {
		return defaultLifecycleEnabled
	}

	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("[WARN] Invalid %s value %q (using %t)", lifecycleEnabledEnvName, raw, defaultLifecycleEnabled)
		return defaultLifecycleEnabled
	}

	return enabled
}

func loadLifecycleDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(utils.GetEnvironmentVariable(name))
	if raw == "" {
		return fallback
	}

	duration, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("[WARN] Invalid %s value %q (using %s)", name, raw, fallback)
		return fallback
	}
	if duration <= 0 {
		log.Printf("[WARN] Non-positive %s value %q (using %s)", name, raw, fallback)
		return fallback
	}

	return duration
}
