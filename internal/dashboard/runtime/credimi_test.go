package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCredimiClient(t *testing.T) {
	var registerPayload RegisterRunnerRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations/my":
			_ = json.NewEncoder(w).Encode(Organization{Name: "Acme", Namespace: "acme"})
		case "/api/mobile-runner/preview-id":
			_ = json.NewEncoder(w).Encode(RunnerPreview{Organization: "acme", RunnerID: "acme/runner"})
		case "/api/mobile-runner":
			_ = json.NewDecoder(r.Body).Decode(&registerPayload)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	org, err := client.MyOrganization(context.Background())
	if err != nil || org.Namespace != "acme" {
		t.Fatalf("organization = %#v err=%v", org, err)
	}
	preview, err := client.PreviewRunnerID(context.Background(), "Runner", "acme")
	if err != nil || preview.RunnerID != "acme/runner" {
		t.Fatalf("preview = %#v err=%v", preview, err)
	}
	err = client.RegisterMobileRunner(context.Background(), RegisterRunnerRequest{
		RunnerID: "acme/runner", Type: "android_phone", Serial: "device-1", Port: "443",
	})
	if err != nil {
		t.Fatal(err)
	}
	if registerPayload.Type != "android_phone" || registerPayload.Serial != "device-1" || registerPayload.Port != "443" {
		t.Fatalf("payload = %#v", registerPayload)
	}
}

func TestCredimiClientIncludesResponseBodyInErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"domain":"name","reason":"name is required"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	err := client.RegisterMobileRunner(context.Background(), RegisterRunnerRequest{
		RunnerID: "acme/runner",
		IP:       "https://runner.example",
	})
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("RegisterMobileRunner error = %v", err)
	}
}

func TestCredimiClientDetectsRunnerNameConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":{"reason":"runner_name_conflict"},"message":"name does not match the existing runner_id"}`))
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	err := client.RegisterMobileRunner(context.Background(), RegisterRunnerRequest{
		RunnerID: "acme/runner",
		Name:     "runner",
		IP:       "https://runner.example",
	})
	if !IsRunnerNameConflict(err) {
		t.Fatalf("IsRunnerNameConflict(%v) = false", err)
	}
}

func TestCredimiClientRetriesRegistrationWithStoredRunnerName(t *testing.T) {
	var attempts int
	var names []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/mobile-runner":
			attempts++
			var payload RegisterRunnerRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			names = append(names, payload.Name)
			if attempts == 1 {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":{"reason":"runner_name_conflict"},"message":"name does not match the existing runner_id"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/api/mobile-runners":
			_ = json.NewEncoder(w).Encode(MobileRunnerListResponse{
				Runners: []MobileRunnerListItem{{Name: "My Phone", Path: "acme/my-phone"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	err := client.RegisterMobileRunnerResolvingName(context.Background(), RegisterRunnerRequest{
		RunnerID: "acme/my-phone",
		Name:     "my-phone",
		IP:       "https://new.example.trycloudflare.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || strings.Join(names, ",") != "my-phone,My Phone" {
		t.Fatalf("attempts=%d names=%v", attempts, names)
	}
}

func TestCredimiClientRetriesRegistrationEvenWhenStoredNameLooksEqual(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/mobile-runner":
			attempts++
			if attempts == 1 {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":{"reason":"runner_name_conflict"},"message":"name does not match the existing runner_id"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/api/mobile-runners":
			_ = json.NewEncoder(w).Encode(MobileRunnerListResponse{
				Runners: []MobileRunnerListItem{{Name: "Test-Runner-Dashboard", Path: "filippo-s-organization/test-runner-dashboard"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	err := client.RegisterMobileRunnerResolvingName(context.Background(), RegisterRunnerRequest{
		RunnerID: "filippo-s-organization/test-runner-dashboard",
		Name:     "Test-Runner-Dashboard",
		IP:       "https://new.example.trycloudflare.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d", attempts)
	}
}
