package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

const (
	defaultLifecycleEnabled           = true
	defaultHeartbeatInterval          = 30 * time.Second
	defaultLifecycleRequestTimeout    = 5 * time.Second
	lifecycleEnabledEnvName           = "CREDIMI_RUNNER_LIFECYCLE_ENABLED"
	lifecycleHeartbeatIntervalEnvName = "CREDIMI_RUNNER_HEARTBEAT_INTERVAL"
)

type RunnerLifecycleConfig struct {
	Enabled           bool
	RunnerID          string
	CredimiURL        string
	APIKey            string
	HeartbeatInterval time.Duration
	RequestTimeout    time.Duration
}

type lifecyclePayload struct {
	RunnerID string `json:"runner_id"`
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
}

func LoadRunnerLifecycleConfig(instance utils.Instance) RunnerLifecycleConfig {
	cfg := RunnerLifecycleConfig{
		Enabled:           loadLifecycleEnabled(),
		RunnerID:          strings.TrimSpace(utils.GetEnvironmentVariable("CREDIMI_RUNNER_ID")),
		CredimiURL:        strings.TrimSpace(instance.URL),
		HeartbeatInterval: loadLifecycleDuration(lifecycleHeartbeatIntervalEnvName, defaultHeartbeatInterval),
		RequestTimeout:    defaultLifecycleRequestTimeout,
	}

	if strings.TrimSpace(instance.UserAPIKey) != "" {
		cfg.APIKey = strings.TrimSpace(instance.UserAPIKey)
	} else {
		cfg.APIKey = strings.TrimSpace(instance.InternalAdminKey)
	}

	return cfg
}

func NewRunnerLifecycleClient(cfg RunnerLifecycleConfig, httpClient HTTPClient, store *ProcessStore) *RunnerLifecycleClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &RunnerLifecycleClient{
		cfg:        cfg,
		httpClient: httpClient,
		store:      store,
		newTicker: func(interval time.Duration) lifecycleTicker {
			return timeTicker{ticker: time.NewTicker(interval)}
		},
		warnf: log.Printf,
	}
}

func (c *RunnerLifecycleClient) Resume(ctx context.Context, reason string) error {
	return c.post(ctx, []string{"api", "mobile-runner", "lifecycle", "resume"}, lifecyclePayload{
		RunnerID: c.cfg.RunnerID,
		Reason:   reason,
	})
}

func (c *RunnerLifecycleClient) Heartbeat(ctx context.Context) error {
	return c.post(ctx, []string{"api", "mobile-runner", "lifecycle", "heartbeat"}, lifecyclePayload{
		RunnerID: c.cfg.RunnerID,
		Reason:   "heartbeat",
	})
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
