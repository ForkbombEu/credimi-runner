package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchCredimiOrganization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/organizations/my" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Credimi-Api-Key"); got != "user-key" {
			t.Fatalf("api key header = %q", got)
		}
		_ = json.NewEncoder(w).Encode(setupOrganization{Name: "Org", Namespace: "org"})
	}))
	defer srv.Close()

	org, err := fetchCredimiOrganization(context.Background(), srv.URL, "user-key")
	if err != nil {
		t.Fatal(err)
	}
	if org.Namespace != "org" {
		t.Fatalf("namespace = %q", org.Namespace)
	}
}

func TestFetchCredimiRunnerPreview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mobile-runner/preview-id" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Credimi-Api-Key"); got != "user-key" {
			t.Fatalf("api key header = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["name"] != "Runner One" || body["organization"] != "org" {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(setupRunnerPreview{Organization: "org", RunnerID: "org/runner-one"})
	}))
	defer srv.Close()

	preview, err := fetchCredimiRunnerPreview(context.Background(), setupRunnerPreviewRequest{
		InstanceURL:  srv.URL,
		APIKey:       "user-key",
		Organization: "org",
		Name:         "Runner One",
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.RunnerID != "org/runner-one" {
		t.Fatalf("runner ID = %q", preview.RunnerID)
	}
}

func TestFetchCredimiCanonify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/canonify/identifier/validate" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Credimi-Api-Key"); got != "user-key" {
			t.Fatalf("api key header = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"record": map[string]string{"slug": "runner-one"},
		})
	}))
	defer srv.Close()

	slug, err := fetchCredimiCanonify(context.Background(), srv.URL, "user-key", "Runner One")
	if err != nil {
		t.Fatal(err)
	}
	if slug != "runner-one" {
		t.Fatalf("slug = %q", slug)
	}
}

func TestFetchCredimiCanonifyFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"record": map[string]string{}})
	}))
	defer srv.Close()

	slug, err := fetchCredimiCanonify(context.Background(), srv.URL, "user-key", " Runner One!! ")
	if err != nil {
		t.Fatal(err)
	}
	if slug != "runner-one" {
		t.Fatalf("fallback slug = %q", slug)
	}
}

func TestCanonifyPlain(t *testing.T) {
	tests := map[string]string{
		" Runner One!! ": "runner-one",
		"---":            "runner",
		"ACME_123":       "acme-123",
	}
	for in, want := range tests {
		if got := canonifyPlain(in); got != want {
			t.Fatalf("canonifyPlain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, map[string]string{"ok": "true"})
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
	}
	if got := rec.Body.String(); !bytes.Contains([]byte(got), []byte(`"ok":"true"`)) {
		t.Fatalf("body = %q", got)
	}
}
