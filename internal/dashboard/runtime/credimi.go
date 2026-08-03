package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

type MobileRunnerListItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type MobileRunnerListResponse struct {
	Runners []MobileRunnerListItem `json:"runners"`
}

type RegisterRunnerRequest struct {
	RunnerID     string `json:"runner_id,omitempty"`
	Name         string `json:"name,omitempty"`
	IP           string `json:"ip,omitempty"`
	Description  string `json:"description,omitempty"`
	Port         string `json:"port,omitempty"`
	Organization string `json:"organization,omitempty"`
	Published    *bool  `json:"published,omitempty"`
}

type DevicePreview struct {
	RunnerID       string `json:"runner_id"`
	DeviceID       string `json:"device_id"`
	CanonifiedName string `json:"canonified_name"`
}

type RegisterDeviceRequest struct {
	Organization string `json:"organization,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	RunnerID     string `json:"runner_id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Type         string `json:"type"`
	Serial       string `json:"serial,omitempty"`
}

type CredimiClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

type CredimiStatusError struct {
	Prefix     string
	Status     string
	StatusCode int
	Body       string
}

func (e *CredimiStatusError) Error() string {
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("%s: %s", e.Prefix, e.Status)
	}
	return fmt.Sprintf("%s: %s: %s", e.Prefix, e.Status, e.Body)
}

func IsRunnerNameConflict(err error) bool {
	statusErr, ok := err.(*CredimiStatusError)
	if !ok {
		return false
	}
	return statusErr.StatusCode == http.StatusConflict &&
		strings.Contains(statusErr.Body, "runner_name_conflict")
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
		return Organization{}, credimiResponseError("organization lookup failed", resp)
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
		return RunnerPreview{}, credimiResponseError("runner ID preview failed", resp)
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
		return credimiResponseError("mobile runner registration failed", resp)
	}
	return nil
}

func (c *CredimiClient) PreviewDeviceID(ctx context.Context, runnerID, name, organization string) (DevicePreview, error) {
	body, err := json.Marshal(map[string]string{"organization": strings.TrimSpace(organization), "runner_id": runnerID, "name": name})
	if err != nil {
		return DevicePreview{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, utils.JoinURL(c.BaseURL, "api", "mobile-device", "preview-id"), bytes.NewReader(body))
	if err != nil {
		return DevicePreview{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Credimi-Api-Key", c.APIKey)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return DevicePreview{}, fmt.Errorf("device ID preview failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DevicePreview{}, credimiResponseError("device ID preview failed", resp)
	}
	var preview DevicePreview
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		return DevicePreview{}, fmt.Errorf("device ID preview returned invalid JSON: %w", err)
	}
	return preview, nil
}

func (c *CredimiClient) RegisterMobileDevice(ctx context.Context, request RegisterDeviceRequest) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, utils.JoinURL(c.BaseURL, "api", "mobile-device"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Credimi-Api-Key", c.APIKey)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("mobile device registration failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return credimiResponseError("mobile device registration failed", resp)
	}
	return nil
}

func (c *CredimiClient) RegisterMobileRunnerResolvingName(ctx context.Context, request RegisterRunnerRequest) error {
	err := c.RegisterMobileRunner(ctx, request)
	if !IsRunnerNameConflict(err) {
		return err
	}
	name, lookupErr := c.MobileRunnerName(ctx, request.RunnerID)
	if lookupErr != nil || strings.TrimSpace(name) == "" {
		return err
	}
	request.Name = name
	return c.RegisterMobileRunner(ctx, request)
}

func (c *CredimiClient) MobileRunnerName(ctx context.Context, runnerID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, utils.JoinURL(c.BaseURL, "api", "mobile-runners")+"?view=selector", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Credimi-Api-Key", c.APIKey)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("mobile runner lookup failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", credimiResponseError("mobile runner lookup failed", resp)
	}
	var list MobileRunnerListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", fmt.Errorf("mobile runner lookup returned invalid JSON: %w", err)
	}
	normalizedRunnerID := runnerIDWithoutLeadingSlash(runnerID)
	for _, runner := range list.Runners {
		if runnerIDWithoutLeadingSlash(runner.Path) == normalizedRunnerID {
			return strings.TrimSpace(runner.Name), nil
		}
	}
	return "", fmt.Errorf("mobile runner %q was not found in visible runners", normalizedRunnerID)
}

func credimiResponseError(prefix string, resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("%s: %s", prefix, resp.Status)
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		return &CredimiStatusError{Prefix: prefix, Status: resp.Status, StatusCode: resp.StatusCode}
	}
	return &CredimiStatusError{Prefix: prefix, Status: resp.Status, StatusCode: resp.StatusCode, Body: message}
}

func (c *CredimiClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}
