package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

type Organization struct {
	Name      string `json:"name"`
	Namespace string `json:"canonified_name"`
}

type RunnerPreview struct {
	Organization string `json:"organization"`
	RunnerID     string `json:"runner_id"`
}

type RegisterRunnerRequest struct {
	RunnerID     string `json:"runner_id"`
	Name         string `json:"name,omitempty"`
	IP           string `json:"ip,omitempty"`
	Description  string `json:"description,omitempty"`
	Type         string `json:"type,omitempty"`
	Port         string `json:"port,omitempty"`
	Serial       string `json:"serial,omitempty"`
	Organization string `json:"organization,omitempty"`
}

type CredimiClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func (c *CredimiClient) MyOrganization(ctx context.Context) (Organization, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, utils.JoinURL(c.BaseURL, "api", "organizations", "my"), nil)
	if err != nil {
		return Organization{}, err
	}
	req.Header.Set("Credimi-Api-Key", c.APIKey)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Organization{}, fmt.Errorf("organization lookup failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Organization{}, fmt.Errorf("organization lookup failed: %s", resp.Status)
	}
	var organization Organization
	if err := json.NewDecoder(resp.Body).Decode(&organization); err != nil {
		return Organization{}, fmt.Errorf("organization lookup returned invalid JSON: %w", err)
	}
	return organization, nil
}

func (c *CredimiClient) PreviewRunnerID(ctx context.Context, name, organization string) (RunnerPreview, error) {
	body, err := json.Marshal(map[string]string{
		"name":         strings.TrimSpace(name),
		"organization": strings.TrimSpace(organization),
	})
	if err != nil {
		return RunnerPreview{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, utils.JoinURL(c.BaseURL, "api", "mobile-runner", "preview-id"), bytes.NewReader(body))
	if err != nil {
		return RunnerPreview{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Credimi-Api-Key", c.APIKey)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return RunnerPreview{}, fmt.Errorf("runner ID preview failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RunnerPreview{}, fmt.Errorf("runner ID preview failed: %s", resp.Status)
	}
	var preview RunnerPreview
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		return RunnerPreview{}, fmt.Errorf("runner ID preview returned invalid JSON: %w", err)
	}
	if preview.RunnerID == "" {
		preview.RunnerID = strings.TrimSpace(organization) + "/" + canonifyPlain(name)
	}
	if preview.Organization == "" {
		preview.Organization = strings.TrimSpace(organization)
	}
	return preview, nil
}

func (c *CredimiClient) RegisterMobileRunner(ctx context.Context, request RegisterRunnerRequest) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, utils.JoinURL(c.BaseURL, "api", "mobile-runner"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Credimi-Api-Key", c.APIKey)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("mobile runner registration failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mobile runner registration failed: %s", resp.Status)
	}
	return nil
}

func (c *CredimiClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}
