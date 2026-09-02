package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	runnerconfig "github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/controller"
)

type controllerClient struct {
	metadata       controller.Metadata
	baseURL, token string
	client         *http.Client
}

func newControllerClient(ctx context.Context, configDir string) (*controllerClient, error) {
	metadata, err := controller.ReadMetadata(configDir)
	if err != nil {
		return nil, serviceNotRunningError()
	}
	if err := controller.Probe(ctx, metadata); err != nil {
		return nil, serviceNotRunningError()
	}
	cfg, err := runnerconfig.LoadFile(configDir + "/config.toml")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load dashboard authentication configuration: %w", err)
	}
	return &controllerClient{metadata: metadata, baseURL: strings.TrimRight(metadata.PublicURL, "/"), token: cfg.Server.DashboardToken, client: http.DefaultClient}, nil
}

func (c *controllerClient) request(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("controller request failed: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
func (c *controllerClient) getJSON(ctx context.Context, path string, out any) error {
	return c.request(ctx, http.MethodGet, path, out)
}
func (c *controllerClient) postJSON(ctx context.Context, path string, out any) error {
	return c.request(ctx, http.MethodPost, path, out)
}
