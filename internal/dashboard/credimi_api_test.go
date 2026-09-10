package dashboard

import (
	"context"
	"errors"
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
			body = `{"organization":"acme","runner_id":"acme/runner-name"}`
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
	if preview.Organization != "acme" || preview.RunnerID != "acme/runner-name" || preview.Conflict {
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

func TestVerifySetupCredentialUsesCanonicalMode(t *testing.T) {
	original := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = original })
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"canonified_name":"user-org"}`
		if request.URL.Path == "/api/organizations/namespaces" {
			body = `{"namespaces":["admin-org"]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	user, err := verifySetupCredential(context.Background(), setupCredentialRequest{InstanceURL: "https://credimi.example", AuthMode: "user", APIKey: "user-key", Organization: "ignored"})
	if err != nil || user.Namespace != "user-org" {
		t.Fatalf("user verification = %#v, %v", user, err)
	}
	admin, err := verifySetupCredential(context.Background(), setupCredentialRequest{InstanceURL: "https://credimi.example", AuthMode: "internal_admin", APIKey: "admin-key", Organization: "admin-org"})
	if err != nil || admin.Namespace != "admin-org" {
		t.Fatalf("admin verification = %#v, %v", admin, err)
	}
	if _, err := verifySetupCredential(context.Background(), setupCredentialRequest{InstanceURL: "https://credimi.example", AuthMode: "internal_admin", APIKey: "admin-key", Organization: "missing"}); err == nil {
		t.Fatal("missing namespace was accepted")
	}
}

func TestFetchCredimiRunnerPreviewRejectsIncompleteConflict(t *testing.T) {
	original := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = original })
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"runner_id":"acme/runner-2","conflict":true}`))}, nil
	})}
	_, err := fetchCredimiRunnerPreview(context.Background(), setupRunnerPreviewRequest{InstanceURL: "https://credimi.example", APIKey: "key", Organization: "acme", Name: "Runner"})
	if err == nil || !strings.Contains(err.Error(), "conflict without an existing runner ID") {
		t.Fatalf("incomplete conflict error = %v", err)
	}
}

func TestSelectedCredimiAPIKeyUsesCanonicalMode(t *testing.T) {
	tests := []struct {
		name, mode, user, admin string
		want, errText           string
	}{
		{"user", "user", "u", "", "u", ""},
		{"user missing", "user", "", "", "", "exactly a user API key"},
		{"user both", "user", "u", "a", "", "exactly a user API key"},
		{"admin", "internal_admin", "", "a", "a", ""},
		{"admin missing", "internal_admin", "", "", "", "exactly an internal admin key"},
		{"admin both", "internal_admin", "u", "a", "", "exactly an internal admin key"},
		{"invalid", "other", "u", "", "", "mode must be user or internal_admin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectedCredimiAPIKey(map[string]string{
				"CREDIMI_AUTH_MODE":          test.mode,
				"CREDIMI_USER_API_KEY":       test.user,
				"CREDIMI_INTERNAL_ADMIN_KEY": test.admin,
			})
			if got != test.want || (test.errText == "" && err != nil) || (test.errText != "" && (err == nil || !strings.Contains(err.Error(), test.errText))) {
				t.Fatalf("selected key = %q, %v; want %q, error containing %q", got, err, test.want, test.errText)
			}
		})
	}
}

func TestVerifySetupCredentialRejectsCredentialEndpointFailures(t *testing.T) {
	original := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = original })
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, status := `{"canonified_name":"org"}`, http.StatusOK
		switch request.URL.Path {
		case "/api/organizations/namespaces":
			body = `{"namespaces":["org"]}`
		case "/api/organizations/my":
			status = http.StatusUnauthorized
			body = `{"message":"invalid key"}`
		}
		if request.URL.Path == "/api/organizations/namespaces" && request.Header.Get("Credimi-Api-Key") != "admin" {
			return nil, errors.New("missing Credimi-Api-Key")
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	for _, request := range []setupCredentialRequest{
		{AuthMode: "user", APIKey: "key"},
		{InstanceURL: "https://credimi.example", AuthMode: "user"},
		{InstanceURL: "https://credimi.example", AuthMode: "unknown", APIKey: "key"},
		{InstanceURL: "https://credimi.example", AuthMode: "internal_admin", APIKey: "admin"},
	} {
		if _, err := verifySetupCredential(context.Background(), request); err == nil {
			t.Fatalf("request %#v unexpectedly succeeded", request)
		}
	}
	if _, err := verifySetupCredential(context.Background(), setupCredentialRequest{InstanceURL: "https://credimi.example", AuthMode: "internal_admin", APIKey: "admin", Organization: "missing"}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing namespace error = %v", err)
	}
}
