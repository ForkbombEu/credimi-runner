package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

type setupCredentialRequest struct {
	InstanceURL string `json:"instance_url"`
	APIKey      string `json:"api_key"`
}

type setupOrganization struct {
	Name      string `json:"name"`
	Namespace string `json:"canonified_name"`
}

type setupRunnerPreviewRequest struct {
	InstanceURL  string `json:"instance_url"`
	APIKey       string `json:"api_key"`
	Organization string `json:"organization"`
	Name         string `json:"name"`
}

type setupDevicePreviewRequest struct {
	InstanceURL  string `json:"instance_url"`
	APIKey       string `json:"api_key"`
	Organization string `json:"organization"`
	RunnerID     string `json:"runner_id"`
	Name         string `json:"name"`
}

type setupRunnerPreview struct {
	Organization    string `json:"organization"`
	BaseRunnerID    string `json:"base_runner_id"`
	PreviewRunnerID string `json:"preview_runner_id"`
	RunnerID        string `json:"runner_id"`
	Conflict        bool   `json:"conflict"`
	DefaultAction   string `json:"default_action"`
}

func fetchCredimiOrganization(ctx context.Context, instanceURL, apiKey string) (setupOrganization, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, utils.JoinURL(instanceURL, "api", "organizations", "my"), nil)
	if err != nil {
		return setupOrganization{}, err
	}
	req.Header.Set("Credimi-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return setupOrganization{}, fmt.Errorf("organization lookup failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return setupOrganization{}, fmt.Errorf("organization lookup failed: %s", resp.Status)
	}
	var org setupOrganization
	if err := json.NewDecoder(resp.Body).Decode(&org); err != nil {
		return setupOrganization{}, fmt.Errorf("organization lookup returned invalid JSON: %w", err)
	}
	if org.Namespace == "" {
		return setupOrganization{}, fmt.Errorf("organization lookup returned an empty organization")
	}
	return org, nil
}

func fetchCredimiRunnerPreview(ctx context.Context, reqData setupRunnerPreviewRequest) (setupRunnerPreview, error) {
	body, err := json.Marshal(map[string]string{
		"name":         reqData.Name,
		"organization": reqData.Organization,
	})
	if err != nil {
		return setupRunnerPreview{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, utils.JoinURL(reqData.InstanceURL, "api", "mobile-runner", "preview-id"), bytes.NewReader(body))
	if err != nil {
		return setupRunnerPreview{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Credimi-Api-Key", reqData.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return setupRunnerPreview{}, fmt.Errorf("runner ID preview failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return setupRunnerPreview{}, fmt.Errorf("runner ID preview failed: %s", resp.Status)
	}
	var preview struct {
		Organization string `json:"organization"`
		RunnerID     string `json:"runner_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		return setupRunnerPreview{}, fmt.Errorf("runner ID preview returned invalid JSON: %w", err)
	}
	baseRunnerID := reqData.Organization + "/" + canonifyPlain(reqData.Name)
	previewRunnerID := strings.TrimPrefix(strings.TrimSpace(preview.RunnerID), "/")
	if previewRunnerID == "" {
		previewRunnerID = baseRunnerID
	}
	organization := preview.Organization
	if organization == "" {
		organization = reqData.Organization
	}
	return setupRunnerPreview{
		Organization:    organization,
		BaseRunnerID:    baseRunnerID,
		PreviewRunnerID: previewRunnerID,
		RunnerID:        baseRunnerID,
		Conflict:        previewRunnerID != baseRunnerID,
		DefaultAction:   "update",
	}, nil
}

func fetchCredimiCanonify(ctx context.Context, instanceURL, apiKey, name string) (string, error) {
	body, err := json.Marshal(map[string]string{"canonified_name": name})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, utils.JoinURL(instanceURL, "api", "canonify", "identifier", "validate"), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Credimi-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("canonify failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("canonify failed: %s", resp.Status)
	}
	var data struct {
		Record struct {
			Slug string `json:"slug"`
		} `json:"record"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("canonify returned invalid JSON: %w", err)
	}
	if data.Record.Slug == "" {
		return canonifyPlain(name), nil
	}
	return data.Record.Slug, nil
}

func canonifyPlain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "runner"
	}
	return out
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
