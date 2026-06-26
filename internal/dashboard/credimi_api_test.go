package dashboard

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCredimiAPIHelpers(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })

	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		statusCode := http.StatusOK
		status := "200 OK"
		switch req.URL.Path {
		case "/api/organizations/my":
			body = `{"name":"Acme","canonified_name":"acme"}`
		case "/api/mobile-runner/preview-id":
			body = `{"organization":"","runner_id":""}`
		case "/api/canonify/identifier/validate":
			body = `{"record":{}}`
		default:
			statusCode = http.StatusNotFound
			status = "404 Not Found"
		}
		return &http.Response{
			StatusCode: statusCode,
			Status:     status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	org, err := fetchCredimiOrganization(context.Background(), "https://credimi.example", "key")
	if err != nil || org.Namespace != "acme" {
		t.Fatalf("fetchCredimiOrganization = %#v %v", org, err)
	}

	preview, err := fetchCredimiRunnerPreview(context.Background(), setupRunnerPreviewRequest{
		InstanceURL:  "https://credimi.example",
		APIKey:       "key",
		Organization: "acme",
		Name:         "Runner Name",
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Organization != "acme" || preview.BaseRunnerID != "acme/runner-name" || preview.PreviewRunnerID != "acme/runner-name" || preview.Conflict {
		t.Fatalf("fetchCredimiRunnerPreview = %#v", preview)
	}

	slug, err := fetchCredimiCanonify(context.Background(), "https://credimi.example", "key", "Runner Name")
	if err != nil || slug != "runner-name" {
		t.Fatalf("fetchCredimiCanonify = %q %v", slug, err)
	}
}

func TestCredimiAPIHelperErrors(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })

	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		statusCode := http.StatusOK
		status := "200 OK"
		switch req.URL.Path {
		case "/api/organizations/my":
			body = `{"canonified_name":""}`
		case "/api/mobile-runner/preview-id":
			statusCode = http.StatusBadGateway
			status = "502 Bad Gateway"
		case "/api/canonify/identifier/validate":
			statusCode = http.StatusBadGateway
			status = "502 Bad Gateway"
		}
		return &http.Response{
			StatusCode: statusCode,
			Status:     status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	if _, err := fetchCredimiOrganization(context.Background(), "https://credimi.example", "key"); err == nil || !strings.Contains(err.Error(), "empty organization") {
		t.Fatalf("expected empty organization error, got %v", err)
	}
	if _, err := fetchCredimiRunnerPreview(context.Background(), setupRunnerPreviewRequest{
		InstanceURL:  "https://credimi.example",
		APIKey:       "key",
		Organization: "acme",
		Name:         "Runner Name",
	}); err == nil || !strings.Contains(err.Error(), "runner ID preview failed") {
		t.Fatalf("expected preview error, got %v", err)
	}
	if _, err := fetchCredimiCanonify(context.Background(), "https://credimi.example", "key", "Runner Name"); err == nil || !strings.Contains(err.Error(), "canonify failed") {
		t.Fatalf("expected canonify error, got %v", err)
	}
}
