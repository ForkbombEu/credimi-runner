package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

type setupCredentialRequest struct {
	InstanceURL  string `json:"instance_url"`
	AuthMode     string `json:"auth_mode"`
	APIKey       string `json:"api_key"`
	Organization string `json:"organization"`
}

type setupOrganization struct {
	Name      string `json:"name"`
	Namespace string `json:"canonified_name"`
}

// verifySetupCredential is the one authentication boundary used by the setup
// wizard.  A user key determines its organization; an internal-admin key must
// be paired with an existing namespace.  Keeping that distinction here avoids
// letting UI state or key presence choose an authentication mode.
func verifySetupCredential(ctx context.Context, request setupCredentialRequest) (setupOrganization, error) {
	request.InstanceURL = strings.TrimSpace(request.InstanceURL)
	request.AuthMode = strings.TrimSpace(request.AuthMode)
	request.APIKey = strings.TrimSpace(request.APIKey)
	request.Organization = strings.TrimSpace(request.Organization)
	if request.InstanceURL == "" || request.APIKey == "" {
		return setupOrganization{}, fmt.Errorf("Credimi URL and authentication key are required")
	}
	switch request.AuthMode {
	case "user":
		return fetchCredimiOrganization(ctx, request.InstanceURL, request.APIKey)
	case "internal_admin":
		if request.Organization == "" {
			return setupOrganization{}, fmt.Errorf("organization is required when using internal-admin authentication")
		}
		namespaces, err := fetchCredimiOrganizationNamespaces(ctx, request.InstanceURL, request.APIKey)
		if err != nil {
			return setupOrganization{}, err
		}
		for _, namespace := range namespaces {
			if namespace == request.Organization {
				return setupOrganization{Namespace: namespace}, nil
			}
		}
		return setupOrganization{}, fmt.Errorf("organization %q does not exist in Credimi", request.Organization)
	default:
		return setupOrganization{}, fmt.Errorf("authentication mode must be user or internal_admin")
	}
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
	Organization     string `json:"organization"`
	CanonifiedName   string `json:"canonified_name"`
	RunnerID         string `json:"runner_id"`
	ExistingRunnerID string `json:"existing_runner_id,omitempty"`
	Conflict         bool   `json:"conflict"`
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

func fetchCredimiOrganizationNamespaces(ctx context.Context, instanceURL, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, utils.JoinURL(instanceURL, "api", "organizations", "namespaces"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Credimi-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("organization namespace lookup failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("organization namespace lookup failed: %s", resp.Status)
	}
	var body struct {
		Namespaces []string `json:"namespaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("organization namespace lookup returned invalid JSON: %w", err)
	}
	return body.Namespaces, nil
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
	var preview setupRunnerPreview
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		return setupRunnerPreview{}, fmt.Errorf("runner ID preview returned invalid JSON: %w", err)
	}
	preview.RunnerID = strings.TrimPrefix(strings.TrimSpace(preview.RunnerID), "/")
	if preview.RunnerID == "" {
		return setupRunnerPreview{}, fmt.Errorf("runner ID preview returned an empty runner ID")
	}
	organization := preview.Organization
	if organization == "" {
		organization = reqData.Organization
	}
	preview.Organization = organization
	preview.ExistingRunnerID = strings.TrimPrefix(strings.TrimSpace(preview.ExistingRunnerID), "/")
	baseRunnerID := reqData.Organization + "/" + canonifyPlain(reqData.Name)
	preview.Conflict = preview.Conflict || preview.RunnerID != baseRunnerID
	if preview.Conflict && preview.ExistingRunnerID == "" {
		return setupRunnerPreview{}, errors.New("runner ID preview returned a conflict without an existing runner ID")
	}
	return preview, nil
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
